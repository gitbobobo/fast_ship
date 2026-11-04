package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	ghclient "github.com/godbobo/fast_ship/server/internal/pkg/github"
	"github.com/godbobo/fast_ship/server/internal/repository"
	gh "github.com/google/go-github/v62/github"
	"go.uber.org/zap"
	"golang.org/x/sync/semaphore"
	"gorm.io/gorm"
)

// toIssueListQuery 把 service 层过滤器映射为 repository 的查询条件；
// q 在此统一 TrimSpace，repository 收到的 Query 保证已归一化。
func toIssueListQuery(projectID string, filters IssueListFilters) repository.IssueListQuery {
	return repository.IssueListQuery{
		ProjectID: projectID,
		State:     filters.State,
		Source:    filters.Source,
		Workflow:  filters.Workflow,
		Query:     strings.TrimSpace(filters.Query),
		Label:     filters.Label,
		Assignee:  filters.Assignee,
		Milestone: filters.Milestone,
	}
}

// List 过滤、排序、分页全部下推 SQL（INT-106）：单次 COUNT + 单次分页 SELECT
// （LIMIT/OFFSET，投影不含正文），meta/shipHook/未读/PR 聚合只按本页 Issue 查询，
// 读盘量与项目 Issue 总量解耦。列表响应不携带正文（Body/BodyHtml 为空串，
// 详情走 Get）。page 超过末页时返回空切片且 total 不变，与迁移前一致。
func (s *IssueService) List(projectID, userID string, filters IssueListFilters, page, pageSize int) ([]IssueResponse, int64, error) {
	if _, err := s.projectRepo.FindByID(projectID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, 0, errs.ErrProjectNotFound
		}
		return nil, 0, errs.ErrInternal
	}

	query := toIssueListQuery(projectID, filters)
	total, err := s.issueRepo.CountFiltered(query)
	if err != nil {
		return nil, 0, errs.ErrInternal
	}

	offset := (page - 1) * pageSize
	if offset < 0 {
		offset = 0
	}
	pageIssues, err := s.issueRepo.ListFiltered(query, filters.Sort, offset, pageSize)
	if err != nil {
		return nil, 0, errs.ErrInternal
	}

	metaByIssueID, err := s.internalMetaByIssueIDs(pageIssues)
	if err != nil {
		return nil, 0, errs.ErrInternal
	}

	labelMap := s.buildLabelMap(projectID)

	shipHooksByIssueID, err := s.shipHookService.shipHooksByIssueIDs(pageIssues)
	if err != nil {
		return nil, 0, err
	}

	unread, err := s.unreadCountsByIssueIDs(userID, pageIssues)
	if err != nil {
		return nil, 0, errs.ErrInternal
	}

	pullRequestSummaries, err := s.pullRequestSummariesByIssueIDs(pageIssues)
	if err != nil {
		return nil, 0, errs.ErrInternal
	}

	resp := make([]IssueResponse, 0, len(pageIssues))
	for _, issue := range pageIssues {
		item := s.toIssueResponse(
			issue,
			metaByIssueID[issue.ID],
			nil,
			labelMap,
			shipHooksByIssueID[issue.ID],
			int(unread[issue.ID]),
		)
		summary := pullRequestSummaries[issue.ID]
		item.PullRequestSummary = &IssuePullRequestSummaryResponse{
			Total:  summary.Total,
			Open:   summary.Open,
			Merged: summary.Merged,
		}
		resp = append(resp, item)
	}
	return resp, total, nil
}

func (s *IssueService) CountIssues(projectID, userID string, filters IssueListFilters) (int64, error) {
	if _, err := s.projectRepo.FindByID(projectID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, errs.ErrProjectNotFound
		}
		return 0, errs.ErrInternal
	}

	total, err := s.issueRepo.CountFiltered(toIssueListQuery(projectID, filters))
	if err != nil {
		return 0, errs.ErrInternal
	}
	return total, nil
}

