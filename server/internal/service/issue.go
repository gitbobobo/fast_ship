package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	neturl "net/url"
	"path/filepath"
	"regexp"
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
	"github.com/godbobo/fast_ship/server/internal/pkg/githubmedia"
	"github.com/godbobo/fast_ship/server/internal/pkg/storage"
	"github.com/godbobo/fast_ship/server/internal/repository"
	gh "github.com/google/go-github/v62/github"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

var issueAssetContentPattern = regexp.MustCompile(`(?:https?://[^/\s"')]+)?/api/issues/assets/([0-9a-fA-F-]+)/content(?:\?[^)\s"']*)?`)

const (
	issueAssetSniffBytes      = 512
	issueAssetPendingTTL      = 24 * time.Hour
	batchCloseDoneMaxIssues   = 200
	batchCloseDoneMaxFailures = 50
)

type gitHubIssueClient interface {
	ValidateRepository(ctx context.Context) error
	ListIssues(ctx context.Context, state string, since *time.Time, page, perPage int) ([]*ghclient.Issue, *gh.Response, error)
	ListRepositoryLabels(ctx context.Context, page, perPage int) ([]*gh.Label, *gh.Response, error)
	ListIssueComments(ctx context.Context, issueNumber, page, perPage int) ([]*ghclient.IssueComment, *gh.Response, error)
	ListIssueTimeline(ctx context.Context, issueNumber, page, perPage int) ([]*ghclient.TimelineEvent, *gh.Response, error)
	CreateIssueComment(ctx context.Context, issueNumber int, body string) (*ghclient.IssueComment, error)
	UpdateIssue(ctx context.Context, issueNumber int, req ghclient.UpdateIssueRequest) (*ghclient.Issue, error)
	CreateIssue(ctx context.Context, title, body string) (*ghclient.Issue, error)
}

type gitHubIssueClientFactory func(token, owner, repo string) gitHubIssueClient

type IssueService struct {
	issueRepo           *repository.IssueRepository
	gitHubMetaRepo      *repository.IssueGitHubMetaRepository
	commentRepo         *repository.IssueCommentRepository
	timelineRepo        *repository.IssueTimelineRepository
	internalMetaRepo    *repository.IssueInternalMetaRepository
	shipHookRepo        *repository.IssueShipHookRepository
	checklistRepo       *repository.IssueChecklistRepository
	syncStateRepo       *repository.IssueSyncStateRepository
	assetRepo           *repository.IssueAssetRepository
	draftAssetRepo      *repository.IssueDraftAssetRepository
	projectRepo         *repository.ProjectRepository
	userRepo            *repository.UserRepository
	githubRepoLabelRepo *repository.GitHubRepoLabelRepository
	storage             storage.Storage
	cfg                 *config.Config
	logger              *zap.Logger
	newClient           gitHubIssueClientFactory
	syncMu              sync.Mutex
	syncingProjectID    map[string]struct{}
}

type IssueListFilters struct {
	State     string
	Query     string
	Label     string
	Source    string
	Assignee  string
	Milestone string
	Workflow  string
	Sort      string
}

type CreateInternalIssueRequest struct {
	Title          string                    `json:"title"`
	Body           string                    `json:"body"`
	WorkflowStatus model.IssueWorkflowStatus `json:"workflow_status"`
}

type UpdateInternalIssueRequest struct {
	Title       *string           `json:"title"`
	Body        *string           `json:"body"`
	State       *model.IssueState `json:"state"`
	StateReason *string           `json:"state_reason"`
	Labels      *[]string         `json:"labels"`
}

type BatchCloseDoneIssueFailure struct {
	ID        string `json:"id"`
	Reference string `json:"reference,omitempty"`
	Error     string `json:"error"`
}

type BatchCloseDoneIssuesResponse struct {
	Total     int64                        `json:"total"`
	Succeeded int                          `json:"succeeded"`
	Failed    int                          `json:"failed"`
	Failures  []BatchCloseDoneIssueFailure `json:"failures"`
	ElapsedMs int64                        `json:"elapsed_ms"`
}

type CreateInternalIssueCommentRequest struct {
	Body string `json:"body"`
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

type IssueGitHubResponse struct {
	GitHubIssueID     int64                        `json:"github_issue_id"`
	GitHubNodeID      string                       `json:"github_node_id"`
	Number            int                          `json:"number"`
	HTMLURL           string                       `json:"html_url"`
	AuthorAssociation string                       `json:"author_association"`
	Assignees         []IssueActorResponse         `json:"assignees"`
	Labels            []IssueLabelResponse         `json:"labels"`
	Milestone         *IssueMilestoneResponse      `json:"milestone,omitempty"`
	Reactions         IssueReactionSummaryResponse `json:"reactions"`
	CommentsCount     int                          `json:"comments_count"`
	Locked            bool                         `json:"locked"`
	ActiveLockReason  string                       `json:"active_lock_reason"`
	SyncedAt          string                       `json:"synced_at"`
}

type IssueResponse struct {
	ID             string                     `json:"id"`
	ProjectID      string                     `json:"project_id"`
	Source         model.IssueSource          `json:"source"`
	SequenceNumber int                        `json:"sequence_number"`
	Reference      string                     `json:"reference"`
	State          model.IssueState           `json:"state"`
	StateReason    string                     `json:"state_reason"`
	Title          string                     `json:"title"`
	Body           string                     `json:"body"`
	BodyHTML       string                     `json:"body_html"`
	Author         IssueActorResponse         `json:"author"`
	CreatedAt      string                     `json:"created_at"`
	UpdatedAt      string                     `json:"updated_at"`
	ClosedAt       *string                    `json:"closed_at"`
	InternalMeta   *IssueInternalMetaResponse `json:"internal_meta,omitempty"`
	ShipHook       *IssueShipHookResponse     `json:"ship_hook,omitempty"`
	GitHub         *IssueGitHubResponse       `json:"github,omitempty"`
}

type IssueCommentResponse struct {
	ID                string                       `json:"id"`
	IssueID           string                       `json:"issue_id"`
	Source            model.IssueSource            `json:"source"`
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

type IssueInternalMetaResponse struct {
	WorkflowStatus     model.IssueWorkflowStatus    `json:"workflow_status"`
	ProgressPercent    *int                         `json:"progress_percent"`
	ChecklistTotal     int                          `json:"checklist_total"`
	ChecklistDone      int                          `json:"checklist_done"`
	StartedAt          *string                      `json:"started_at,omitempty"`
	CompletedAt        *string                      `json:"completed_at,omitempty"`
	ChecklistUpdatedAt *string                      `json:"checklist_updated_at,omitempty"`
	UpdatedAt          *string                      `json:"updated_at,omitempty"`
	Checklist          []IssueChecklistItemResponse `json:"checklist,omitempty"`
	Labels             []IssueLabelResponse         `json:"labels,omitempty"`
}

type IssueChecklistItemResponse struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	IsCompleted bool   `json:"is_completed"`
	SortOrder   int    `json:"sort_order"`
}

type ReplaceIssueChecklistRequest struct {
	Items []IssueChecklistItemInput `json:"items"`
}

type IssueChecklistItemInput struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	IsCompleted bool   `json:"is_completed"`
}

