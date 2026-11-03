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

// batchCloseGitHubRateLimitRemainingText：熔断后未派发项的失败文案，与单条
// 限流文案区分——这些项根本没发过请求，不能写成「结果未确认」。
const batchCloseGitHubRateLimitRemainingText = "GitHub API 限流，未尝试关闭"

// closeOneGitHubIssue 执行单个 GitHub Issue 的关闭：PATCH 开始前先判定整批
// 取消（Acquire 成功不代表 worker 已调度，已取消的批次不得再发新请求），之后
// 经 WithoutCancel + 独立超时与整批取消解耦；结果只经 results 返回，由调用方
// 统一落库与计数。命中 API 限流时调用 onRateLimit（可为 nil），由调用方熔断
// 剩余派发——限流后继续打满配额只会得到重复失败，次级限流还会延长封禁。
func closeOneGitHubIssue(batchCtx context.Context, client ghclient.Client, issue model.Issue, updateReq ghclient.UpdateIssueRequest, results chan<- githubCloseNetResult, onRateLimit func(), rateLimited *atomic.Bool, guard func(ctx context.Context, id string) (bool, error)) {
	if issue.GitHubMeta == nil {
		results <- githubCloseNetResult{issue: issue, err: errs.ErrIssueReadOnly}
		return
	}
	if err := batchCtx.Err(); err != nil {
		// 归因当场定死（INT-185 R5）：错误产生于派发阶段，按当时的熔断状态
		// 归类；drain 阶段不再重读全局标志，避免时序改写失败原因。
		results <- githubCloseNetResult{issue: issue, err: batchCloseCancelErr(err, rateLimited)}
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
				batchCtx.Err() != nil || (rateLimited != nil && rateLimited.Load()) {
				// gerr 是 ctx err 时透传；批次 ctx 仍活但已熔断（限流）、守卫返回
				// 非 ctx 包装错误时用 gerr 兜底，保证 err 非 nil。
				src := gerr
				if !errors.Is(gerr, context.Canceled) && !errors.Is(gerr, context.DeadlineExceeded) {
					src = batchCtx.Err()
					if src == nil {
						src = context.Canceled
					}
				}
				results <- githubCloseNetResult{issue: issue, err: batchCloseCancelErr(src, rateLimited)}
				return
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
			results <- githubCloseNetResult{issue: issue, err: batchCloseCancelErr(err, rateLimited)}
			return
		}
	}
	patchCtx, cancel := context.WithTimeout(context.WithoutCancel(batchCtx), batchCloseGitHubPatchTimeout)
	defer cancel()
	updated, err := client.UpdateIssue(patchCtx, issue.GitHubMeta.Number, updateReq)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			// PATCH 自身超时/取消：远端可能已生效，结果未确认，与
			// 「未开始」的取消区分开。
			err = &batchClosePatchUnconfirmedError{err: err}
		} else if isGitHubRateLimitError(err) {
			err = errs.New(errs.ErrGitHubAPI.Code, batchCloseGitHubRateLimitText)
			if onRateLimit != nil {
				onRateLimit()
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
	err := s.issueRepo.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		closed, err := s.issueRepo.BatchCloseInternalTx(tx, ids, now)
		if err != nil {
			return err
		}
		closedIDs = closed
		// 推荐只随真正关闭的行删除：未命中行保持原状，推荐与依赖一并保留。
		return s.recRepo.DeleteByIssueIDsTx(tx, closed)
	})
	if err != nil {
		// 除判定 err 本身是 ctx err 外再回读批次状态：驱动/GORM 可能把批次
		// 取消/超时包装成非 ctx 错误（SQLITE_INTERRUPT、连接池关闭错误），
		// 批次已死时不能误报为「服务器内部错误」让用户重试（INT-185 R12）。
		if batchErr := ctx.Err(); batchErr != nil {
			err = batchErr
		} else if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			s.logger.Warn("batch close internal issues failed", zap.Error(err))
			err = errs.ErrInternal
		}
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

	// 派发上下文与整批截止分开（INT-185 R2）：命中首个限流即熔断，剩余项不再
	// 向已被限流的 API 发请求；已派发的 PATCH 经 WithoutCancel 与本取消解耦，
	// 仍会走完并落库。熔断与相邻 worker 的槽位释放没有跨 goroutine 时序保证，
	// trip 落地前后的极小窗口内至多再发出并发度条请求——有界且可接受，不为
	// 此引入同步 trip。
	dispatchCtx, dispatchCancel := context.WithCancel(ctx)
	defer dispatchCancel()
	var rateLimited atomic.Bool

	for _, issue := range issues {
		issue := issue
		// 熔断预检短路（INT-185 R2）：槽位释放后若已限流，不再派发——不能让
		// 后续项走到 worker 守卫里以「已取消」文案弹回。
		if rateLimited.Load() {
			results <- githubCloseNetResult{issue: issue, err: errs.New(errs.ErrGitHubAPI.Code, batchCloseGitHubRateLimitRemainingText)}
			continue
		}
		if err := sem.Acquire(dispatchCtx, 1); err != nil {
			// 归因当场定死（INT-185 R5）：按 Acquire 失败当时的熔断状态归类。
			if rateLimited.Load() {
				results <- githubCloseNetResult{issue: issue, err: errs.New(errs.ErrGitHubAPI.Code, batchCloseGitHubRateLimitRemainingText)}
			} else {
				results <- githubCloseNetResult{issue: issue, err: batchCloseCancelErr(err, &rateLimited)}
			}
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer sem.Release(1)
			closeOneGitHubIssue(dispatchCtx, client, issue, updateReq, results, func() {
				if rateLimited.CompareAndSwap(false, true) {
					dispatchCancel()
				}
			}, &rateLimited, func(ctx context.Context, id string) (bool, error) {
				return s.issueRepo.IsOpenAndDone(ctx, id)
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
			appendBatchCloseFailure(resp, res.issue, &batchClosePatchUnconfirmedError{err: err})
			continue
		}
		if err := storer.markCollectionsStaleAfterPush(stored.ID); err != nil {
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
// 熔断已触发→「限流未尝试」；否则保留原始 ctx err（整批截止/调用方取消）。
// drain 阶段不再重读全局标志，避免同一批次的归因随 goroutine 时序漂移
// （INT-185 R5）。
func batchCloseCancelErr(err error, rateLimited *atomic.Bool) error {
	if rateLimited != nil && rateLimited.Load() {
		return errs.New(errs.ErrGitHubAPI.Code, batchCloseGitHubRateLimitRemainingText)
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
	if code != http.StatusForbidden && code != http.StatusTooManyRequests {
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