func (s *IssueService) BatchCloseDoneIssues(ctx context.Context, projectID, userID, sourceFilter string) (*BatchCloseDoneIssuesResponse, error) {
	start := time.Now()
	if ctx == nil {
		ctx = context.Background()
	}

	project, err := s.projectRepo.FindByID(projectID, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrProjectNotFound
		}
		return nil, errs.ErrInternal
	}

	// 只取 done+open 的行，多取 1 条探测是否超上限，等价于迁移前的 total 判断；
	// 排序键取默认 updated_desc（本场景只关心行集，不关心顺序）。
	filters := IssueListFilters{
		State:    string(model.IssueStateOpen),
		Workflow: string(model.IssueWorkflowStatusDone),
		Source:   sourceFilter,
	}
	filtered, err := s.issueRepo.ListFiltered(toIssueListQuery(projectID, filters), "updated_desc", 0, batchCloseDoneMaxIssues+1)
	if err != nil {
		return nil, errs.ErrInternal
	}

	total := len(filtered)
	if total > batchCloseDoneMaxIssues {
		return nil, errs.ErrBatchCloseTooMany
	}

	// 30s 预算只管关闭阶段（内部事务 + GitHub PATCH）：前置的权限检查与
	// ListFiltered 本就不收 ctx，不该被整批截止截断；parent 仍是调用方 ctx，
	// 调用方取消照常生效（INT-185）。
	closeCtx, cancel := context.WithTimeout(ctx, batchCloseDoneTimeout)
	defer cancel()

	resp := &BatchCloseDoneIssuesResponse{
		Total:    int64(total),
		Failures: make([]BatchCloseDoneIssueFailure, 0),
	}

	var internalIssues []model.Issue
	var githubIssues []model.Issue
	for _, issue := range filtered {
		switch issue.Source {
		case model.IssueSourceInternal:
			internalIssues = append(internalIssues, issue)
		case model.IssueSourceGitHub:
			githubIssues = append(githubIssues, issue)
		default:
			appendBatchCloseFailure(resp, issue, errs.ErrIssueReadOnly)
		}
	}

	s.closeInternalIssues(closeCtx, internalIssues, resp)
	if len(githubIssues) > 0 {
		s.closeGitHubIssues(closeCtx, project, githubIssues, resp)
	}

	resp.ElapsedMs = time.Since(start).Milliseconds()
	s.logger.With(zap.String("user_id", userID)).Info("batch close done issues",
		zap.String("project_id", projectID),
		zap.String("user_id", userID),
		zap.String("source_filter", sourceFilter),
		zap.Int64("total", resp.Total),
		zap.Int("succeeded", resp.Succeeded),
		zap.Int("failed", resp.Failed),
		zap.Int64("elapsed_ms", resp.ElapsedMs),
	)
	return resp, nil
}

type githubCloseNetResult struct {
	issue   model.Issue
	updated *ghclient.Issue
	err     error
}

// batchCloseGitHubAbortReason：整批性失败类别，命中任一类即熔断，剩余项不再
// 派发。限流（INT-185）之外，INT-191 把认证失败（401）与无权限/访问受限
// （非限流 403）纳入——凭证失效或访问被拒时继续逐条 PATCH 只会得到同因重复
// 失败，最多白打 batchCloseDoneMaxIssues 条。404/410 成因两义——可能是仓库
// 级（token 失去可见性、issues 被禁用），也可能是单条（Issue 已转移/删除），
// 状态码无法区分；本期按单条处理，归 classifyGitHubCloseError 逐条回报，
// 不熔断（INT-191 验收钉死；仓库级取舍另立 INT-198）。限流 403 与非限流
// 403 的区分沿用 isGitHubRateLimitError 现有口径。
type batchCloseGitHubAbortReason int32

const (
	// abortNone：未熔断。
	abortNone batchCloseGitHubAbortReason = iota
	abortRateLimit
	abortAuth
	abortForbidden
)

// itemText：熔断触发项自身的失败文案。逐字固定、不含 URL/Issue 号，前端按
// 文案分组去重才有效（INT-185 R3/R4）。
func (r batchCloseGitHubAbortReason) itemText() string {
	switch r {
	case abortAuth:
		return "更新 GitHub Issue 失败：GitHub 认证失败，请检查凭证"
	case abortForbidden:
		// 403 成因不止仓库权限（企业策略、平台限制、反代/WAF 也返回 403），
		// 文案不写死「无权限」，避免把用户钉死在授权检查上；真实成因看服务端
		// warn 日志（issue_id + 底层 err，INT-191 R1 bot）。
		return "更新 GitHub Issue 失败：GitHub 仓库无权限或访问受限，请检查授权"
	default:
		return batchCloseGitHubRateLimitText
	}
}

// remainingText：熔断后未派发项的失败文案，与触发项文案区分——这些项根本
// 没发过请求，不能写成「结果未确认」。
func (r batchCloseGitHubAbortReason) remainingText() string {
	switch r {
	case abortAuth:
		return "GitHub 认证失败，未尝试关闭"
	case abortForbidden:
		return "GitHub 仓库无权限或访问受限，未尝试关闭"
	default:
		return batchCloseGitHubRateLimitRemainingText
	}
}

