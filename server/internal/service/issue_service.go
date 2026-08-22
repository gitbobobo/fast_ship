package service

import (
	"context"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	ghclient "github.com/godbobo/fast_ship/server/internal/pkg/github"
	"github.com/godbobo/fast_ship/server/internal/pkg/storage"
	"github.com/godbobo/fast_ship/server/internal/repository"
	gh "github.com/google/go-github/v62/github"
	"go.uber.org/zap"
	"regexp"
	"sync"
	"time"
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
	shipHookService     *IssueShipHookService
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
	shipHookService *IssueShipHookService,
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
		shipHookService:     shipHookService,
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
