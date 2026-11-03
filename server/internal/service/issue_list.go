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

// closeOneGitHubIssue 执行单个 GitHub Issue 的关闭：PATCH 开始前先判定整批
// 取消（Acquire 成功不代表 worker 已调度，已取消的批次不得再发新请求），之后
// 经 WithoutCancel + 独立超时与整批取消解耦；结果只经 results 返回，由调用方
// 统一落库与计数。
func closeOneGitHubIssue(batchCtx context.Context, client ghclient.Client, issue model.Issue, updateReq ghclient.UpdateIssueRequest, results chan<- githubCloseNetResult) {
	if issue.GitHubMeta == nil {
		results <- githubCloseNetResult{issue: issue, err: errs.ErrIssueReadOnly}
		return
	}
	if err := batchCtx.Err(); err != nil {
		results <- githubCloseNetResult{issue: issue, err: err}
		return
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
		} else {
			err = errs.New(errs.ErrGitHubAPI.Code, fmt.Sprintf("更新 GitHub Issue 失败: %v", err))
		}
		results <- githubCloseNetResult{issue: issue, err: err}
		return
	}
	results <- githubCloseNetResult{issue: issue, updated: updated}
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
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
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

	for _, issue := range issues {
		issue := issue
		if err := sem.Acquire(ctx, 1); err != nil {
			results <- githubCloseNetResult{issue: issue, err: err}
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer sem.Release(1)
			closeOneGitHubIssue(ctx, client, issue, updateReq, results)
		}()
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	for res := range results {
		if res.err != nil {
			appendBatchCloseFailure(resp, res.issue, res.err)
			continue
		}
		stored, err := s.mirror.upsertGitHubIssue(project.ID, res.updated)
		if err != nil {
			appendBatchCloseFailure(resp, res.issue, err)
			continue
		}
		if err := s.mirror.markCollectionsStaleAfterPush(stored.ID); err != nil {
			appendBatchCloseFailure(resp, res.issue, err)
			continue
		}
		resp.Succeeded++
	}
}

func appendBatchCloseFailure(resp *BatchCloseDoneIssuesResponse, issue model.Issue, err error) {
	resp.Failed++
	if len(resp.Failures) >= batchCloseDoneMaxFailures {
		return
	}
	resp.Failures = append(resp.Failures, BatchCloseDoneIssueFailure{
		Id:        issue.ID,
		Reference: api.Ptr(buildIssueReference(issue)),
		Error:     batchCloseItemError(err),
	})
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