// closeOneGitHubIssue 执行单个 GitHub Issue 的关闭：PATCH 开始前先判定整批
// 取消（Acquire 成功不代表 worker 已调度，已取消的批次不得再发新请求），之后
// 经 WithoutCancel + 独立超时与整批取消解耦；结果只经 results 返回，由调用方
// 统一落库与计数。命中整批性失败（限流/认证/权限）时调用 onAbort（可为 nil）
// 上报类别，由调用方熔断剩余派发——这类失败继续派发只会得到同因重复失败；
// 404 等单条错误不熔断，逐条回报（INT-191）。
func closeOneGitHubIssue(batchCtx context.Context, client ghclient.Client, issue model.Issue, updateReq ghclient.UpdateIssueRequest, results chan<- githubCloseNetResult, onAbort func(batchCloseGitHubAbortReason), trip *atomic.Int32, guard func(ctx context.Context, id string) (bool, error), warn func(string, ...zap.Field)) {
	if issue.GitHubMeta == nil {
		results <- githubCloseNetResult{issue: issue, err: errs.ErrIssueReadOnly}
		return
	}
	if err := batchCtx.Err(); err != nil {
		// 归因当场定死（INT-185 R5）：错误产生于派发阶段，按当时的熔断状态
		// 归类；drain 阶段不再重读全局标志，避免时序改写失败原因。
		results <- githubCloseNetResult{issue: issue, err: batchCloseCancelErr(err, trip)}
		return
	}
	// 行级守卫（INT-185 R7）：发 PATCH 前按 id 复核本地行仍为 open+done——
	// 批量窗口内被拖回开发中的行不发 PATCH、按「已跳过关闭」回报；内部来源
	// 在事务 WHERE 里钉住同条件，GitHub 来源没有事务可依托，只能靠这里。
	if guard != nil {
		ok, gerr := guard(batchCtx, issue.ID)
		if gerr != nil {
			// 守卫带 batchCtx：批次被取消/熔断/超时时守卫本身也返回 ctx err，
			// 要按「未开始/限流」当场归因，不能误报为「复核失败」内部故障
			// （INT-185 R9）。驱动/连接池也可能把取消包装成非 ctx 错误，因此
			// 除 ctx err 判定外再回读批次状态：批次已死（ctx done 或已熔断）
			// 一律按取消归因，仅批次仍存活才报内部复核失败（INT-185 R11）。
			if errors.Is(gerr, context.Canceled) || errors.Is(gerr, context.DeadlineExceeded) ||
				batchCtx.Err() != nil || (trip != nil && trip.Load() != int32(abortNone)) {
				// gerr 是 ctx err 时透传；批次 ctx 仍活但已熔断（限流/认证/权限）、
				// 守卫返回非 ctx 包装错误时用 gerr 兜底，保证 err 非 nil。
				src := gerr
				if !errors.Is(gerr, context.Canceled) && !errors.Is(gerr, context.DeadlineExceeded) {
					src = batchCtx.Err()
					if src == nil {
						src = context.Canceled
					}
				}
				results <- githubCloseNetResult{issue: issue, err: batchCloseCancelErr(src, trip)}
				return
			}
			// 守卫非 ctx 错误（SQLITE_BUSY、连接池/驱动故障）：底层 gerr 先记
			// warn 再返回静态文案——数据库抖动整批变「复核失败」时线上要有
			// 证据定位，与 PATCH/落地失败的日志口径一致（INT-185 R15）。
			if warn != nil {
				warn("batch close github issue: guard failed",
					zap.String("issue_id", issue.ID), zap.Error(gerr))
			}
			results <- githubCloseNetResult{issue: issue, err: errs.New(errs.ErrInternal.Code, "复核本地状态失败，未关闭该项")}
			return
		}
		if !ok {
			results <- githubCloseNetResult{issue: issue, err: errBatchCloseSkippedNotOpen}
			return
		}
		// 守卫过后、发 PATCH 前再复核批次状态（INT-185 R8）：守卫本身可能
		// 在连接池里排了一阵，期间批次已被取消/超时的不再开始新 PATCH，
		// 按「未开始」计数。
		if err := batchCtx.Err(); err != nil {
			results <- githubCloseNetResult{issue: issue, err: batchCloseCancelErr(err, trip)}
			return
		}
	}
	patchCtx, cancel := context.WithTimeout(context.WithoutCancel(batchCtx), batchCloseGitHubPatchTimeout)
	defer cancel()
	updated, err := client.UpdateIssue(patchCtx, issue.GitHubMeta.Number, updateReq)
	if err != nil {
		if warn != nil {
			warn("batch close github issue: PATCH failed",
				zap.String("issue_id", issue.ID), zap.Int("number", issue.GitHubMeta.Number), zap.Error(err))
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			// PATCH 自身超时/取消：远端可能已生效，结果未确认，与
			// 「未开始」的取消区分开。
			err = &batchClosePatchUnconfirmedError{err: err}
		} else if reason, fatal := githubBatchCloseAbort(err); fatal {
			// 整批性失败（限流/认证/权限，INT-191）：触发项按类别文案回报并上报
			// 熔断；404 等单条错误走 classifyGitHubCloseError。
			err = errs.New(errs.ErrGitHubAPI.Code, reason.itemText())
			if onAbort != nil {
				onAbort(reason)
			}
		} else {
			err = classifyGitHubCloseError(err)
		}
		results <- githubCloseNetResult{issue: issue, err: err}
		return
	}
	results <- githubCloseNetResult{issue: issue, updated: updated}
}

