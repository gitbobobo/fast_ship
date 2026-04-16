package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/crypto"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	ghclient "github.com/godbobo/fast_ship/server/internal/pkg/github"
	"github.com/godbobo/fast_ship/server/internal/repository"
	gh "github.com/google/go-github/v62/github"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type gitHubIssueClient interface {
	ValidateRepository(ctx context.Context) error
	ListIssues(ctx context.Context, state string, since *time.Time, page, perPage int) ([]*ghclient.Issue, *gh.Response, error)
	ListIssueComments(ctx context.Context, issueNumber, page, perPage int) ([]*ghclient.IssueComment, *gh.Response, error)
	ListIssueTimeline(ctx context.Context, issueNumber, page, perPage int) ([]*ghclient.TimelineEvent, *gh.Response, error)
}

type gitHubIssueClientFactory func(token, owner, repo string) gitHubIssueClient

type IssueService struct {
	issueRepo        *repository.IssueRepository
	commentRepo      *repository.IssueCommentRepository
	timelineRepo     *repository.IssueTimelineRepository
	syncStateRepo    *repository.IssueSyncStateRepository
	projectRepo      *repository.ProjectRepository
	cfg              *config.Config
	logger           *zap.Logger
	newClient        gitHubIssueClientFactory
	syncMu           sync.Mutex
	syncingProjectID map[string]struct{}
}

type IssueListFilters struct {
	State     string
	Query     string
	Label     string
	Assignee  string
	Milestone string
	Sort      string
}

type IssueActorResponse struct {
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url"`
}

type IssueLabelResponse struct {
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description"`
}

type IssueMilestoneResponse struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	State       string `json:"state"`
	Description string `json:"description"`
}

type IssueReactionSummaryResponse struct {
	TotalCount int `json:"total_count"`
	PlusOne    int `json:"+1"`
	MinusOne   int `json:"-1"`
	Laugh      int `json:"laugh"`
	Hooray     int `json:"hooray"`
	Confused   int `json:"confused"`
	Heart      int `json:"heart"`
	Rocket     int `json:"rocket"`
	Eyes       int `json:"eyes"`
}

type IssueResponse struct {
	ID                string                       `json:"id"`
	ProjectID         string                       `json:"project_id"`
	GitHubIssueID     int64                        `json:"github_issue_id"`
	GitHubNodeID      string                       `json:"github_node_id"`
	Number            int                          `json:"number"`
	State             model.IssueState             `json:"state"`
	StateReason       string                       `json:"state_reason"`
	Title             string                       `json:"title"`
	Body              string                       `json:"body"`
	BodyHTML          string                       `json:"body_html"`
	HTMLURL           string                       `json:"html_url"`
	Author            IssueActorResponse           `json:"author"`
	AuthorAssociation string                       `json:"author_association"`
	Assignees         []IssueActorResponse         `json:"assignees"`
	Labels            []IssueLabelResponse         `json:"labels"`
	Milestone         *IssueMilestoneResponse      `json:"milestone,omitempty"`
	Reactions         IssueReactionSummaryResponse `json:"reactions"`
	CommentsCount     int                          `json:"comments_count"`
	Locked            bool                         `json:"locked"`
	ActiveLockReason  string                       `json:"active_lock_reason"`
	ClosedAt          *string                      `json:"closed_at"`
	CreatedAt         string                       `json:"created_at"`
	UpdatedAt         string                       `json:"updated_at"`
	SyncedAt          string                       `json:"synced_at"`
}

type IssueCommentResponse struct {
	ID                string                       `json:"id"`
	IssueID           string                       `json:"issue_id"`
	GitHubCommentID   int64                        `json:"github_comment_id"`
	GitHubNodeID      string                       `json:"github_node_id"`
	Body              string                       `json:"body"`
	BodyHTML          string                       `json:"body_html"`
	HTMLURL           string                       `json:"html_url"`
	Author            IssueActorResponse           `json:"author"`
	AuthorAssociation string                       `json:"author_association"`
	Reactions         IssueReactionSummaryResponse `json:"reactions"`
	CreatedAt         string                       `json:"created_at"`
	UpdatedAt         string                       `json:"updated_at"`
}

