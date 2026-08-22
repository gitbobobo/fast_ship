package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/crypto"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	ghclient "github.com/godbobo/fast_ship/server/internal/pkg/github"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"strings"
	"time"
)

func (s *IssueService) SyncProjectIssues(projectID, userID string) (*IssueSyncResponse, error) {
	project, err := s.projectRepo.FindByID(projectID, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrProjectNotFound
		}
		return nil, errs.ErrInternal
	}

	if !project.IsGitHubConfigured() {
		return nil, errs.ErrProjectGitHubNotConfigured
	}

	return s.syncProject(context.Background(), project)
}

func (s *IssueService) SyncAllProjectsIncremental(ctx context.Context) {
	projects, err := s.projectRepo.ListAll()
	if err != nil {
		s.logger.Error("list projects for issue sync failed", zap.Error(err))
		return
	}

	for i := range projects {
		project := projects[i]
		if ctx.Err() != nil {
			return
		}
		if !project.IsGitHubConfigured() {
			continue
		}
		if _, err := s.syncProject(ctx, &project); err != nil {
			s.logger.Warn("background issue sync failed", zap.String("project_id", project.ID), zap.Error(err))
		}
	}
}

func (s *IssueService) syncProject(ctx context.Context, project *model.Project) (*IssueSyncResponse, error) {
	if !s.beginSync(project.ID) {
		return nil, errs.ErrIssueSyncRunning
	}
	defer s.endSync(project.ID)

	state, err := s.syncStateRepo.GetOrCreate(project.ID)
	if err != nil {
		return nil, errs.ErrInternal
	}

	startedAt := time.Now().UTC()
	state.Status = model.IssueSyncStatusRunning
	state.LastError = ""
	state.LastSyncedAt = &startedAt
	if err := s.syncStateRepo.Save(state); err != nil {
		return nil, errs.ErrInternal
	}

	failSync := func(syncErr error) (*IssueSyncResponse, error) {
		failedAt := time.Now().UTC()
		state.Status = model.IssueSyncStatusFailed
		state.LastSyncedAt = &failedAt
		state.LastError = syncErr.Error()
		_ = s.syncStateRepo.Save(state)
		return nil, syncErr
	}

	tokenBytes, appErr := s.decryptGitHubToken(project)
	if appErr != nil {
		return failSync(appErr)
	}

	client := s.newClient(string(tokenBytes), project.GithubOwner, project.GithubRepo)
	if err := client.ValidateRepository(ctx); err != nil {
		return failSync(errs.New(errs.ErrGitHubAPI.Code, "无法访问 GitHub 仓库或 Token 无效"))
	}

	if err := s.syncRepositoryLabels(ctx, client, project.ID); err != nil {
		s.logger.Warn("同步仓库标签失败", zap.String("project_id", project.ID), zap.Error(err))
	}

	var since *time.Time
	if state.LastIssueUpdatedAt != nil {
		t := state.LastIssueUpdatedAt.Add(-1 * time.Second)
		since = &t
	}

	const perPage = 100
	var (
		page              = 1
		syncedIssues      int
		syncedComments    int
		syncedTimeline    int
		maxIssueUpdatedAt *time.Time
	)

	for {
		items, resp, err := client.ListIssues(ctx, "all", since, page, perPage)
		if err != nil {
			return failSync(errs.New(errs.ErrGitHubAPI.Code, fmt.Sprintf("同步 GitHub Issues 失败: %v", err)))
		}

		for _, item := range items {
			if item == nil || item.IsPullRequest() || item.GetID() == 0 {
				continue
			}

			issue, syncErr := s.upsertGitHubIssue(project.ID, item)
			if syncErr != nil {
				return failSync(syncErr)
			}

			commentCount, syncErr := s.syncComments(ctx, client, issue, item.GetNumber())
			if syncErr != nil {
				return failSync(syncErr)
			}
			timelineCount, syncErr := s.syncTimeline(ctx, client, issue, item.GetNumber())
			if syncErr != nil {
				return failSync(syncErr)
			}

			syncedIssues++
			syncedComments += commentCount
			syncedTimeline += timelineCount

			updatedAt := item.GetUpdatedAt().UTC()
			if maxIssueUpdatedAt == nil || updatedAt.After(*maxIssueUpdatedAt) {
				maxIssueUpdatedAt = &updatedAt
			}
		}

		if resp == nil || resp.NextPage == 0 {
			break
		}
		page = resp.NextPage
	}

	completedAt := time.Now().UTC()
	state.Status = model.IssueSyncStatusCompleted
	state.LastSyncedAt = &completedAt
	state.LastSuccessfulSyncAt = &completedAt
	state.LastError = ""
	if maxIssueUpdatedAt != nil {
		state.LastIssueUpdatedAt = maxIssueUpdatedAt
	}
	if err := s.syncStateRepo.Save(state); err != nil {
		return nil, errs.ErrInternal
	}

	resp := &IssueSyncResponse{
		ProjectID:           project.ID,
		SyncedIssueCount:    syncedIssues,
		SyncedCommentCount:  syncedComments,
		SyncedTimelineCount: syncedTimeline,
		StartedAt:           formatTime(startedAt),
		CompletedAt:         formatTime(completedAt),
	}
	if state.LastIssueUpdatedAt != nil {
		value := formatTime(state.LastIssueUpdatedAt.UTC())
		resp.LastIssueUpdatedAt = &value
	}
	return resp, nil
}