// classifyGitHubCloseError 把剩余的 GitHub 错误收敛成少量固定类别（INT-185
// R3 方向审计）：5xx 与传输层错误在 PATCH 已发出的前提下结果未知，按「结果
// 未确认」处理，宁可多提示刷新确认；其余 ErrorResponse 规范化为「<状态码>
// <Message>」——go-github 的 Error() 带 METHOD 和完整 URL（含 Issue 号），
// 逐条不同会让前端分组去重失效，同一类错误必须逐字相同。
func classifyGitHubCloseError(err error) error {
	var respErr *gh.ErrorResponse
	if errors.As(err, &respErr) && respErr.Response != nil {
		if code := respErr.Response.StatusCode; code >= http.StatusInternalServerError {
			return &batchClosePatchUnconfirmedError{err: err}
		}
		msg := strings.TrimSpace(respErr.Message)
		if msg == "" {
			return errs.New(errs.ErrGitHubAPI.Code, fmt.Sprintf("更新 GitHub Issue 失败：%d", respErr.Response.StatusCode))
		}
		return errs.New(errs.ErrGitHubAPI.Code, fmt.Sprintf("更新 GitHub Issue 失败：%d %s", respErr.Response.StatusCode, msg))
	}
	// 非 ErrorResponse 的传输错误（*url.Error 等）：请求状态未知。
	return &batchClosePatchUnconfirmedError{err: err}
}

// classifyInternalCloseErr 归类内部事务失败（INT-185 R12/R13）：批次 ctx
// 已死（取消/超时）时归 ctx 自身 err——驱动/GORM 可能把取消包装成非 ctx
// 错误（SQLITE_INTERRUPT、SQLITE_BUSY、连接池关闭错误），此时不能误报为
// 「服务器内部错误」让用户重试；批次仍存活时 ctx err 透传、非 ctx 错误归
// ErrInternal 并记 warn。warn 为 nil 时不记。
func classifyInternalCloseErr(ctx context.Context, err error, warn func(string, ...zap.Field)) error {
	if batchErr := ctx.Err(); batchErr != nil {
		return batchErr
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if warn != nil {
		warn("batch close internal issues failed", zap.Error(err))
	}
	return errs.ErrInternal
}

func (s *IssueService) closeInternalIssues(ctx context.Context, issues []model.Issue, resp *BatchCloseDoneIssuesResponse) {
	if len(issues) == 0 {
		return
	}
	if err := ctx.Err(); err != nil {
		for _, issue := range issues {
			appendBatchCloseFailure(resp, issue, err)
		}
		return
	}

	ids := make([]string, len(issues))
	byID := make(map[string]model.Issue, len(issues))
	for i, issue := range issues {
		ids[i] = issue.ID
		byID[issue.ID] = issue
	}
	now := time.Now().UTC()
	var closedIDs []string
	// runCloseInternalTx 是测试缝（INT-185 R14 Note）：生产为 nil 走真实事务；
	// 测试注入一个返回驱动包装错误的函数，断言事务失败时按批次 ctx 归因。
	runTx := s.runCloseInternalTx
	var err error
	if runTx != nil {
		var closed []string
		closed, err = runTx(ids, now)
		closedIDs = closed
	} else {
		err = s.issueRepo.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			closed, err := s.issueRepo.BatchCloseInternalTx(tx, ids, now)
			if err != nil {
				return err
			}
			closedIDs = closed
			// 推荐只随真正关闭的行删除：未命中行保持原状，推荐与依赖一并保留。
			return s.recRepo.DeleteByIssueIDsTx(tx, closed)
		})
	}
	if err != nil {
		err = classifyInternalCloseErr(ctx, err, func(msg string, fields ...zap.Field) {
			s.logger.Warn(msg, fields...)
		})
		for _, issue := range issues {
			appendBatchCloseFailure(resp, issue, err)
		}
		return
	}
	resp.Succeeded += len(closedIDs)
	closedSet := make(map[string]struct{}, len(closedIDs))
	for _, id := range closedIDs {
		closedSet[id] = struct{}{}
	}
	for _, id := range ids {
		if _, ok := closedSet[id]; !ok {
			// 查询到关闭之间被并发方改写（已非 open）：不重复关闭也不删推荐，
			// 按失败逐条回报（INT-185）。
			appendBatchCloseFailure(resp, byID[id], errBatchCloseSkippedNotOpen)
		}
	}
}

