package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"sort"
	"strings"
	"time"
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

func (s *IssueService) BatchCloseDoneIssues(projectID, userID, sourceFilter string) (*BatchCloseDoneIssuesResponse, error) {
	start := time.Now()
	if _, err := s.projectRepo.FindByID(projectID, userID); err != nil {
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

	closedState := model.IssueStateClosed
	stateReason := api.UpdateIssueRequestStateReasonCompleted
	resp := &BatchCloseDoneIssuesResponse{
		Total:    int64(total),
		Failures: make([]BatchCloseDoneIssueFailure, 0),
	}

	for _, issue := range filtered {
		_, updateErr := s.UpdateInternalIssue(issue.ID, userID, UpdateInternalIssueRequest{
			State:       &closedState,
			StateReason: &stateReason,
		})

		if updateErr != nil {
			resp.Failed++
			if len(resp.Failures) < batchCloseDoneMaxFailures {
				msg := updateErr.Error()
				var appErr *errs.AppError
				if errors.As(updateErr, &appErr) {
					msg = appErr.Message
				}
				resp.Failures = append(resp.Failures, BatchCloseDoneIssueFailure{
					Id:        issue.ID,
					Reference: api.Ptr(buildIssueReference(issue)),
					Error:     msg,
				})
			}
			continue
		}
		resp.Succeeded++
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