type IssueTimelineEventResponse struct {
	ID            string             `json:"id"`
	IssueID       string             `json:"issue_id"`
	EventKey      string             `json:"event_key"`
	EventType     string             `json:"event_type"`
	GitHubEventID int64              `json:"github_event_id"`
	Actor         IssueActorResponse `json:"actor"`
	Body          string             `json:"body"`
	Summary       string             `json:"summary"`
	Payload       map[string]any     `json:"payload"`
	CreatedAt     string             `json:"created_at"`
}

type IssueSyncResponse struct {
	ProjectID           string  `json:"project_id"`
	SyncedIssueCount    int     `json:"synced_issue_count"`
	SyncedCommentCount  int     `json:"synced_comment_count"`
	SyncedTimelineCount int     `json:"synced_timeline_count"`
	StartedAt           string  `json:"started_at"`
	CompletedAt         string  `json:"completed_at"`
	LastIssueUpdatedAt  *string `json:"last_issue_updated_at,omitempty"`
}

type IssueFilterOptionsResponse struct {
	Labels     []string `json:"labels"`
	Assignees  []string `json:"assignees"`
	Milestones []string `json:"milestones"`
}

type IssueSyncStateResponse struct {
	Status               model.IssueSyncStatus `json:"status"`
	LastIssueUpdatedAt   *string               `json:"last_issue_updated_at,omitempty"`
	LastSyncedAt         *string               `json:"last_synced_at,omitempty"`
	LastSuccessfulSyncAt *string               `json:"last_successful_sync_at,omitempty"`
	LastError            string                `json:"last_error"`
}

type issueUserPayload struct {
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url"`
}

type issueLabelPayload struct {
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description"`
}

type issueMilestonePayload struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	State       string `json:"state"`
	Description string `json:"description"`
}

func NewIssueService(
	issueRepo *repository.IssueRepository,
	commentRepo *repository.IssueCommentRepository,
	timelineRepo *repository.IssueTimelineRepository,
	syncStateRepo *repository.IssueSyncStateRepository,
	projectRepo *repository.ProjectRepository,
	cfg *config.Config,
	logger *zap.Logger,
) *IssueService {
	return &IssueService{
		issueRepo:     issueRepo,
		commentRepo:   commentRepo,
		timelineRepo:  timelineRepo,
		syncStateRepo: syncStateRepo,
		projectRepo:   projectRepo,
		cfg:           cfg,
		logger:        logger,
		newClient: func(token, owner, repo string) gitHubIssueClient {
			return ghclient.NewClient(token, owner, repo)
		},
		syncingProjectID: make(map[string]struct{}),
	}
}

func (s *IssueService) List(projectID, userID string, filters IssueListFilters, page, pageSize int) ([]IssueResponse, int64, error) {
	if _, err := s.projectRepo.FindByID(projectID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, 0, errs.ErrProjectNotFound
		}
		return nil, 0, errs.ErrInternal
	}

	issues, err := s.issueRepo.ListByProject(projectID)
	if err != nil {
		return nil, 0, errs.ErrInternal
	}

	filtered := make([]model.Issue, 0, len(issues))
	for _, issue := range issues {
		if !matchesIssueFilters(issue, filters) {
			continue
		}
		filtered = append(filtered, issue)
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

	resp := make([]IssueResponse, 0, end-start)
	for _, issue := range filtered[start:end] {
		resp = append(resp, toIssueResponse(issue))
	}
	return resp, total, nil
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

	labelSet := make(map[string]struct{})
	assigneeSet := make(map[string]struct{})
	milestoneSet := make(map[string]struct{})

	for _, issue := range issues {
		for _, label := range parseJSON[[]issueLabelPayload](issue.LabelsJSON) {
			if label.Name != "" {
				labelSet[label.Name] = struct{}{}
			}
		}
		for _, assignee := range parseJSON[[]issueUserPayload](issue.AssigneesJSON) {
			if assignee.Login != "" {
				assigneeSet[assignee.Login] = struct{}{}
			}
		}
		if milestone := parseJSON[*issueMilestonePayload](issue.MilestoneJSON); milestone != nil && milestone.Title != "" {
			milestoneSet[milestone.Title] = struct{}{}
		}
	}

	return &IssueFilterOptionsResponse{
		Labels:     sortedKeys(labelSet),
		Assignees:  sortedKeys(assigneeSet),
		Milestones: sortedKeys(milestoneSet),
	}, nil
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

	resp := toIssueResponse(*issue)
	return &resp, nil
}