func (s *IssueService) closeGitHubIssues(ctx context.Context, project *model.Project, issues []model.Issue, resp *BatchCloseDoneIssuesResponse) {
	if err := ctx.Err(); err != nil {
		for _, issue := range issues {
			appendBatchCloseFailure(resp, issue, err)
		}
		return
	}

	tokenBytes, appErr := s.credentials.requiredGitHubToken(project)
	if appErr != nil {
		for _, issue := range issues {
			appendBatchCloseFailure(resp, issue, appErr)
		}
		return
	}
	client := s.newClient(string(tokenBytes), project.GithubOwner, project.GithubRepo)

	closedState := string(model.IssueStateClosed)
	stateReason := string(api.UpdateIssueRequestStateReasonCompleted)
	updateReq := ghclient.UpdateIssueRequest{
		State:       &closedState,
		StateReason: &stateReason,
	}

	workers := int64(batchCloseGitHubConcurrency)
	if n := int64(len(issues)); n < workers {
		workers = n
	}
	sem := semaphore.NewWeighted(workers)
	results := make(chan githubCloseNetResult, len(issues))
	var wg sync.WaitGroup

	// 派发上下文与整批截止分开（INT-185 R2）：命中首个整批性失败（限流/认证/
	// 权限，INT-191）即熔断，剩余项不再向必然同样失败的 API 发请求；已派发的
	// PATCH 经 WithoutCancel 与本取消解耦，仍会走完并落库。槽位释放时序：每个
	// 触发 worker 都在释放槽位前幂等 cancel（见下方 onAbort，INT-191 R1），
	// 纯触发批次（如整批 401/403）槽位释放后派发循环的 Acquire 与 worker 顶部
	// 的 ctx 复核必然看到已取消，不再有额外 PATCH；混合批次中非触发项的槽位
	// 释放仍可能落在 trip 落地前的极小窗口内，至多再发出并发度条请求——有界
	// 且可接受。
	dispatchCtx, dispatchCancel := context.WithCancel(ctx)
	defer dispatchCancel()
	var trip atomic.Int32 // batchCloseGitHubAbortReason

	for _, issue := range issues {
		issue := issue
		// 熔断预检短路（INT-185 R2）：槽位释放后若已熔断，按类别回报「未尝试」，
		// 不再派发——不能让后续项走到 worker 守卫里以「已取消」文案弹回。
		if r := batchCloseGitHubAbortReason(trip.Load()); r != abortNone {
			results <- githubCloseNetResult{issue: issue, err: errs.New(errs.ErrGitHubAPI.Code, r.remainingText())}
			continue
		}
		if err := sem.Acquire(dispatchCtx, 1); err != nil {
			// 归因当场定死（INT-185 R5）：按 Acquire 失败当时的熔断状态归类。
			if r := batchCloseGitHubAbortReason(trip.Load()); r != abortNone {
				results <- githubCloseNetResult{issue: issue, err: errs.New(errs.ErrGitHubAPI.Code, r.remainingText())}
			} else {
				results <- githubCloseNetResult{issue: issue, err: batchCloseCancelErr(err, &trip)}
			}
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer sem.Release(1)
			closeOneGitHubIssue(dispatchCtx, client, issue, updateReq, results, func(r batchCloseGitHubAbortReason) {
				// 首个触发类别 CAS 生效，后续触发项只贡献类别不改写已定类别；
				// dispatchCancel 幂等：每个触发 worker 释放槽位前都确保取消已完成，
				// 否则 CAS 赢家在 cancel 前暂停时，输家先释放的槽位会被派发循环
				// 取走多发一条 PATCH（INT-191 R1 审查）。
				trip.CompareAndSwap(int32(abortNone), int32(r))
				dispatchCancel()
			}, &trip, func(ctx context.Context, id string) (bool, error) {
				return s.issueRepo.IsOpenAndDone(ctx, id)
			}, func(msg string, fields ...zap.Field) {
				s.logger.Warn(msg, fields...)
			})
		}()
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	storer := s.batchCloseStorer
	if storer == nil {
		storer = s.mirror
	}
	for res := range results {
		if res.err != nil {
			appendBatchCloseFailure(resp, res.issue, res.err)
			continue
		}
		// PATCH 已成功（res.err==nil 且 updated 有值），本地落地失败——GitHub
		// 侧已关闭而本地可能仍 open，必须按「结果未确认」提示刷新，不能用
		// 普通「服务器内部错误」让用户以为没关成功而重复操作（INT-185 R10）。
		stored, err := storer.upsertGitHubIssue(project.ID, res.updated)
		if err != nil {
			s.logger.Warn("batch close github issue: local upsert failed",
				zap.String("issue_id", res.issue.ID), zap.Error(err))
			appendBatchCloseFailure(resp, res.issue, &batchClosePatchUnconfirmedError{err: err})
			continue
		}
		if err := storer.markCollectionsStaleAfterPush(stored.ID); err != nil {
			s.logger.Warn("batch close github issue: mark stale failed",
				zap.String("issue_id", stored.ID), zap.Error(err))
			appendBatchCloseFailure(resp, res.issue, &batchClosePatchUnconfirmedError{err: err})
			continue
		}
		resp.Succeeded++
	}
}

// batchCloseMirrorStorer 抽象「PATCH 成功后本地落地」的两个调用点，便于测试
// 注入失败路径（INT-185 R11 Note）。生产路径由 *issueMirror 实现。
type batchCloseMirrorStorer interface {
	upsertGitHubIssue(projectID string, item *ghclient.Issue) (*model.Issue, error)
	markCollectionsStaleAfterPush(issueID string) error
}

func appendBatchCloseFailure(resp *BatchCloseDoneIssuesResponse, issue model.Issue, err error) {
	appendBatchFailureEntry(resp, buildBatchCloseFailure(issue, err))
}

// appendBatchFailureEntry 明细上限内追加；达到上限后，未出现过的新类别样例
// 仍保留（每类至少出现一次，INT-185 R4/R7），同类不重复追加。批量关闭与
// 批量 internal-meta 共用，保证共享契约描述对两个端点都成立。
func appendBatchFailureEntry(resp *BatchCloseDoneIssuesResponse, entry BatchCloseDoneIssueFailure) {
	resp.Failed++
	if len(resp.Failures) >= batchCloseDoneMaxFailures {
		for _, f := range resp.Failures {
			if f.Error == entry.Error {
				return
			}
		}
	}
	resp.Failures = append(resp.Failures, entry)
}

func buildBatchCloseFailure(issue model.Issue, err error) BatchCloseDoneIssueFailure {
	return BatchCloseDoneIssueFailure{
		Id:        issue.ID,
		Reference: api.Ptr(buildIssueReference(issue)),
		Error:     batchCloseItemError(err),
	}
}

// batchCloseCancelErr：派发阶段的 ctx err 按当时的熔断状态当场定死归因——
// 熔断已触发→按触发类别的「未尝试」文案；否则保留原始 ctx err（整批截止/
// 调用方取消）。drain 阶段不再重读全局标志，避免同一批次的归因随 goroutine
// 时序漂移（INT-185 R5）。
func batchCloseCancelErr(err error, trip *atomic.Int32) error {
	if trip != nil {
		if r := batchCloseGitHubAbortReason(trip.Load()); r != abortNone {
			return errs.New(errs.ErrGitHubAPI.Code, r.remainingText())
		}
	}
	return err
}

// batchClosePatchUnconfirmedError 标记「PATCH 已发出但结果未确认」：请求可能
// 已在 GitHub 侧生效（Issue 已被关闭），只是没等到响应。文案必须提示刷新确认，
// 不能与「未开始关闭」的取消混为一谈（INT-185）。
type batchClosePatchUnconfirmedError struct{ err error }

func (e *batchClosePatchUnconfirmedError) Error() string { return e.err.Error() }
func (e *batchClosePatchUnconfirmedError) Unwrap() error { return e.err }

// errBatchCloseSkippedNotOpen：关闭事务内未命中（并发方已把行改出 open），
// 不计入成功，按失败逐条回报。
var errBatchCloseSkippedNotOpen = errors.New("该问题已在其他操作中被更新，已跳过关闭")

const batchCloseGitHubRateLimitText = "更新 GitHub Issue 失败：GitHub API 限流，请稍后重试"

// batchCloseGitHubRateLimitRemainingText：限流熔断后未派发项的失败文案（
// INT-185）；认证/权限类别的同位文案见 batchCloseGitHubAbortReason.remainingText。
const batchCloseGitHubRateLimitRemainingText = "GitHub API 限流，未尝试关闭"

// githubBatchCloseAbort 判定 PATCH 错误是否为整批性失败并给出类别：限流
// （口径见 isGitHubRateLimitError：*RateLimitError、*AbuseRateLimitError、
// 429、含限流文案的 403）、认证失败（401）、无权限（其余 403）。404 与其他
// 状态码按单条结果逐条回报，不得熔断（INT-191）：404/410 的仓库级成因
// （token 失去可见性、issues 被禁用）与单条成因（已转移/删除）状态码无法
// 区分，本期的取舍与候选口径见 INT-198。
func githubBatchCloseAbort(err error) (batchCloseGitHubAbortReason, bool) {
	if isGitHubRateLimitError(err) {
		return abortRateLimit, true
	}
	var respErr *gh.ErrorResponse
	if !errors.As(err, &respErr) || respErr.Response == nil {
		return abortNone, false
	}
	switch respErr.Response.StatusCode {
	case http.StatusUnauthorized:
		return abortAuth, true
	case http.StatusForbidden:
		// 含限流文案的 403 已在上面归入限流；落到这里的 403 成因不止仓库权限
		//（企业策略、平台限制、反代/WAF 也可能），但同一仓库/凭证下重试只会
		// 同因失败，熔断不变；归为「无权限或访问受限」类别，不写死成因。
		return abortForbidden, true
	}
	return abortNone, false
}

// isGitHubRateLimitError 判定 go-github 错误是否为 API 限流：*RateLimitError、
// *AbuseRateLimitError，或 403/429 且响应体含 rate limit/abuse detection 字样的
// *ErrorResponse。判定口径与 PR 域的 pullRequestAccessHint 对齐但不共用
// （那边是 githubapi.StatusError 的 PR 读取域）；Message 是响应体 JSON 解析出的
// message 字段，原始 body 由 go-github 解析后回填，可尽力重读。
func isGitHubRateLimitError(err error) bool {
	var rateLimit *gh.RateLimitError
	if errors.As(err, &rateLimit) {
		return true
	}
	var abuse *gh.AbuseRateLimitError
	if errors.As(err, &abuse) {
		return true
	}
	var respErr *gh.ErrorResponse
	if !errors.As(err, &respErr) || respErr.Response == nil {
		return false
	}
	code := respErr.Response.StatusCode
	// 429 本身是权威限流信号，无需依赖 body 文案——空 body、纯 Retry-After
	// 或网关/WAF 自定义文案的 429 也必须触发熔断（INT-185 R13）。
	if code == http.StatusTooManyRequests {
		return true
	}
	if code != http.StatusForbidden {
		return false
	}
	text := strings.ToLower(respErr.Message)
	if raw := readGitHubErrorBody(respErr.Response.Body); raw != "" {
		text += " " + strings.ToLower(raw)
	}
	return strings.Contains(text, "rate limit") || strings.Contains(text, "abuse detection")
}

// readGitHubErrorBody 尽力读取错误响应体；已被读走或关闭时返回空串，不影响
// 判定（还有 Message 兜底）。
func readGitHubErrorBody(body io.ReadCloser) string {
	if body == nil {
		return ""
	}
	raw, err := io.ReadAll(body)
	if err != nil {
		return ""
	}
	return string(raw)
}

func batchCloseItemError(err error) string {
	var unconfirmed *batchClosePatchUnconfirmedError
	if errors.As(err, &unconfirmed) {
		return "关闭结果未确认：GitHub 侧可能已关闭，请刷新查看"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "请求已取消或超时，未继续关闭"
	}
	var appErr *errs.AppError
	if errors.As(err, &appErr) {
		return appErr.Message
	}
	return err.Error()
}

// GetFilterOptions 走只读投影查询（INT-106）：不读整行 Issue，只取两张 meta 表的
// 标签/指派人/里程碑 JSON，去重与排序行为与迁移前一致。
func (s *IssueService) GetFilterOptions(projectID, userID string) (*IssueFilterOptionsResponse, error) {
	if _, err := s.projectRepo.FindByID(projectID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrProjectNotFound
		}
		return nil, errs.ErrInternal
	}

	sources, err := s.issueRepo.ListFilterOptionSources(projectID)
	if err != nil {
		return nil, errs.ErrInternal
	}

	labelSet := make(map[string]struct{})
	assigneeSet := make(map[string]struct{})
	milestoneSet := make(map[string]struct{})

	for _, source := range sources {
		for _, name := range extractLabelNames(source.LabelsJSON) {
			if name != "" {
				labelSet[name] = struct{}{}
			}
		}
		for _, assignee := range parseJSON[[]issueUserPayload](source.AssigneesJSON) {
			if assignee.Login != "" {
				assigneeSet[assignee.Login] = struct{}{}
			}
		}
		if milestone := parseJSON[*issueMilestonePayload](source.MilestoneJSON); milestone != nil && milestone.Title != "" {
			milestoneSet[milestone.Title] = struct{}{}
		}
		for _, name := range extractLabelNames(source.InternalLabelsJSON) {
			if name != "" {
				labelSet[name] = struct{}{}
			}
		}
	}

	return &IssueFilterOptionsResponse{
		Labels:     sortedKeys(labelSet),
		Assignees:  sortedKeys(assigneeSet),
		Milestones: sortedKeys(milestoneSet),
	}, nil
}

