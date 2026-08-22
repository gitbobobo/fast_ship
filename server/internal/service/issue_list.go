package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"sort"
	"strings"
	"time"
)

func (s *IssueService) loadFilteredIssues(projectID string, filters IssueListFilters) ([]model.Issue, map[string]*model.IssueInternalMeta, error) {
	issues, err := s.issueRepo.ListByProject(projectID)
	if err != nil {
		return nil, nil, errs.ErrInternal
	}

	metaByIssueID, err := s.internalMetaByIssueIDs(issues)
	if err != nil {
		return nil, nil, errs.ErrInternal
	}

	filtered := make([]model.Issue, 0, len(issues))
	for _, issue := range issues {
		var meta *model.IssueInternalMeta
		if current, ok := metaByIssueID[issue.ID]; ok {
			meta = current
		}
		if !matchesIssueFilters(issue, issue.GitHubMeta, meta, filters) {
			continue
		}
		filtered = append(filtered, issue)
	}
	return filtered, metaByIssueID, nil
}

func (s *IssueService) List(projectID, userID string, filters IssueListFilters, page, pageSize int) ([]IssueResponse, int64, error) {
	if _, err := s.projectRepo.FindByID(projectID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, 0, errs.ErrProjectNotFound
		}
		return nil, 0, errs.ErrInternal
	}

	filtered, metaByIssueID, err := s.loadFilteredIssues(projectID, filters)
	if err != nil {
		return nil, 0, err
	}
	sortIssues(filtered, filters.Sort)

	total := int64(len(filtered))
	start := (page - 1) * pageSize
	if start > len(filtered) {
		start = len(filtered)
	}
	end := start + pageSize
	if end > len(filtered) {
		end = len(filtered)
	}

	labelMap := s.buildLabelMap(projectID)

	pageIssues := filtered[start:end]
	shipHooksByIssueID, err := s.shipHookService.shipHooksByIssueIDs(pageIssues)
	if err != nil {
		return nil, 0, err
	}

	resp := make([]IssueResponse, 0, len(pageIssues))
	for _, issue := range pageIssues {
		resp = append(resp, s.toIssueResponse(issue, metaByIssueID[issue.ID], nil, labelMap, shipHooksByIssueID[issue.ID]))
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

	filtered, _, err := s.loadFilteredIssues(projectID, filters)
	if err != nil {
		return 0, err
	}
	return int64(len(filtered)), nil
}

func (s *IssueService) BatchCloseDoneIssues(projectID, userID, sourceFilter string) (*BatchCloseDoneIssuesResponse, error) {
	start := time.Now()
	if _, err := s.projectRepo.FindByID(projectID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrProjectNotFound
		}
		return nil, errs.ErrInternal
	}

	filters := IssueListFilters{
		State:    string(model.IssueStateOpen),
		Workflow: string(model.IssueWorkflowStatusDone),
		Source:   sourceFilter,
	}
	filtered, _, err := s.loadFilteredIssues(projectID, filters)
	if err != nil {
		return nil, err
	}

	total := len(filtered)
	if total > batchCloseDoneMaxIssues {
		return nil, errs.ErrBatchCloseTooMany
	}

	closedState := model.IssueStateClosed
	stateReason := "completed"
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
					ID:        issue.ID,
					Reference: buildIssueReference(issue),
					Error:     msg,
				})
			}
			continue
		}
		resp.Succeeded++
	}

	resp.ElapsedMs = time.Since(start).Milliseconds()
	s.logger.Info("batch close done issues",
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

func (s *IssueService) GetFilterOptions(projectID, userID string) (*IssueFilterOptionsResponse, error) {
	if _, err := s.projectRepo.FindByID(projectID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrProjectNotFound
		}
		return nil, errs.ErrInternal
	}

	issues, err := s.issueRepo.ListByProject(projectID)
	if err != nil {
		return nil, errs.ErrInternal
	}

	internalMetaByIssueID, err := s.internalMetaByIssueIDs(issues)
	if err != nil {
		return nil, errs.ErrInternal
	}

	labelSet := make(map[string]struct{})
	assigneeSet := make(map[string]struct{})
	milestoneSet := make(map[string]struct{})

	for _, issue := range issues {
		meta := issue.GitHubMeta
		if meta != nil {
			for _, name := range extractLabelNames(meta.LabelsJSON) {
				if name != "" {
					labelSet[name] = struct{}{}
				}
			}
			for _, assignee := range parseJSON[[]issueUserPayload](meta.AssigneesJSON) {
				if assignee.Login != "" {
					assigneeSet[assignee.Login] = struct{}{}
				}
			}
			if milestone := parseJSON[*issueMilestonePayload](meta.MilestoneJSON); milestone != nil && milestone.Title != "" {
				milestoneSet[milestone.Title] = struct{}{}
			}
		}
		if iMeta, ok := internalMetaByIssueID[issue.ID]; ok && iMeta.LabelsJSON != "" {
			for _, name := range extractLabelNames(iMeta.LabelsJSON) {
				if name != "" {
					labelSet[name] = struct{}{}
				}
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

	tokenBytes, appErr := s.decryptGitHubToken(project)
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
			s.logger.Warn("缓存仓库标签失败", zap.String("project_id", projectID), zap.String("label", item.Name), zap.Error(err))
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

	resp := s.toIssueResponse(*issue, meta, checklist, nil, shipHook)
	return &resp, nil
}