func (s *IssueService) ListComments(issueID, userID string, page, pageSize int) ([]IssueCommentResponse, int64, error) {
	issue, err := s.issueRepo.FindByID(issueID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, 0, errs.ErrIssueNotFound
		}
		return nil, 0, errs.ErrInternal
	}
	if _, err := s.projectRepo.FindByID(issue.ProjectID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, 0, errs.ErrProjectNotFound
		}
		return nil, 0, errs.ErrNotOwner
	}

	comments, total, err := s.commentRepo.List(issueID, page, pageSize)
	if err != nil {
		return nil, 0, errs.ErrInternal
	}

	resp := make([]IssueCommentResponse, 0, len(comments))
	for _, comment := range comments {
		resp = append(resp, toIssueCommentResponse(comment))
	}
	return resp, total, nil
}

func (s *IssueService) ListTimeline(issueID, userID string, page, pageSize int) ([]IssueTimelineEventResponse, int64, error) {
	issue, err := s.issueRepo.FindByID(issueID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, 0, errs.ErrIssueNotFound
		}
		return nil, 0, errs.ErrInternal
	}
	if _, err := s.projectRepo.FindByID(issue.ProjectID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, 0, errs.ErrProjectNotFound
		}
		return nil, 0, errs.ErrNotOwner
	}

	events, total, err := s.timelineRepo.List(issueID, false, page, pageSize)
	if err != nil {
		return nil, 0, errs.ErrInternal
	}

	resp := make([]IssueTimelineEventResponse, 0, len(events))
	for _, event := range events {
		resp = append(resp, toIssueTimelineResponse(event))
	}
	return resp, total, nil
}