func (s *IssueService) GetRepositoryLabels(projectID, userID string) ([]IssueLabelResponse, error) {
	project, err := s.projectRepo.FindByID(projectID, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrProjectNotFound
		}
		return nil, errs.ErrInternal
	}

	tokenBytes, appErr := s.credentials.requiredGitHubToken(project)
	if appErr != nil {
		return nil, appErr
	}

	cached, err := s.githubRepoLabelRepo.ListByProject(projectID)
	if err == nil && len(cached) > 0 {
		labels := make([]IssueLabelResponse, 0, len(cached))
		for _, item := range cached {
			labels = append(labels, IssueLabelResponse{
				Name:        item.Name,
				Color:       item.Color,
				Description: item.Description,
			})
		}
		return labels, nil
	}

	client := s.newClient(string(tokenBytes), project.GithubOwner, project.GithubRepo)
	const perPage = 100
	page := 1
	labelsByKey := make(map[string]IssueLabelResponse)

	for {
		items, resp, err := client.ListRepositoryLabels(context.Background(), page, perPage)
		if err != nil {
			return nil, errs.New(errs.ErrGitHubAPI.Code, fmt.Sprintf("获取 GitHub 标签失败: %v", err))
		}

		for _, item := range items {
			if item == nil {
				continue
			}
			name := strings.TrimSpace(item.GetName())
			if name == "" {
				continue
			}
			key := strings.ToLower(name)
			if _, ok := labelsByKey[key]; ok {
				continue
			}
			labelsByKey[key] = IssueLabelResponse{
				Name:        name,
				Color:       item.GetColor(),
				Description: item.GetDescription(),
			}
		}

		if resp == nil || resp.NextPage == 0 {
			break
		}
		page = resp.NextPage
	}

	labels := make([]IssueLabelResponse, 0, len(labelsByKey))
	for _, item := range labelsByKey {
		labels = append(labels, item)
	}
	sort.Slice(labels, func(i, j int) bool {
		return strings.ToLower(labels[i].Name) < strings.ToLower(labels[j].Name)
	})

	now := time.Now().UTC()
	for _, item := range labels {
		if err := s.githubRepoLabelRepo.Save(&model.GitHubRepoLabel{
			ProjectID:   projectID,
			Name:        item.Name,
			Color:       item.Color,
			Description: item.Description,
			SyncedAt:    now,
		}); err != nil {
			s.logger.With(zap.String("user_id", userID)).Warn("缓存仓库标签失败", zap.String("project_id", projectID), zap.String("label", item.Name), zap.Error(err))
		}
	}

	return labels, nil
}