type IssueAssetResponse struct {
	ID         string `json:"id"`
	IssueID    string `json:"issue_id"`
	FileName   string `json:"file_name"`
	MimeType   string `json:"mime_type"`
	FileSize   int64  `json:"file_size"`
	ContentURL string `json:"content_url"`
	Markdown   string `json:"markdown"`
	CreatedAt  string `json:"created_at"`
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
	gitHubMetaRepo *repository.IssueGitHubMetaRepository,
	commentRepo *repository.IssueCommentRepository,
	timelineRepo *repository.IssueTimelineRepository,
	internalMetaRepo *repository.IssueInternalMetaRepository,
	shipHookRepo *repository.IssueShipHookRepository,
	checklistRepo *repository.IssueChecklistRepository,
	syncStateRepo *repository.IssueSyncStateRepository,
	assetRepo *repository.IssueAssetRepository,
	draftAssetRepo *repository.IssueDraftAssetRepository,
	projectRepo *repository.ProjectRepository,
	userRepo *repository.UserRepository,
	githubRepoLabelRepo *repository.GitHubRepoLabelRepository,
	storage storage.Storage,
	cfg *config.Config,
	logger *zap.Logger,
) *IssueService {
	return &IssueService{
		issueRepo:           issueRepo,
		gitHubMetaRepo:      gitHubMetaRepo,
		commentRepo:         commentRepo,
		timelineRepo:        timelineRepo,
		internalMetaRepo:    internalMetaRepo,
		shipHookRepo:        shipHookRepo,
		checklistRepo:       checklistRepo,
		syncStateRepo:       syncStateRepo,
		assetRepo:           assetRepo,
		draftAssetRepo:      draftAssetRepo,
		projectRepo:         projectRepo,
		userRepo:            userRepo,
		githubRepoLabelRepo: githubRepoLabelRepo,
		storage:             storage,
		cfg:                 cfg,
		logger:              logger,
		newClient: func(token, owner, repo string) gitHubIssueClient {
			return ghclient.NewClient(token, owner, repo)
		},
		syncingProjectID: make(map[string]struct{}),
	}
}

func (s *IssueService) CleanupExpiredPendingIssueAssets() error {
	cutoff := time.Now().UTC().Add(-issueAssetPendingTTL)
	assets, err := s.assetRepo.ListPendingCreatedBefore(cutoff)
	if err != nil {
		return err
	}
	if len(assets) == 0 {
		return nil
	}

	idsByIssue := make(map[string][]string)
	for _, asset := range assets {
		idsByIssue[asset.IssueID] = append(idsByIssue[asset.IssueID], asset.ID)
	}

	for issueID, ids := range idsByIssue {
		if err := s.deleteIssueAssets(issueID, ids); err != nil {
			return err
		}
	}

	draftAssets, err := s.draftAssetRepo.ListCreatedBefore(cutoff)
	if err != nil {
		return err
	}
	if len(draftAssets) == 0 {
		return nil
	}

	idsByProject := make(map[string][]string)
	for _, asset := range draftAssets {
		idsByProject[asset.ProjectID] = append(idsByProject[asset.ProjectID], asset.ID)
	}

	for projectID, ids := range idsByProject {
		if err := s.deleteDraftIssueAssets(projectID, ids); err != nil {
			return err
		}
	}
	return nil
}