func (s *IssueService) syncRepositoryLabels(ctx context.Context, client gitHubIssueClient, projectID string) error {
	const perPage = 100
	page := 1
	now := time.Now().UTC()
	var allLabels []model.GitHubRepoLabel

	for {
		items, resp, err := client.ListRepositoryLabels(ctx, page, perPage)
		if err != nil {
			return err
		}

		for _, item := range items {
			if item == nil {
				continue
			}
			name := strings.TrimSpace(item.GetName())
			if name == "" {
				continue
			}
			allLabels = append(allLabels, model.GitHubRepoLabel{
				ProjectID:   projectID,
				Name:        name,
				Color:       item.GetColor(),
				Description: item.GetDescription(),
				SyncedAt:    now,
			})
		}

		if resp == nil || resp.NextPage == 0 {
			break
		}
		page = resp.NextPage
	}

	if err := s.githubRepoLabelRepo.ReplaceAllForProject(projectID, allLabels); err != nil {
		return err
	}
	return nil
}

func (s *IssueService) upsertGitHubIssue(projectID string, item *ghclient.Issue) (*model.Issue, error) {
	now := time.Now().UTC()

	var issue *model.Issue
	meta, err := s.gitHubMetaRepo.FindByProjectAndGitHubID(projectID, item.GetID())
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errs.ErrInternal
	}

	if meta != nil {
		issue, err = s.issueRepo.FindByID(meta.IssueID)
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrInternal
		}
	}

	if issue == nil {
		sequenceNumber, err := s.issueRepo.NextSequenceNumber(projectID)
		if err != nil {
			return nil, errs.ErrInternal
		}
		issue = &model.Issue{
			ID:             uuid.NewString(),
			ProjectID:      projectID,
			Source:         model.IssueSourceGitHub,
			SequenceNumber: sequenceNumber,
		}
	}

	issue.Source = model.IssueSourceGitHub
	issue.State = model.IssueState(item.GetState())
	issue.StateReason = item.GetStateReason()
	issue.Title = item.GetTitle()
	issue.Body = item.GetBody()
	issue.BodyHTML = item.GetBodyHTML()
	issue.AuthorUserID = ""
	issue.AuthorLogin = item.GetUser().GetLogin()
	issue.AuthorAvatarURL = item.GetUser().GetAvatarURL()
	issue.CreatedAt = item.GetCreatedAt().UTC()
	issue.UpdatedAt = item.GetUpdatedAt().UTC()
	if closedAt := item.GetClosedAt(); !closedAt.IsZero() {
		value := closedAt.UTC()
		issue.ClosedAt = &value
	} else {
		issue.ClosedAt = nil
	}

	if meta == nil {
		if err := s.issueRepo.Create(issue); err != nil {
			return nil, errs.ErrInternal
		}
	} else {
		if err := s.issueRepo.Save(issue); err != nil {
			return nil, errs.ErrInternal
		}
	}

	gitHubMeta := &model.IssueGitHubMeta{
		IssueID:           issue.ID,
		ProjectID:         projectID,
		GitHubIssueID:     item.GetID(),
		GitHubNodeID:      item.GetNodeID(),
		Number:            item.GetNumber(),
		HTMLURL:           item.GetHTMLURL(),
		AuthorAssociation: item.GetAuthorAssociation(),
		AssigneesJSON:     toJSONString(mapUsers(item.Assignees)),
		LabelsJSON:        toJSONString(mapLabelNames(item.Labels)),
		MilestoneJSON:     toJSONString(mapMilestone(item.Milestone)),
		ReactionsJSON:     toJSONString(mapReactions(item.Reactions)),
		CommentsCount:     item.GetComments(),
		Locked:            item.GetLocked(),
		ActiveLockReason:  item.GetActiveLockReason(),
		SyncedAt:          now,
		RawJSON:           toJSONString(item),
	}

	if err := s.gitHubMetaRepo.Upsert(gitHubMeta); err != nil {
		return nil, errs.ErrInternal
	}

	stored, err := s.issueRepo.FindByID(issue.ID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	return stored, nil
}