func (s *IssueService) GetSyncState(projectID, userID string) (*IssueSyncStateResponse, error) {
	if _, err := s.projectRepo.FindByID(projectID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrProjectNotFound
		}
		return nil, errs.ErrInternal
	}

	state, err := s.syncStateRepo.GetOrCreate(projectID)
	if err != nil {
		return nil, errs.ErrInternal
	}

	return toIssueSyncStateResponse(state), nil
}

func (s *IssueService) Get(issueID, userID string) (*IssueResponse, error) {
	issue, err := s.issueRepo.FindByID(issueID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrIssueNotFound
		}
		return nil, errs.ErrInternal
	}

	if _, err := s.projectRepo.FindByID(issue.ProjectID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrProjectNotFound
		}
		return nil, errs.ErrNotOwner
	}

	meta, err := s.loadInternalMeta(issue.ID)
	if err != nil {
		return nil, err
	}
	checklist, err := s.loadChecklist(issue.ID)
	if err != nil {
		return nil, err
	}

	shipHook, err := s.shipHookService.loadShipHook(issue.ID)
	if err != nil {
		return nil, err
	}

	attachments, err := s.attachmentRepo.ListByIssueID(issue.ID)
	if err != nil {
		return nil, errs.ErrInternal
	}

	resp := s.toIssueResponse(*issue, meta, checklist, nil, shipHook, 0)
	if len(attachments) > 0 {
		resp.Attachments = toIssueAttachmentResponses(attachments)
	}

	annotations, err := s.annotationRepo.ListByIssueID(issue.ID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	if len(annotations) > 0 {
		related, err := s.annotationRepo.LoadRelated(annotations)
		if err != nil {
			return nil, errs.ErrInternal
		}
		resp.ScreenshotAnnotations = assembleScreenshotAnnotations(
			annotations, related,
			map[string]model.Issue{issue.ID: *issue},
			func(screen *model.ScreenshotScreen) (int, int) {
				return resolveScreenshotDims(s.storage, screen)
			},
		)
	}
	return &resp, nil
}