func (s *IssueService) CreateInternalIssue(projectID, userID string, req CreateInternalIssueRequest) (*IssueResponse, error) {
	title := strings.TrimSpace(req.Title)
	if title == "" || !model.IsValidIssueWorkflowStatus(req.WorkflowStatus) {
		return nil, errs.ErrInvalidParams
	}

	if _, err := s.projectRepo.FindByID(projectID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrProjectNotFound
		}
		return nil, errs.ErrInternal
	}

	user, err := s.userRepo.FindByID(userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrUserNotFound
		}
		return nil, errs.ErrInternal
	}

	sequenceNumber, err := s.issueRepo.NextSequenceNumber(projectID)
	if err != nil {
		return nil, errs.ErrInternal
	}

	now := time.Now().UTC()
	issue := &model.Issue{
		ID:              uuid.NewString(),
		ProjectID:       projectID,
		Source:          model.IssueSourceInternal,
		SequenceNumber:  sequenceNumber,
		State:           model.IssueStateOpen,
		Title:           title,
		Body:            req.Body,
		AuthorUserID:    user.ID,
		AuthorLogin:     user.Username,
		AuthorAvatarURL: "",
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	var meta *model.IssueInternalMeta
	if req.WorkflowStatus != "" {
		meta = buildInternalIssueMeta(issue.ID, userID, req.WorkflowStatus, now)
	}

	if err := s.issueRepo.Transaction(func(tx *gorm.DB) error {
		if err := s.issueRepo.CreateTx(tx, issue); err != nil {
			return err
		}
		if err := s.attachDraftAssetsToIssueTx(tx, projectID, issue.ID, req.Body); err != nil {
			return err
		}
		if meta != nil {
			if err := s.internalMetaRepo.UpsertTx(tx, meta); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		var appErr *errs.AppError
		if errors.As(err, &appErr) {
			return nil, appErr
		}
		return nil, errs.ErrInternal
	}

	stored, err := s.issueRepo.FindByID(issue.ID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	meta, err = s.loadInternalMeta(stored.ID)
	if err != nil {
		return nil, err
	}

	resp := s.toIssueResponse(*stored, meta, nil, nil, nil)
	return &resp, nil
}

func (s *IssueService) CreateGitHubIssue(projectID, userID string, req CreateInternalIssueRequest) (*IssueResponse, error) {
	title := strings.TrimSpace(req.Title)
	if title == "" {
		return nil, errs.ErrInvalidParams
	}

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

	client := s.newClient(string(tokenBytes), project.GithubOwner, project.GithubRepo)

	ctx := context.Background()
	if err := s.validateIssueAssetReferences(projectID, "", req.Body); err != nil {
		return nil, err
	}

	createdIssue, err := client.CreateIssue(ctx, title, req.Body)
	if err != nil {
		return nil, errs.New(errs.ErrGitHubAPI.Code, fmt.Sprintf("创建 GitHub Issue 失败: %v", err))
	}

	stored, err := s.upsertGitHubIssue(projectID, createdIssue)
	if err != nil {
		return nil, err
	}
	var assetPathsToDelete []string
	if err := s.issueRepo.Transaction(func(tx *gorm.DB) error {
		var err error
		assetPathsToDelete, err = s.syncIssueAssetsTx(tx, projectID, stored.ID, req.Body)
		return err
	}); err != nil {
		return nil, mapIssueAssetReferenceError(err)
	}
	s.deleteIssueAssetFiles(assetPathsToDelete)

	if _, err := s.syncTimeline(ctx, client, stored, createdIssue.GetNumber()); err != nil {
		s.logger.Warn("sync timeline after creating github issue failed", zap.String("issue_id", stored.ID), zap.Error(err))
	}

	meta, err := s.loadInternalMeta(stored.ID)
	if err != nil {
		return nil, err
	}

	resp := s.toIssueResponse(*stored, meta, nil, nil, nil)
	return &resp, nil
}

func (s *IssueService) UpdateInternalIssue(issueID, userID string, req UpdateInternalIssueRequest) (*IssueResponse, error) {
	if req.Title == nil && req.Body == nil && req.State == nil && req.StateReason == nil && req.Labels == nil {
		return nil, errs.ErrInvalidParams
	}
	if req.State == nil && req.StateReason != nil {
		return nil, errs.ErrInvalidParams
	}

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
	if issue.Source == model.IssueSourceGitHub {
		if req.Title == nil && req.Body == nil && req.State == nil && req.Labels == nil {
			return nil, errs.ErrIssueReadOnly
		}
		if issue.GitHubMeta == nil {
			return nil, errs.ErrIssueReadOnly
		}
		if req.State != nil && !isValidIssueState(*req.State) {
			return nil, errs.ErrInvalidParams
		}

		stateReason := ""
		if req.State != nil {
			var appErr *errs.AppError
			stateReason, appErr = normalizeIssueStateReason(req.State, req.StateReason)
			if appErr != nil {
				return nil, appErr
			}
		}

		var labelsToUpdate *[]string
		if req.Labels != nil {
			normalizedLabels, appErr := normalizeGitHubLabels(*req.Labels)
			if appErr != nil {
				return nil, appErr
			}
			labelsToUpdate = &normalizedLabels
		}

		project, err := s.projectRepo.FindByID(issue.ProjectID, userID)
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

		client := s.newClient(string(tokenBytes), project.GithubOwner, project.GithubRepo)
		updateReq := ghclient.UpdateIssueRequest{}
		if req.Title != nil {
			title := strings.TrimSpace(*req.Title)
			if title == "" {
				return nil, errs.ErrInvalidParams
			}
			updateReq.Title = &title
		}
		if req.Body != nil {
			if err := s.validateIssueAssetReferences(project.ID, issue.ID, *req.Body); err != nil {
				return nil, err
			}
			updateReq.Body = req.Body
		}
		if req.State != nil {
			state := string(*req.State)
			updateReq.State = &state
			if stateReason != "" {
				updateReq.StateReason = &stateReason
			}
		}
		if labelsToUpdate != nil {
			updateReq.Labels = labelsToUpdate
		}
		updatedIssue, err := client.UpdateIssue(context.Background(), issue.GitHubMeta.Number, updateReq)
		if err != nil {
			return nil, errs.New(errs.ErrGitHubAPI.Code, fmt.Sprintf("更新 GitHub Issue 失败: %v", err))
		}

		stored, err := s.upsertGitHubIssue(project.ID, updatedIssue)
		if err != nil {
			return nil, err
		}
		if req.Body != nil {
			var assetPathsToDelete []string
			if err := s.issueRepo.Transaction(func(tx *gorm.DB) error {
				var err error
				assetPathsToDelete, err = s.syncIssueAssetsTx(tx, project.ID, stored.ID, *req.Body)
				return err
			}); err != nil {
				return nil, mapIssueAssetReferenceError(err)
			}
			s.deleteIssueAssetFiles(assetPathsToDelete)
		}
		if _, err := s.syncTimeline(context.Background(), client, stored, issue.GitHubMeta.Number); err != nil {
			return nil, err
		}
		meta, err := s.loadInternalMeta(stored.ID)
		if err != nil {
			return nil, err
		}
		resp := s.toIssueResponse(*stored, meta, nil, nil, nil)
		return &resp, nil
	}
	if issue.Source != model.IssueSourceInternal {
		return nil, errs.ErrIssueReadOnly
	}
	var internalMeta *model.IssueInternalMeta
	if req.Labels != nil {
		project, err := s.projectRepo.FindByID(issue.ProjectID, userID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errs.ErrProjectNotFound
			}
			return nil, errs.ErrInternal
		}
		repoLabels, err := s.GetRepositoryLabels(project.ID, userID)
		if err != nil {
			return nil, err
		}
		resolvedLabels, appErr := resolveInternalLabels(*req.Labels, repoLabels)
		if appErr != nil {
			return nil, appErr
		}
		meta, err := s.loadInternalMeta(issue.ID)
		if err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		if meta == nil {
			meta = &model.IssueInternalMeta{
				IssueID:   issue.ID,
				CreatedAt: now,
			}
		}
		meta.LabelsJSON = toJSONString(resolvedLabels)
		meta.UpdatedByUserID = userID
		meta.UpdatedAt = now
		internalMeta = meta
	}

	if req.Title != nil {
		title := strings.TrimSpace(*req.Title)
		if title == "" {
			return nil, errs.ErrInvalidParams
		}
		issue.Title = title
	}
	if req.Body != nil {
		issue.Body = *req.Body
		issue.BodyHTML = ""
	}
	if req.State != nil {
		if !isValidIssueState(*req.State) {
			return nil, errs.ErrInvalidParams
		}
		stateReason, appErr := normalizeIssueStateReason(req.State, req.StateReason)
		if appErr != nil {
			return nil, appErr
		}
		issue.State = *req.State
		issue.StateReason = stateReason
		if *req.State == model.IssueStateClosed {
			now := time.Now().UTC()
			issue.ClosedAt = &now
		} else {
			issue.ClosedAt = nil
		}
	}
	issue.UpdatedAt = time.Now().UTC()

	if err := s.issueRepo.Transaction(func(tx *gorm.DB) error {
		if err := s.issueRepo.SaveTx(tx, issue); err != nil {
			return err
		}
		if req.Labels != nil && internalMeta != nil {
			if err := s.internalMetaRepo.UpsertTx(tx, internalMeta); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, errs.ErrInternal
	}
	if req.Body != nil {
		if err := s.reconcileIssueAssets(issue.ID, *req.Body); err != nil {
			s.logger.Warn("reconcile issue assets failed", zap.String("issue_id", issue.ID), zap.Error(err))
		}
	}

	meta, err := s.loadInternalMeta(issue.ID)
	if err != nil {
		return nil, err
	}
	resp := s.toIssueResponse(*issue, meta, nil, nil, nil)
	return &resp, nil
}

func (s *IssueService) UploadInternalIssueAsset(issueID, userID, fileName string, fileSize int64, reader io.Reader, actor string) (*IssueAssetResponse, error) {
	_ = fileSize

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
	if issue.Source != model.IssueSourceInternal {
		return nil, errs.ErrIssueReadOnly
	}

	readFrom := reader
	if s.cfg.Upload.MaxFileSize > 0 {
		readFrom = io.LimitReader(reader, s.cfg.Upload.MaxFileSize+1)
	}

	head := make([]byte, issueAssetSniffBytes)
	headSize, err := io.ReadFull(readFrom, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, errs.ErrInternal
	}
	head = head[:headSize]
	if len(head) == 0 {
		return nil, errs.ErrInvalidParams
	}

	mimeType := http.DetectContentType(head)
	if !strings.HasPrefix(mimeType, "image/") {
		return nil, errs.ErrInvalidParams
	}

	assetID := uuid.NewString()
	storagePath := buildIssueAssetStoragePath(issue.ProjectID, issue.ID, assetID, fileName, mimeType)
	uploadReader := io.MultiReader(bytes.NewReader(head), readFrom)
	countedReader := &countingReader{reader: uploadReader}
	if err := s.storage.Save(storagePath, countedReader); err != nil {
		_ = s.storage.Delete(storagePath)
		return nil, errs.ErrInternal
	}
	if s.cfg.Upload.MaxFileSize > 0 && countedReader.n > s.cfg.Upload.MaxFileSize {
		_ = s.storage.Delete(storagePath)
		return nil, errs.ErrInvalidParams
	}

	asset := &model.IssueAsset{
		ID:              assetID,
		IssueID:         issue.ID,
		FileName:        normalizeIssueAssetFileName(fileName, mimeType),
		FilePath:        storagePath,
		MimeType:        mimeType,
		FileSize:        countedReader.n,
		Status:          model.IssueAssetStatusPending,
		CreatedByUserID: userID,
		CreatedAt:       time.Now().UTC(),
	}

	if err := s.assetRepo.Create(asset); err != nil {
		_ = s.storage.Delete(storagePath)
		return nil, errs.ErrInternal
	}

	s.logger.Info("issue asset uploaded",
		zap.String("action", "upload_issue_asset"),
		zap.String("issue_id", issueID),
		zap.String("user_id", userID),
		zap.String("actor", actor),
	)
	resp := toIssueAssetResponse(*asset)
	return &resp, nil
}

func (s *IssueService) UploadDraftInternalIssueAsset(projectID, userID, fileName string, fileSize int64, reader io.Reader) (*IssueAssetResponse, error) {
	_ = fileSize

	if _, err := s.projectRepo.FindByID(projectID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrProjectNotFound
		}
		return nil, errs.ErrInternal
	}

	asset, err := s.storeDraftIssueAsset(projectID, userID, fileName, reader)
	if err != nil {
		return nil, err
	}

	resp := toDraftIssueAssetResponse(*asset)
	return &resp, nil
}

func (s *IssueService) GetIssueAssetContent(assetID, userID string) (io.ReadCloser, string, int64, error) {
	asset, err := s.assetRepo.FindByID(assetID)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", 0, errs.ErrInternal
		}
		draftAsset, draftErr := s.draftAssetRepo.FindByID(assetID)
		if draftErr != nil {
			if errors.Is(draftErr, gorm.ErrRecordNotFound) {
				return nil, "", 0, errs.ErrIssueAssetNotFound
			}
			return nil, "", 0, errs.ErrInternal
		}
		if _, err := s.projectRepo.FindByID(draftAsset.ProjectID, userID); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, "", 0, errs.ErrProjectNotFound
			}
			return nil, "", 0, errs.ErrNotOwner
		}
		reader, err := s.storage.Get(draftAsset.FilePath)
		if err != nil {
			return nil, "", 0, errs.ErrIssueAssetNotFound
		}
		return reader, draftAsset.MimeType, draftAsset.FileSize, nil
	}

	issue, err := s.issueRepo.FindByID(asset.IssueID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", 0, errs.ErrIssueNotFound
		}
		return nil, "", 0, errs.ErrInternal
	}
	if _, err := s.projectRepo.FindByID(issue.ProjectID, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", 0, errs.ErrProjectNotFound
		}
		return nil, "", 0, errs.ErrNotOwner
	}

	reader, err := s.storage.Get(asset.FilePath)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", 0, errs.ErrIssueAssetNotFound
		}
		return nil, "", 0, errs.ErrInternal
	}

	return reader, asset.MimeType, asset.FileSize, nil
}

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
	shipHooksByIssueID, err := s.shipHooksByIssueIDs(pageIssues)
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

	shipHook, err := s.loadShipHook(issue.ID)
	if err != nil {
		return nil, err
	}

	resp := s.toIssueResponse(*issue, meta, checklist, nil, shipHook)
	return &resp, nil
}