func (s *IssueService) syncComments(ctx context.Context, client gitHubIssueClient, issue *model.Issue, issueNumber int) (int, error) {
	const perPage = 100
	page := 1
	commentIDs := make([]int64, 0)
	synced := 0

	for {
		items, resp, err := client.ListIssueComments(ctx, issueNumber, page, perPage)
		if err != nil {
			return synced, errs.New(errs.ErrGitHubAPI.Code, fmt.Sprintf("同步 Issue 评论失败: %v", err))
		}

		for _, item := range items {
			if item == nil || item.GetID() == 0 {
				continue
			}
			comment := &model.IssueComment{
				ID:                uuid.NewString(),
				IssueID:           issue.ID,
				Source:            model.IssueSourceGitHub,
				GitHubCommentID:   item.GetID(),
				GitHubNodeID:      item.GetNodeID(),
				Body:              item.GetBody(),
				BodyHTML:          item.GetBodyHTML(),
				HTMLURL:           item.GetHTMLURL(),
				AuthorLogin:       item.GetUser().GetLogin(),
				AuthorAvatarURL:   item.GetUser().GetAvatarURL(),
				AuthorAssociation: item.GetAuthorAssociation(),
				ReactionsJSON:     toJSONString(mapReactions(item.Reactions)),
				GitHubCreatedAt:   item.GetCreatedAt().UTC(),
				GitHubUpdatedAt:   item.GetUpdatedAt().UTC(),
				RawJSON:           toJSONString(item),
			}
			if err := s.commentRepo.Upsert(comment); err != nil {
				return synced, errs.ErrInternal
			}
			commentIDs = append(commentIDs, item.GetID())
			synced++
		}

		if resp == nil || resp.NextPage == 0 {
			break
		}
		page = resp.NextPage
	}

	if err := s.commentRepo.DeleteMissing(issue.ID, commentIDs); err != nil {
		return synced, errs.ErrInternal
	}
	return synced, nil
}

func (s *IssueService) syncTimeline(ctx context.Context, client gitHubIssueClient, issue *model.Issue, issueNumber int) (int, error) {
	const perPage = 100
	page := 1
	eventKeys := make([]string, 0)
	synced := 0

	for {
		items, resp, err := client.ListIssueTimeline(ctx, issueNumber, page, perPage)
		if err != nil {
			return synced, errs.New(errs.ErrGitHubAPI.Code, fmt.Sprintf("同步 Issue 动态失败: %v", err))
		}

		for _, item := range items {
			if item == nil {
				continue
			}
			event := &model.IssueTimelineEvent{
				ID:              uuid.NewString(),
				IssueID:         issue.ID,
				EventKey:        buildTimelineEventKey(item),
				GitHubEventID:   item.GetID(),
				EventType:       item.GetEvent(),
				ActorLogin:      firstNonEmpty(item.GetActor().GetLogin(), item.GetUser().GetLogin()),
				ActorAvatarURL:  firstNonEmpty(item.GetActor().GetAvatarURL(), item.GetUser().GetAvatarURL()),
				Body:            item.GetBody(),
				Summary:         summarizeTimeline(item),
				PayloadJSON:     toJSONString(item),
				GitHubCreatedAt: item.GetCreatedAt().UTC(),
			}
			if err := s.timelineRepo.Upsert(event); err != nil {
				return synced, errs.ErrInternal
			}
			eventKeys = append(eventKeys, event.EventKey)
			synced++
		}

		if resp == nil || resp.NextPage == 0 {
			break
		}
		page = resp.NextPage
	}

	if err := s.timelineRepo.DeleteMissing(issue.ID, eventKeys); err != nil {
		return synced, errs.ErrInternal
	}
	return synced, nil
}

func (s *IssueService) decryptGitHubToken(project *model.Project) ([]byte, *errs.AppError) {
	if !project.IsGitHubConfigured() {
		return nil, errs.ErrProjectGitHubNotConfigured
	}
	tokenBytes, err := crypto.Decrypt(project.GithubTokenEncrypted, []byte(s.cfg.Encryption.Key))
	if err != nil {
		s.logger.Error("decrypt github token failed for issue sync", zap.Error(err))
		return nil, errs.ErrInternal
	}
	return tokenBytes, nil
}

func (s *IssueService) beginSync(projectID string) bool {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	if _, exists := s.syncingProjectID[projectID]; exists {
		return false
	}
	s.syncingProjectID[projectID] = struct{}{}
	return true
}

func (s *IssueService) endSync(projectID string) {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	delete(s.syncingProjectID, projectID)
}