func (s *IssueService) SyncProjectIssues(projectID, userID string) (*IssueSyncResponse, error) {
	project, err := s.projectRepo.FindByID(projectID, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrProjectNotFound
		}
		return nil, errs.ErrInternal
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
		if project.GithubOwner == "" || project.GithubRepo == "" || len(project.GithubTokenEncrypted) == 0 {
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

			issue, syncErr := s.upsertIssue(project.ID, item)
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

func (s *IssueService) upsertIssue(projectID string, item *ghclient.Issue) (*model.Issue, error) {
	now := time.Now().UTC()
	issue := &model.Issue{
		ID:                uuid.NewString(),
		ProjectID:         projectID,
		GitHubIssueID:     item.GetID(),
		GitHubNodeID:      item.GetNodeID(),
		Number:            item.GetNumber(),
		State:             model.IssueState(item.GetState()),
		StateReason:       item.GetStateReason(),
		Title:             item.GetTitle(),
		Body:              item.GetBody(),
		BodyHTML:          item.GetBodyHTML(),
		HTMLURL:           item.GetHTMLURL(),
		AuthorLogin:       item.GetUser().GetLogin(),
		AuthorAvatarURL:   item.GetUser().GetAvatarURL(),
		AuthorAssociation: item.GetAuthorAssociation(),
		AssigneesJSON:     toJSONString(mapUsers(item.Assignees)),
		LabelsJSON:        toJSONString(mapLabels(item.Labels)),
		MilestoneJSON:     toJSONString(mapMilestone(item.Milestone)),
		ReactionsJSON:     toJSONString(mapReactions(item.Reactions)),
		CommentsCount:     item.GetComments(),
		Locked:            item.GetLocked(),
		ActiveLockReason:  item.GetActiveLockReason(),
		SyncedAt:          now,
		RawJSON:           toJSONString(item),
	}

	createdAt := item.GetCreatedAt().UTC()
	updatedAt := item.GetUpdatedAt().UTC()
	issue.GitHubCreatedAt = createdAt
	issue.GitHubUpdatedAt = updatedAt
	if closedAt := item.GetClosedAt(); !closedAt.IsZero() {
		value := closedAt.UTC()
		issue.ClosedAt = &value
	}

	if err := s.issueRepo.Upsert(issue); err != nil {
		return nil, errs.ErrInternal
	}

	stored, err := s.issueRepo.FindByProjectAndGitHubID(projectID, item.GetID())
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

func toIssueResponse(issue model.Issue) IssueResponse {
	assignees := parseJSON[[]issueUserPayload](issue.AssigneesJSON)
	labels := parseJSON[[]issueLabelPayload](issue.LabelsJSON)
	milestone := parseJSON[*issueMilestonePayload](issue.MilestoneJSON)
	reactions := parseJSON[IssueReactionSummaryResponse](issue.ReactionsJSON)

	resp := IssueResponse{
		ID:                issue.ID,
		ProjectID:         issue.ProjectID,
		GitHubIssueID:     issue.GitHubIssueID,
		GitHubNodeID:      issue.GitHubNodeID,
		Number:            issue.Number,
		State:             issue.State,
		StateReason:       issue.StateReason,
		Title:             issue.Title,
		Body:              issue.Body,
		BodyHTML:          issue.BodyHTML,
		HTMLURL:           issue.HTMLURL,
		Author:            IssueActorResponse{Login: issue.AuthorLogin, AvatarURL: issue.AuthorAvatarURL},
		AuthorAssociation: issue.AuthorAssociation,
		Assignees:         make([]IssueActorResponse, 0, len(assignees)),
		Labels:            make([]IssueLabelResponse, 0, len(labels)),
		Reactions:         reactions,
		CommentsCount:     issue.CommentsCount,
		Locked:            issue.Locked,
		ActiveLockReason:  issue.ActiveLockReason,
		CreatedAt:         formatTime(issue.GitHubCreatedAt),
		UpdatedAt:         formatTime(issue.GitHubUpdatedAt),
		SyncedAt:          formatTime(issue.SyncedAt),
	}
	for _, assignee := range assignees {
		resp.Assignees = append(resp.Assignees, IssueActorResponse{Login: assignee.Login, AvatarURL: assignee.AvatarURL})
	}
	for _, label := range labels {
		resp.Labels = append(resp.Labels, IssueLabelResponse{Name: label.Name, Color: label.Color, Description: label.Description})
	}
	if milestone != nil {
		resp.Milestone = &IssueMilestoneResponse{
			Number:      milestone.Number,
			Title:       milestone.Title,
			State:       milestone.State,
			Description: milestone.Description,
		}
	}
	if issue.ClosedAt != nil {
		value := formatTime(issue.ClosedAt.UTC())
		resp.ClosedAt = &value
	}
	return resp
}

func toIssueSyncStateResponse(state *model.IssueSyncState) *IssueSyncStateResponse {
	if state == nil {
		return &IssueSyncStateResponse{Status: model.IssueSyncStatusIdle}
	}

	resp := &IssueSyncStateResponse{
		Status:    state.Status,
		LastError: state.LastError,
	}
	if state.LastIssueUpdatedAt != nil {
		value := formatTime(state.LastIssueUpdatedAt.UTC())
		resp.LastIssueUpdatedAt = &value
	}
	if state.LastSyncedAt != nil {
		value := formatTime(state.LastSyncedAt.UTC())
		resp.LastSyncedAt = &value
	}
	if state.LastSuccessfulSyncAt != nil {
		value := formatTime(state.LastSuccessfulSyncAt.UTC())
		resp.LastSuccessfulSyncAt = &value
	}
	return resp
}

func toIssueCommentResponse(comment model.IssueComment) IssueCommentResponse {
	reactions := parseJSON[IssueReactionSummaryResponse](comment.ReactionsJSON)
	return IssueCommentResponse{
		ID:                comment.ID,
		IssueID:           comment.IssueID,
		GitHubCommentID:   comment.GitHubCommentID,
		GitHubNodeID:      comment.GitHubNodeID,
		Body:              comment.Body,
		BodyHTML:          comment.BodyHTML,
		HTMLURL:           comment.HTMLURL,
		Author:            IssueActorResponse{Login: comment.AuthorLogin, AvatarURL: comment.AuthorAvatarURL},
		AuthorAssociation: comment.AuthorAssociation,
		Reactions:         reactions,
		CreatedAt:         formatTime(comment.GitHubCreatedAt),
		UpdatedAt:         formatTime(comment.GitHubUpdatedAt),
	}
}

func toIssueTimelineResponse(event model.IssueTimelineEvent) IssueTimelineEventResponse {
	return IssueTimelineEventResponse{
		ID:            event.ID,
		IssueID:       event.IssueID,
		EventKey:      event.EventKey,
		EventType:     event.EventType,
		GitHubEventID: event.GitHubEventID,
		Actor:         IssueActorResponse{Login: event.ActorLogin, AvatarURL: event.ActorAvatarURL},
		Body:          event.Body,
		Summary:       event.Summary,
		Payload:       parseJSON[map[string]any](event.PayloadJSON),
		CreatedAt:     formatTime(event.GitHubCreatedAt),
	}
}

func mapUsers(users []*gh.User) []issueUserPayload {
	out := make([]issueUserPayload, 0, len(users))
	for _, user := range users {
		if user == nil || user.GetLogin() == "" {
			continue
		}
		out = append(out, issueUserPayload{
			Login:     user.GetLogin(),
			AvatarURL: user.GetAvatarURL(),
		})
	}
	return out
}

func mapLabels(labels []*gh.Label) []issueLabelPayload {
	out := make([]issueLabelPayload, 0, len(labels))
	for _, label := range labels {
		if label == nil || label.GetName() == "" {
			continue
		}
		out = append(out, issueLabelPayload{
			Name:        label.GetName(),
			Color:       label.GetColor(),
			Description: label.GetDescription(),
		})
	}
	return out
}

func mapMilestone(m *gh.Milestone) *issueMilestonePayload {
	if m == nil || m.GetTitle() == "" {
		return nil
	}
	return &issueMilestonePayload{
		Number:      m.GetNumber(),
		Title:       m.GetTitle(),
		State:       m.GetState(),
		Description: m.GetDescription(),
	}
}

func mapReactions(r *gh.Reactions) IssueReactionSummaryResponse {
	if r == nil {
		return IssueReactionSummaryResponse{}
	}
	return IssueReactionSummaryResponse{
		TotalCount: r.GetTotalCount(),
		PlusOne:    r.GetPlusOne(),
		MinusOne:   r.GetMinusOne(),
		Laugh:      r.GetLaugh(),
		Hooray:     r.GetHooray(),
		Confused:   r.GetConfused(),
		Heart:      r.GetHeart(),
		Rocket:     r.GetRocket(),
		Eyes:       r.GetEyes(),
	}
}

func summarizeTimeline(item *ghclient.TimelineEvent) string {
	eventType := item.GetEvent()
	switch eventType {
	case "labeled":
		return fmt.Sprintf("添加了标签 %s", item.GetLabel().GetName())
	case "unlabeled":
		return fmt.Sprintf("移除了标签 %s", item.GetLabel().GetName())
	case "assigned":
		return fmt.Sprintf("指派给 %s", item.GetAssignee().GetLogin())
	case "unassigned":
		return fmt.Sprintf("取消指派 %s", item.GetAssignee().GetLogin())
	case "milestoned":
		return fmt.Sprintf("加入里程碑 %s", item.GetMilestone().GetTitle())
	case "demilestoned":
		return fmt.Sprintf("移出里程碑 %s", item.GetMilestone().GetTitle())
	case "renamed":
		return fmt.Sprintf("标题从 %s 改为 %s", item.GetRename().GetFrom(), item.GetRename().GetTo())
	case "closed":
		return "关闭了问题"
	case "reopened":
		return "重新打开了问题"
	case "locked":
		return "锁定了讨论"
	case "unlocked":
		return "解锁了讨论"
	case "cross-referenced":
		source := item.GetSource()
		if source != nil && source.Issue != nil {
			return fmt.Sprintf("被 #%d 交叉引用", source.Issue.GetNumber())
		}
		return "发生了交叉引用"
	case "referenced":
		if item.GetCommitID() != "" {
			return fmt.Sprintf("被提交 %s 引用", shortSHA(item.GetCommitID()))
		}
		return "被提交引用"
	case "commented":
		return "添加了评论"
	case "subscribed":
		return "订阅了此问题"
	case "unsubscribed":
		return "取消订阅此问题"
	case "added_type", "issue_type_added":
		if item.GetIssueType() != nil {
			return fmt.Sprintf("添加了问题类型 %s", item.GetIssueType().GetName())
		}
		return "添加了问题类型"
	case "removed_type", "issue_type_removed":
		if item.GetIssueType() != nil {
			return fmt.Sprintf("移除了问题类型 %s", item.GetIssueType().GetName())
		}
		return "移除了问题类型"
	default:
		if eventType == "" {
			return "发生了更新"
		}
		return eventType
	}
}

func buildTimelineEventKey(item *ghclient.TimelineEvent) string {
	if item.GetID() != 0 {
		return fmt.Sprintf("gh:%d", item.GetID())
	}

	parts := []string{
		item.GetEvent(),
		formatTime(item.GetCreatedAt().UTC()),
		firstNonEmpty(item.GetActor().GetLogin(), item.GetUser().GetLogin()),
		item.GetLabel().GetName(),
		item.GetMilestone().GetTitle(),
		item.GetCommitID(),
		item.GetBody(),
	}
	return "fallback:" + strings.Join(parts, "|")
}

func toJSONString(v any) string {
	if v == nil {
		return ""
	}
	data, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(data)
}

func parseJSON[T any](raw string) T {
	var target T
	if strings.TrimSpace(raw) == "" {
		return target
	}
	_ = json.Unmarshal([]byte(raw), &target)
	return target
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func shortSHA(sha string) string {
	if len(sha) <= 7 {
		return sha
	}
	return sha[:7]
}

func parseIssueNumberQuery(query string) (int, bool) {
	num, err := strconv.Atoi(strings.TrimSpace(query))
	return num, err == nil
}

func sortedKeys(values map[string]struct{}) []string {
	items := make([]string, 0, len(values))
	for value := range values {
		items = append(items, value)
	}
	sort.Strings(items)
	return items
}

func sortIssues(items []model.Issue, sortKey string) {
	switch sortKey {
	case "updated_asc":
		sort.Slice(items, func(i, j int) bool {
			if items[i].GitHubUpdatedAt.Equal(items[j].GitHubUpdatedAt) {
				return items[i].Number < items[j].Number
			}
			return items[i].GitHubUpdatedAt.Before(items[j].GitHubUpdatedAt)
		})
	case "created_desc":
		sort.Slice(items, func(i, j int) bool {
			if items[i].GitHubCreatedAt.Equal(items[j].GitHubCreatedAt) {
				return items[i].Number > items[j].Number
			}
			return items[i].GitHubCreatedAt.After(items[j].GitHubCreatedAt)
		})
	case "created_asc":
		sort.Slice(items, func(i, j int) bool {
			if items[i].GitHubCreatedAt.Equal(items[j].GitHubCreatedAt) {
				return items[i].Number < items[j].Number
			}
			return items[i].GitHubCreatedAt.Before(items[j].GitHubCreatedAt)
		})
	case "comments_desc":
		sort.Slice(items, func(i, j int) bool {
			if items[i].CommentsCount == items[j].CommentsCount {
				return items[i].GitHubUpdatedAt.After(items[j].GitHubUpdatedAt)
			}
			return items[i].CommentsCount > items[j].CommentsCount
		})
	case "comments_asc":
		sort.Slice(items, func(i, j int) bool {
			if items[i].CommentsCount == items[j].CommentsCount {
				return items[i].GitHubUpdatedAt.Before(items[j].GitHubUpdatedAt)
			}
			return items[i].CommentsCount < items[j].CommentsCount
		})
	default:
		sort.Slice(items, func(i, j int) bool {
			if items[i].GitHubUpdatedAt.Equal(items[j].GitHubUpdatedAt) {
				return items[i].Number > items[j].Number
			}
			return items[i].GitHubUpdatedAt.After(items[j].GitHubUpdatedAt)
		})
	}
}

func matchesIssueFilters(issue model.Issue, filters IssueListFilters) bool {
	if filters.State != "" && string(issue.State) != filters.State {
		return false
	}

	query := strings.TrimSpace(filters.Query)
	if query != "" {
		queryLower := strings.ToLower(query)
		matches := strings.Contains(strings.ToLower(issue.Title), queryLower) ||
			strings.Contains(strings.ToLower(issue.Body), queryLower) ||
			strings.Contains(strings.ToLower(issue.AuthorLogin), queryLower)
		if num, ok := parseIssueNumberQuery(query); ok && issue.Number == num {
			matches = true
		}
		if !matches {
			return false
		}
	}

	if filters.Label != "" {
		matched := false
		for _, label := range parseJSON[[]issueLabelPayload](issue.LabelsJSON) {
			if strings.EqualFold(label.Name, filters.Label) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	if filters.Assignee != "" {
		matched := false
		for _, assignee := range parseJSON[[]issueUserPayload](issue.AssigneesJSON) {
			if strings.EqualFold(assignee.Login, filters.Assignee) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	if filters.Milestone != "" {
		milestone := parseJSON[*issueMilestonePayload](issue.MilestoneJSON)
		if milestone == nil || !strings.EqualFold(milestone.Title, filters.Milestone) {
			return false
		}
	}

	return true
}