func (s *IssueService) ReplaceChecklist(issueID, userID string, req ReplaceIssueChecklistRequest, actor string) (*IssueInternalMetaResponse, error) {
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

	items, snapshot, err := buildChecklistSnapshot(issueID, userID, req.Items)
	if err != nil {
		return nil, errs.ErrInvalidParams
	}

	now := time.Now().UTC()
	meta, err := s.internalMetaRepo.Get(issue.ID)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrInternal
		}
		meta = &model.IssueInternalMeta{
			IssueID:   issue.ID,
			CreatedAt: now,
		}
	}

	meta.ProgressPercent = snapshot.ProgressPercent
	meta.ChecklistTotal = snapshot.Total
	meta.ChecklistDone = snapshot.Done
	meta.UpdatedByUserID = userID
	meta.UpdatedAt = now
	if len(items) == 0 {
		meta.ChecklistUpdatedAt = nil
	} else {
		value := now
		meta.ChecklistUpdatedAt = &value
	}

	if err := s.checklistRepo.Transaction(func(tx *gorm.DB) error {
		if err := s.checklistRepo.ReplaceForIssueTx(tx, issue.ID, items); err != nil {
			return err
		}
		if err := s.internalMetaRepo.UpsertTx(tx, meta); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return nil, errs.ErrInternal
	}

	s.logger.Info("issue checklist replaced",
		zap.String("action", "replace_checklist"),
		zap.String("issue_id", issueID),
		zap.String("user_id", userID),
		zap.String("actor", actor),
	)
	return s.toIssueInternalMetaResponse(issue.ProjectID, meta, items, nil), nil
}

func (s *IssueService) UpdateInternalMeta(issueID, userID string, workflowStatus model.IssueWorkflowStatus, actor string) (*IssueInternalMetaResponse, error) {
	if !model.IsValidIssueWorkflowStatus(workflowStatus) {
		return nil, errs.ErrInvalidParams
	}

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

	now := time.Now().UTC()
	meta, err := s.internalMetaRepo.Get(issue.ID)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrInternal
		}
		meta = &model.IssueInternalMeta{
			IssueID:   issue.ID,
			CreatedAt: now,
		}
	}

	meta.WorkflowStatus = workflowStatus
	meta.UpdatedByUserID = userID
	meta.UpdatedAt = now
	applyExplicitWorkflowStatus(meta, workflowStatus, now)

	// 同步刷新 issues 主表的 updated_at（仅此一列），使看板按 updated_at DESC 排序时能反映状态变更。
	if err := s.issueRepo.Transaction(func(tx *gorm.DB) error {
		if err := s.internalMetaRepo.UpsertTx(tx, meta); err != nil {
			return err
		}
		if err := s.issueRepo.TouchUpdatedAt(tx, issue.ID, now); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return nil, errs.ErrInternal
	}

	s.logger.Info("issue internal meta updated",
		zap.String("action", "update_internal_meta"),
		zap.String("issue_id", issueID),
		zap.String("user_id", userID),
		zap.String("actor", actor),
		zap.String("workflow_status", string(workflowStatus)),
	)
	return s.toIssueInternalMetaResponse(issue.ProjectID, meta, nil, nil), nil
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

func (s *IssueService) CreateInternalComment(issueID, userID string, req CreateInternalIssueCommentRequest, actor string) (*IssueCommentResponse, error) {
	body := strings.TrimSpace(req.Body)
	if body == "" {
		return nil, errs.ErrInvalidParams
	}

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
	if issue.Source == model.IssueSourceGitHub {
		if issue.GitHubMeta == nil {
			return nil, errs.ErrInternal
		}

		project, err := s.projectRepo.FindByID(issue.ProjectID, userID)
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

		client := s.newClient(string(tokenBytes), project.GithubOwner, project.GithubRepo)
		createdComment, err := client.CreateIssueComment(context.Background(), issue.GitHubMeta.Number, body)
		if err != nil {
			return nil, errs.New(errs.ErrGitHubAPI.Code, fmt.Sprintf("创建 GitHub Issue 评论失败: %v", err))
		}

		comment := buildGitHubIssueCommentModel(issue.ID, createdComment)
		if err := s.commentRepo.Upsert(comment); err != nil {
			return nil, errs.ErrInternal
		}

		now := time.Now().UTC()
		updatedAt := comment.GitHubUpdatedAt
		if updatedAt.IsZero() {
			updatedAt = now
		}
		issue.UpdatedAt = updatedAt
		if err := s.issueRepo.Save(issue); err != nil {
			return nil, errs.ErrInternal
		}

		meta := issue.GitHubMeta
		meta.CommentsCount++
		meta.SyncedAt = now
		if err := s.gitHubMetaRepo.Upsert(meta); err != nil {
			return nil, errs.ErrInternal
		}

		s.logger.Info("issue comment created",
			zap.String("action", "create_comment"),
			zap.String("issue_id", issueID),
			zap.String("user_id", userID),
			zap.String("actor", actor),
			zap.String("source", "github"),
		)
		resp := toIssueCommentResponse(*comment)
		return &resp, nil
	}
	if issue.Source != model.IssueSourceInternal {
		return nil, errs.ErrIssueReadOnly
	}

	user, err := s.userRepo.FindByID(userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrUserNotFound
		}
		return nil, errs.ErrInternal
	}

	commentID, err := s.commentRepo.NextSyntheticCommentID(issueID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	now := time.Now().UTC()
	comment := &model.IssueComment{
		ID:              uuid.NewString(),
		IssueID:         issueID,
		Source:          model.IssueSourceInternal,
		AuthorUserID:    userID,
		GitHubCommentID: commentID,
		Body:            req.Body,
		BodyHTML:        "",
		AuthorLogin:     user.Username,
		AuthorAvatarURL: "",
		GitHubCreatedAt: now,
		GitHubUpdatedAt: now,
	}

	if err := s.commentRepo.Create(comment); err != nil {
		return nil, errs.ErrInternal
	}

	issue.UpdatedAt = now
	if err := s.issueRepo.Save(issue); err != nil {
		return nil, errs.ErrInternal
	}

	s.logger.Info("issue comment created",
		zap.String("action", "create_comment"),
		zap.String("issue_id", issueID),
		zap.String("user_id", userID),
		zap.String("actor", actor),
		zap.String("source", "internal"),
	)
	resp := toIssueCommentResponse(*comment)
	return &resp, nil
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
	if issue.Source != model.IssueSourceGitHub || issue.GitHubMeta == nil {
		return []IssueTimelineEventResponse{}, 0, nil
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

func (s *IssueService) loadInternalMeta(issueID string) (*model.IssueInternalMeta, error) {
	meta, err := s.internalMetaRepo.Get(issueID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, errs.ErrInternal
	}
	return meta, nil
}

// InternalMetaWorkflowStatus 返回 Issue 当前内部工作流状态，无记录时返回 ""。
func (s *IssueService) InternalMetaWorkflowStatus(issueID string) (model.IssueWorkflowStatus, error) {
	meta, err := s.loadInternalMeta(issueID)
	if err != nil {
		return "", err
	}
	if meta == nil {
		return "", nil
	}
	return meta.WorkflowStatus, nil
}

func (s *IssueService) loadChecklist(issueID string) ([]model.IssueChecklistItem, error) {
	items, err := s.checklistRepo.ListByIssueID(issueID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	return items, nil
}

func (s *IssueService) toIssueResponse(issue model.Issue, meta *model.IssueInternalMeta, checklist []model.IssueChecklistItem, labelMap map[string]model.GitHubRepoLabel, shipHook *model.IssueShipHook) IssueResponse {
	resp := IssueResponse{
		ID:             issue.ID,
		ProjectID:      issue.ProjectID,
		Source:         issue.Source,
		SequenceNumber: issue.SequenceNumber,
		Reference:      buildIssueReference(issue),
		State:          issue.State,
		StateReason:    issue.StateReason,
		Title:          issue.Title,
		Body:           issue.Body,
		BodyHTML:       githubmedia.RewriteHTMLMediaSources(issue.BodyHTML),
		Author: IssueActorResponse{
			Login:     issue.AuthorLogin,
			AvatarURL: githubmedia.RewriteMediaURL(issue.AuthorAvatarURL),
		},
		CreatedAt:    formatTime(issue.CreatedAt),
		UpdatedAt:    formatTime(issue.UpdatedAt),
		InternalMeta: s.toIssueInternalMetaResponse(issue.ProjectID, meta, checklist, labelMap),
		ShipHook:     s.toIssueShipHookResponse(shipHook),
	}
	if issue.ClosedAt != nil {
		value := formatTime(issue.ClosedAt.UTC())
		resp.ClosedAt = &value
	}
	if issue.GitHubMeta != nil {
		resp.GitHub = s.toIssueGitHubResponse(issue.ProjectID, issue.GitHubMeta, labelMap)
	}
	return resp
}

func (s *IssueService) toIssueGitHubResponse(projectID string, meta *model.IssueGitHubMeta, labelMap map[string]model.GitHubRepoLabel) *IssueGitHubResponse {
	if meta == nil {
		return nil
	}

	assignees := parseJSON[[]issueUserPayload](meta.AssigneesJSON)
	labelNames := extractLabelNames(meta.LabelsJSON)
	milestone := parseJSON[*issueMilestonePayload](meta.MilestoneJSON)
	reactions := parseJSON[IssueReactionSummaryResponse](meta.ReactionsJSON)

	resp := &IssueGitHubResponse{
		GitHubIssueID:     meta.GitHubIssueID,
		GitHubNodeID:      meta.GitHubNodeID,
		Number:            meta.Number,
		HTMLURL:           meta.HTMLURL,
		AuthorAssociation: meta.AuthorAssociation,
		Assignees:         make([]IssueActorResponse, 0, len(assignees)),
		Labels:            s.resolveLabels(projectID, labelNames, labelMap),
		Reactions:         reactions,
		CommentsCount:     meta.CommentsCount,
		Locked:            meta.Locked,
		ActiveLockReason:  meta.ActiveLockReason,
		SyncedAt:          formatTime(meta.SyncedAt),
	}
	for _, assignee := range assignees {
		resp.Assignees = append(resp.Assignees, IssueActorResponse{
			Login:     assignee.Login,
			AvatarURL: githubmedia.RewriteMediaURL(assignee.AvatarURL),
		})
	}
	if milestone != nil {
		resp.Milestone = &IssueMilestoneResponse{
			Number:      milestone.Number,
			Title:       milestone.Title,
			State:       milestone.State,
			Description: milestone.Description,
		}
	}
	return resp
}

func (s *IssueService) toIssueInternalMetaResponse(projectID string, meta *model.IssueInternalMeta, checklist []model.IssueChecklistItem, labelMap map[string]model.GitHubRepoLabel) *IssueInternalMetaResponse {
	if meta == nil && len(checklist) == 0 {
		return nil
	}

	resp := &IssueInternalMetaResponse{}
	if meta != nil {
		resp.WorkflowStatus = meta.WorkflowStatus
		resp.ProgressPercent = meta.ProgressPercent
		resp.ChecklistTotal = meta.ChecklistTotal
		resp.ChecklistDone = meta.ChecklistDone
		if meta.StartedAt != nil {
			value := formatTime(meta.StartedAt.UTC())
			resp.StartedAt = &value
		}
		if meta.CompletedAt != nil {
			value := formatTime(meta.CompletedAt.UTC())
			resp.CompletedAt = &value
		}
		if meta.ChecklistUpdatedAt != nil {
			value := formatTime(meta.ChecklistUpdatedAt.UTC())
			resp.ChecklistUpdatedAt = &value
		}
		if !meta.UpdatedAt.IsZero() {
			value := formatTime(meta.UpdatedAt.UTC())
			resp.UpdatedAt = &value
		}
		resp.Labels = s.resolveLabels(projectID, extractLabelNames(meta.LabelsJSON), labelMap)
	}
	if len(checklist) > 0 {
		resp.Checklist = make([]IssueChecklistItemResponse, 0, len(checklist))
		for _, item := range checklist {
			resp.Checklist = append(resp.Checklist, IssueChecklistItemResponse{
				ID:          item.ID,
				Title:       item.Title,
				IsCompleted: item.IsCompleted,
				SortOrder:   item.SortOrder,
			})
		}
	}
	return resp
}

func toIssueAssetResponse(asset model.IssueAsset) IssueAssetResponse {
	contentURL := buildIssueAssetContentURL(asset.ID)
	return IssueAssetResponse{
		ID:         asset.ID,
		IssueID:    asset.IssueID,
		FileName:   asset.FileName,
		MimeType:   asset.MimeType,
		FileSize:   asset.FileSize,
		ContentURL: contentURL,
		Markdown:   fmt.Sprintf("![%s](%s)", issueAssetAltText(asset.FileName), contentURL),
		CreatedAt:  asset.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
}

func toDraftIssueAssetResponse(asset model.IssueDraftAsset) IssueAssetResponse {
	contentURL := buildIssueAssetContentURL(asset.ID)
	return IssueAssetResponse{
		ID:         asset.ID,
		IssueID:    "",
		FileName:   asset.FileName,
		MimeType:   asset.MimeType,
		FileSize:   asset.FileSize,
		ContentURL: contentURL,
		Markdown:   fmt.Sprintf("![%s](%s)", issueAssetAltText(asset.FileName), contentURL),
		CreatedAt:  asset.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
}

func buildIssueAssetContentURL(assetID string) string {
	return fmt.Sprintf("/api/issues/assets/%s/content", assetID)
}

func issueAssetAltText(fileName string) string {
	trimmed := strings.TrimSpace(strings.TrimSuffix(fileName, filepath.Ext(fileName)))
	if trimmed == "" {
		return "image"
	}
	return trimmed
}

func normalizeIssueAssetFileName(fileName, mimeType string) string {
	name := strings.TrimSpace(fileName)
	if name == "" {
		name = "image"
	}
	ext := strings.ToLower(filepath.Ext(name))
	if ext != "" {
		return name
	}
	if exts, err := mime.ExtensionsByType(mimeType); err == nil && len(exts) > 0 {
		return name + exts[0]
	}
	return name
}

func buildIssueAssetStoragePath(projectID, issueID, assetID, fileName, mimeType string) string {
	name := normalizeIssueAssetFileName(fileName, mimeType)
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		ext = ".bin"
	}
	return fmt.Sprintf("%s/issues/%s/assets/%s%s", projectID, issueID, assetID, ext)
}

func buildIssueDraftAssetStoragePath(projectID, assetID, fileName, mimeType string) string {
	name := normalizeIssueAssetFileName(fileName, mimeType)
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		ext = ".bin"
	}
	return fmt.Sprintf("%s/issues/drafts/assets/%s%s", projectID, assetID, ext)
}

func extractIssueAssetIDs(body string) map[string]struct{} {
	matches := issueAssetContentPattern.FindAllStringSubmatch(body, -1)
	result := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		id := strings.TrimSpace(match[1])
		if id == "" {
			continue
		}
		result[id] = struct{}{}
	}
	return result
}

func (s *IssueService) reconcileIssueAssets(issueID, body string) error {
	pathsToDelete, err := s.reconcileIssueAssetsTx(nil, issueID, body)
	if err != nil {
		return err
	}
	s.deleteIssueAssetFiles(pathsToDelete)
	return nil
}

func (s *IssueService) reconcileIssueAssetsTx(tx *gorm.DB, issueID, body string) ([]string, error) {
	if tx == nil {
		tx = s.issueRepo.DB()
	}

	assets, err := s.assetRepo.ListByIssueIDTx(tx, issueID)
	if err != nil {
		return nil, err
	}

	referencedIDs := extractIssueAssetIDs(body)
	if len(assets) == 0 {
		return nil, nil
	}

	idsToAttach := make([]string, 0)
	idsToDelete := make([]string, 0)
	for _, asset := range assets {
		if _, ok := referencedIDs[asset.ID]; ok {
			if asset.Status != model.IssueAssetStatusAttached {
				idsToAttach = append(idsToAttach, asset.ID)
			}
			continue
		}
		idsToDelete = append(idsToDelete, asset.ID)
	}

	if err := s.assetRepo.UpdateStatusByIssueIDAndIDsTx(tx, issueID, idsToAttach, model.IssueAssetStatusAttached); err != nil {
		return nil, err
	}
	pathsToDelete, err := s.deleteIssueAssetsTx(tx, issueID, idsToDelete)
	if err != nil {
		return nil, err
	}
	return pathsToDelete, nil
}

func (s *IssueService) deleteIssueAssets(issueID string, ids []string) error {
	pathsToDelete, err := s.deleteIssueAssetsTx(nil, issueID, ids)
	if err != nil {
		return err
	}
	s.deleteIssueAssetFiles(pathsToDelete)
	return nil
}

func (s *IssueService) deleteIssueAssetsTx(tx *gorm.DB, issueID string, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	if tx == nil {
		tx = s.issueRepo.DB()
	}

	assets, err := s.assetRepo.ListByIssueIDTx(tx, issueID)
	if err != nil {
		return nil, err
	}

	pathsToDelete := make([]string, 0, len(ids))
	pathByID := make(map[string]string, len(assets))
	for _, asset := range assets {
		pathByID[asset.ID] = asset.FilePath
	}

	for _, id := range ids {
		if path := pathByID[id]; path != "" {
			pathsToDelete = append(pathsToDelete, path)
		}
	}

	if err := s.assetRepo.DeleteByIssueIDAndIDsTx(tx, issueID, ids); err != nil {
		return nil, err
	}
	return pathsToDelete, nil
}

func (s *IssueService) deleteIssueAssetFiles(paths []string) {
	for _, path := range paths {
		if path == "" {
			continue
		}
		_ = s.storage.Delete(path)
	}
}

func (s *IssueService) deleteDraftIssueAssets(projectID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}

	assets, err := s.draftAssetRepo.ListByProjectIDAndIDs(projectID, ids)
	if err != nil {
		return err
	}

	pathByID := make(map[string]string, len(assets))
	for _, asset := range assets {
		pathByID[asset.ID] = asset.FilePath
	}

	if err := s.draftAssetRepo.DeleteByProjectIDAndIDs(projectID, ids); err != nil {
		return err
	}
	for _, id := range ids {
		if path := pathByID[id]; path != "" {
			_ = s.storage.Delete(path)
		}
	}
	return nil
}

func (s *IssueService) syncIssueAssetsTx(tx *gorm.DB, projectID, issueID, body string) ([]string, error) {
	if err := s.validateIssueAssetReferencesTx(tx, projectID, issueID, body); err != nil {
		return nil, err
	}
	if err := s.attachDraftAssetsToIssueTx(tx, projectID, issueID, body); err != nil {
		return nil, err
	}
	return s.reconcileIssueAssetsTx(tx, issueID, body)
}

func (s *IssueService) attachDraftAssetsToIssueTx(tx *gorm.DB, projectID, issueID, body string) error {
	referencedIDs := extractIssueAssetIDs(body)
	if len(referencedIDs) == 0 {
		return nil
	}

	ids := make([]string, 0, len(referencedIDs))
	for id := range referencedIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	if tx == nil {
		tx = s.issueRepo.DB()
	}

	existingAssets, err := s.assetRepo.ListByIssueIDTx(tx, issueID)
	if err != nil {
		return err
	}

	existingIDs := make(map[string]struct{}, len(existingAssets))
	for _, asset := range existingAssets {
		existingIDs[asset.ID] = struct{}{}
	}

	draftIDs := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := existingIDs[id]; ok {
			continue
		}
		draftIDs = append(draftIDs, id)
	}
	if len(draftIDs) == 0 {
		return nil
	}

	draftAssets, err := s.draftAssetRepo.ListByProjectIDAndIDsTx(tx, projectID, draftIDs)
	if err != nil {
		return err
	}
	if len(draftAssets) != len(draftIDs) {
		return errs.ErrInvalidParams
	}

	attachedIDs := make([]string, 0, len(draftAssets))
	for _, asset := range draftAssets {
		issueAsset := &model.IssueAsset{
			ID:              asset.ID,
			IssueID:         issueID,
			FileName:        asset.FileName,
			FilePath:        asset.FilePath,
			MimeType:        asset.MimeType,
			FileSize:        asset.FileSize,
			Status:          model.IssueAssetStatusAttached,
			CreatedByUserID: asset.CreatedByUserID,
			CreatedAt:       asset.CreatedAt,
		}
		if err := s.assetRepo.CreateTx(tx, issueAsset); err != nil {
			return err
		}
		attachedIDs = append(attachedIDs, asset.ID)
	}

	return s.draftAssetRepo.DeleteByProjectIDAndIDsTx(tx, projectID, attachedIDs)
}

func (s *IssueService) validateIssueAssetReferences(projectID, issueID, body string) error {
	return s.validateIssueAssetReferencesTx(nil, projectID, issueID, body)
}

func (s *IssueService) validateIssueAssetReferencesTx(tx *gorm.DB, projectID, issueID, body string) error {
	referencedIDs := extractIssueAssetIDs(body)
	if len(referencedIDs) == 0 {
		return nil
	}

	if tx == nil {
		tx = s.issueRepo.DB()
	}

	existingIDs := make(map[string]struct{}, len(referencedIDs))
	if strings.TrimSpace(issueID) != "" {
		issueAssets, err := s.assetRepo.ListByIssueIDTx(tx, issueID)
		if err != nil {
			return err
		}
		for _, asset := range issueAssets {
			existingIDs[asset.ID] = struct{}{}
		}
	}

	draftIDs := make([]string, 0, len(referencedIDs))
	for id := range referencedIDs {
		if _, ok := existingIDs[id]; ok {
			continue
		}
		draftIDs = append(draftIDs, id)
	}
	if len(draftIDs) == 0 {
		return nil
	}

	sort.Strings(draftIDs)
	draftAssets, err := s.draftAssetRepo.ListByProjectIDAndIDsTx(tx, projectID, draftIDs)
	if err != nil {
		return err
	}
	if len(draftAssets) != len(draftIDs) {
		return errs.ErrInvalidParams
	}
	return nil
}

func mapIssueAssetReferenceError(err error) error {
	if err == nil {
		return nil
	}

	var appErr *errs.AppError
	if errors.As(err, &appErr) {
		return appErr
	}
	return errs.ErrInternal
}

func resolveIssueAssetIDFromURL(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	parsed, err := neturl.Parse(value)
	if err == nil {
		value = parsed.Path
	}
	match := issueAssetContentPattern.FindStringSubmatch(value)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

type countingReader struct {
	reader io.Reader
	n      int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.n += int64(n)
	return n, err
}

func (s *IssueService) storeDraftIssueAsset(projectID, userID, fileName string, reader io.Reader) (*model.IssueDraftAsset, error) {
	readFrom := reader
	if s.cfg.Upload.MaxFileSize > 0 {
		readFrom = io.LimitReader(reader, s.cfg.Upload.MaxFileSize+1)
	}

	head := make([]byte, issueAssetSniffBytes)
	headSize, err := io.ReadFull(readFrom, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, errs.ErrInternal
	}
	head = head[:headSize]
	if len(head) == 0 {
		return nil, errs.ErrInvalidParams
	}

	mimeType := http.DetectContentType(head)
	if !strings.HasPrefix(mimeType, "image/") {
		return nil, errs.ErrInvalidParams
	}

	assetID := uuid.NewString()
	storagePath := buildIssueDraftAssetStoragePath(projectID, assetID, fileName, mimeType)
	uploadReader := io.MultiReader(bytes.NewReader(head), readFrom)
	countedReader := &countingReader{reader: uploadReader}
	if err := s.storage.Save(storagePath, countedReader); err != nil {
		_ = s.storage.Delete(storagePath)
		return nil, errs.ErrInternal
	}
	if s.cfg.Upload.MaxFileSize > 0 && countedReader.n > s.cfg.Upload.MaxFileSize {
		_ = s.storage.Delete(storagePath)
		return nil, errs.ErrInvalidParams
	}

	asset := &model.IssueDraftAsset{
		ID:              assetID,
		ProjectID:       projectID,
		FileName:        normalizeIssueAssetFileName(fileName, mimeType),
		FilePath:        storagePath,
		MimeType:        mimeType,
		FileSize:        countedReader.n,
		CreatedByUserID: userID,
		CreatedAt:       time.Now().UTC(),
	}

	if err := s.draftAssetRepo.Create(asset); err != nil {
		_ = s.storage.Delete(storagePath)
		return nil, errs.ErrInternal
	}

	return asset, nil
}

func buildInternalIssueMeta(issueID, userID string, workflowStatus model.IssueWorkflowStatus, now time.Time) *model.IssueInternalMeta {
	meta := &model.IssueInternalMeta{
		IssueID:         issueID,
		UpdatedByUserID: userID,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	applyExplicitWorkflowStatus(meta, workflowStatus, now)
	return meta
}

func (s *IssueService) internalMetaByIssueIDs(issues []model.Issue) (map[string]*model.IssueInternalMeta, error) {
	issueIDs := make([]string, 0, len(issues))
	for _, issue := range issues {
		issueIDs = append(issueIDs, issue.ID)
	}

	raw, err := s.internalMetaRepo.ListByIssueIDs(issueIDs)
	if err != nil {
		return nil, err
	}

	result := make(map[string]*model.IssueInternalMeta, len(raw))
	for issueID, meta := range raw {
		current := meta
		result[issueID] = &current
	}
	return result, nil
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
		Source:            comment.Source,
		GitHubCommentID:   comment.GitHubCommentID,
		GitHubNodeID:      comment.GitHubNodeID,
		Body:              comment.Body,
		BodyHTML:          githubmedia.RewriteHTMLMediaSources(comment.BodyHTML),
		HTMLURL:           comment.HTMLURL,
		Author:            IssueActorResponse{Login: comment.AuthorLogin, AvatarURL: githubmedia.RewriteMediaURL(comment.AuthorAvatarURL)},
		AuthorAssociation: comment.AuthorAssociation,
		Reactions:         reactions,
		CreatedAt:         formatTime(comment.GitHubCreatedAt),
		UpdatedAt:         formatTime(comment.GitHubUpdatedAt),
	}
}

func buildGitHubIssueCommentModel(issueID string, item *ghclient.IssueComment) *model.IssueComment {
	comment := &model.IssueComment{
		ID:                uuid.NewString(),
		IssueID:           issueID,
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
		RawJSON:           toJSONString(item),
	}
	if createdAt := item.GetCreatedAt(); !createdAt.IsZero() {
		comment.GitHubCreatedAt = createdAt.UTC()
	}
	if updatedAt := item.GetUpdatedAt(); !updatedAt.IsZero() {
		comment.GitHubUpdatedAt = updatedAt.UTC()
	}
	if comment.GitHubCreatedAt.IsZero() {
		comment.GitHubCreatedAt = time.Now().UTC()
	}
	if comment.GitHubUpdatedAt.IsZero() {
		comment.GitHubUpdatedAt = comment.GitHubCreatedAt
	}
	return comment
}

func toIssueTimelineResponse(event model.IssueTimelineEvent) IssueTimelineEventResponse {
	return IssueTimelineEventResponse{
		ID:            event.ID,
		IssueID:       event.IssueID,
		EventKey:      event.EventKey,
		EventType:     event.EventType,
		GitHubEventID: event.GitHubEventID,
		Actor:         IssueActorResponse{Login: event.ActorLogin, AvatarURL: githubmedia.RewriteMediaURL(event.ActorAvatarURL)},
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

func mapLabelNames(labels []*gh.Label) []string {
	out := make([]string, 0, len(labels))
	for _, label := range labels {
		if label == nil {
			continue
		}
		name := strings.TrimSpace(label.GetName())
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

func extractLabelNames(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	var names []string
	if err := json.Unmarshal([]byte(raw), &names); err == nil {
		return names
	}

	var payloads []issueLabelPayload
	if err := json.Unmarshal([]byte(raw), &payloads); err == nil {
		result := make([]string, 0, len(payloads))
		for _, p := range payloads {
			if p.Name != "" {
				result = append(result, p.Name)
			}
		}
		return result
	}

	return nil
}

func (s *IssueService) resolveLabels(projectID string, labelNames []string, labelMap map[string]model.GitHubRepoLabel) []IssueLabelResponse {
	if len(labelNames) == 0 {
		return nil
	}
	if labelMap == nil {
		allLabels, err := s.githubRepoLabelRepo.ListByProject(projectID)
		if err == nil {
			labelMap = make(map[string]model.GitHubRepoLabel, len(allLabels))
			for _, l := range allLabels {
				labelMap[l.Name] = l
			}
		}
	}
	result := make([]IssueLabelResponse, 0, len(labelNames))
	for _, name := range labelNames {
		if repoLabel, ok := labelMap[name]; ok {
			result = append(result, IssueLabelResponse{
				Name:        repoLabel.Name,
				Color:       repoLabel.Color,
				Description: repoLabel.Description,
			})
		} else {
			result = append(result, IssueLabelResponse{
				Name:  name,
				Color: "999999",
			})
		}
	}
	return result
}

func (s *IssueService) buildLabelMap(projectID string) map[string]model.GitHubRepoLabel {
	allLabels, err := s.githubRepoLabelRepo.ListByProject(projectID)
	if err != nil {
		return nil
	}
	m := make(map[string]model.GitHubRepoLabel, len(allLabels))
	for _, l := range allLabels {
		m[l.Name] = l
	}
	return m
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

func buildIssueReference(issue model.Issue) string {
	if issue.Source == model.IssueSourceGitHub && issue.GitHubMeta != nil {
		return fmt.Sprintf("GH-%d", issue.GitHubMeta.Number)
	}
	return fmt.Sprintf("INT-%d", issue.SequenceNumber)
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

type checklistSnapshot struct {
	ProgressPercent *int
	Total           int
	Done            int
}

func buildChecklistSnapshot(issueID, userID string, items []IssueChecklistItemInput) ([]model.IssueChecklistItem, checklistSnapshot, error) {
	now := time.Now().UTC()
	result := make([]model.IssueChecklistItem, 0, len(items))
	done := 0

	for index, item := range items {
		title := strings.TrimSpace(item.Title)
		if title == "" {
			return nil, checklistSnapshot{}, errs.ErrInvalidParams
		}

		id := strings.TrimSpace(item.ID)
		if id == "" {
			id = uuid.NewString()
		}

		result = append(result, model.IssueChecklistItem{
			ID:              id,
			IssueID:         issueID,
			Title:           title,
			IsCompleted:     item.IsCompleted,
			SortOrder:       index,
			CreatedByUserID: userID,
			CreatedAt:       now,
			UpdatedAt:       now,
		})
		if item.IsCompleted {
			done++
		}
	}

	snapshot := checklistSnapshot{
		Total: len(result),
		Done:  done,
	}
	if len(result) > 0 {
		value := int(math.Round(float64(done*100) / float64(len(result))))
		snapshot.ProgressPercent = &value
	}
	return result, snapshot, nil
}

func applyExplicitWorkflowStatus(meta *model.IssueInternalMeta, workflowStatus model.IssueWorkflowStatus, now time.Time) {
	meta.WorkflowStatus = workflowStatus

	switch workflowStatus {
	case "", model.IssueWorkflowStatusTodo:
		if meta.CompletedAt == nil {
			meta.StartedAt = nil
		}
	case model.IssueWorkflowStatusInProgress:
		if meta.StartedAt == nil {
			value := now
			meta.StartedAt = &value
		}
	case model.IssueWorkflowStatusDone:
		if meta.StartedAt == nil {
			value := now
			meta.StartedAt = &value
		}
		if meta.CompletedAt == nil {
			value := now
			meta.CompletedAt = &value
		}
	}
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

func isValidIssueState(state model.IssueState) bool {
	switch state {
	case model.IssueStateOpen, model.IssueStateClosed:
		return true
	default:
		return false
	}
}

func normalizeIssueStateReason(state *model.IssueState, reason *string) (string, *errs.AppError) {
	if state == nil {
		return "", nil
	}

	value := ""
	if reason != nil {
		value = strings.TrimSpace(*reason)
	}
	if value == "" {
		return "", nil
	}

	switch *state {
	case model.IssueStateClosed:
		if value == "completed" || value == "not_planned" {
			return value, nil
		}
	case model.IssueStateOpen:
		if value == "reopened" {
			return value, nil
		}
	}
	return "", errs.ErrInvalidParams
}

func normalizeGitHubLabels(labels []string) ([]string, *errs.AppError) {
	normalized := make([]string, 0, len(labels))
	seen := make(map[string]struct{}, len(labels))

	for _, label := range labels {
		value := strings.TrimSpace(label)
		if value == "" {
			return nil, errs.ErrInvalidParams
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, value)
	}

	return normalized, nil
}

func resolveInternalLabels(names []string, repoLabels []IssueLabelResponse) ([]string, *errs.AppError) {
	labelMap := make(map[string]IssueLabelResponse, len(repoLabels))
	for _, l := range repoLabels {
		labelMap[strings.ToLower(l.Name)] = l
	}
	result := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		value := strings.TrimSpace(name)
		if value == "" {
			return nil, errs.ErrInvalidParams
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		if _, ok := labelMap[key]; !ok {
			return nil, errs.New(errs.ErrInvalidParams.Code, fmt.Sprintf("标签不存在: %s", value))
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func sortedKeys(values map[string]struct{}) []string {
	items := make([]string, 0, len(values))
	for value := range values {
		items = append(items, value)
	}
	sort.Strings(items)
	return items
}

func issueCommentCount(issue model.Issue) int {
	if issue.GitHubMeta == nil {
		return 0
	}
	return issue.GitHubMeta.CommentsCount
}

func issueSortNumber(issue model.Issue) int {
	if issue.Source == model.IssueSourceGitHub && issue.GitHubMeta != nil {
		return issue.GitHubMeta.Number
	}
	return issue.SequenceNumber
}

func sortIssues(items []model.Issue, sortKey string) {
	switch sortKey {
	case "updated_asc":
		sort.Slice(items, func(i, j int) bool {
			if items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
				return issueSortNumber(items[i]) < issueSortNumber(items[j])
			}
			return items[i].UpdatedAt.Before(items[j].UpdatedAt)
		})
	case "created_desc":
		sort.Slice(items, func(i, j int) bool {
			if items[i].CreatedAt.Equal(items[j].CreatedAt) {
				return issueSortNumber(items[i]) > issueSortNumber(items[j])
			}
			return items[i].CreatedAt.After(items[j].CreatedAt)
		})
	case "created_asc":
		sort.Slice(items, func(i, j int) bool {
			if items[i].CreatedAt.Equal(items[j].CreatedAt) {
				return issueSortNumber(items[i]) < issueSortNumber(items[j])
			}
			return items[i].CreatedAt.Before(items[j].CreatedAt)
		})
	case "comments_desc":
		sort.Slice(items, func(i, j int) bool {
			if issueCommentCount(items[i]) == issueCommentCount(items[j]) {
				return items[i].UpdatedAt.After(items[j].UpdatedAt)
			}
			return issueCommentCount(items[i]) > issueCommentCount(items[j])
		})
	case "comments_asc":
		sort.Slice(items, func(i, j int) bool {
			if issueCommentCount(items[i]) == issueCommentCount(items[j]) {
				return items[i].UpdatedAt.Before(items[j].UpdatedAt)
			}
			return issueCommentCount(items[i]) < issueCommentCount(items[j])
		})
	default:
		sort.Slice(items, func(i, j int) bool {
			if items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
				return issueSortNumber(items[i]) > issueSortNumber(items[j])
			}
			return items[i].UpdatedAt.After(items[j].UpdatedAt)
		})
	}
}

func matchesIssueFilters(issue model.Issue, gitHubMeta *model.IssueGitHubMeta, meta *model.IssueInternalMeta, filters IssueListFilters) bool {
	if filters.State != "" && string(issue.State) != filters.State {
		return false
	}

	switch filters.Workflow {
	case "":
	case "unset":
		if meta != nil && meta.WorkflowStatus != "" {
			return false
		}
	default:
		if meta == nil || string(meta.WorkflowStatus) != filters.Workflow {
			return false
		}
	}

	query := strings.TrimSpace(filters.Query)
	if query != "" {
		queryLower := strings.ToLower(query)
		matches := strings.Contains(strings.ToLower(issue.Title), queryLower) ||
			strings.Contains(strings.ToLower(issue.Body), queryLower) ||
			strings.Contains(strings.ToLower(issue.AuthorLogin), queryLower) ||
			strings.Contains(strings.ToLower(buildIssueReference(issue)), queryLower)
		if num, ok := parseIssueNumberQuery(query); ok {
			if issue.SequenceNumber == num {
				matches = true
			}
			if gitHubMeta != nil && gitHubMeta.Number == num {
				matches = true
			}
		}
		if !matches {
			return false
		}
	}

	if filters.Label != "" {
		matched := false
		if gitHubMeta != nil {
			for _, name := range extractLabelNames(gitHubMeta.LabelsJSON) {
				if strings.EqualFold(name, filters.Label) {
					matched = true
					break
				}
			}
		}
		if !matched && meta != nil && meta.LabelsJSON != "" {
			for _, name := range extractLabelNames(meta.LabelsJSON) {
				if strings.EqualFold(name, filters.Label) {
					matched = true
					break
				}
			}
		}
		if !matched {
			return false
		}
	}

	if filters.Source != "" && string(issue.Source) != filters.Source {
		return false
	}

	if filters.Assignee != "" {
		if gitHubMeta == nil {
			return false
		}
		matched := false
		for _, assignee := range parseJSON[[]issueUserPayload](gitHubMeta.AssigneesJSON) {
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
		if gitHubMeta == nil {
			return false
		}
		milestone := parseJSON[*issueMilestonePayload](gitHubMeta.MilestoneJSON)
		if milestone == nil || !strings.EqualFold(milestone.Title, filters.Milestone) {
			return false
		}
	}

	return true
}
