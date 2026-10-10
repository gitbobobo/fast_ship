package service

import (
	"context"
	"github.com/godbobo/fast_ship/server/internal/config"
	ghclient "github.com/godbobo/fast_ship/server/internal/pkg/github"
	"github.com/godbobo/fast_ship/server/internal/pkg/storage"
	"github.com/godbobo/fast_ship/server/internal/repository"
	gh "github.com/google/go-github/v62/github"
	"go.uber.org/zap"
	"regexp"
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
	GetPullRequest(ctx context.Context, number int) (*gh.PullRequest, error)
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
	readStateRepo       *repository.IssueReadStateRepository
	recRepo             *repository.IssueRecommendationRepository
	pullRequestRepo     *repository.IssuePullRequestRepository
	attachmentRepo      *repository.IssueAttachmentRepository
	annotationRepo      *repository.ScreenshotAnnotationRepository
	storage             storage.Storage
	cfg                 *config.Config
	logger              *zap.Logger
	newClient           gitHubIssueClientFactory
	mirror              *issueMirror
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

type issueLabelPayload struct {
	Name        string `json:"name"`
	Color       string `json:"color"`
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
	readStateRepo *repository.IssueReadStateRepository,
	recRepo *repository.IssueRecommendationRepository,
	pullRequestRepo *repository.IssuePullRequestRepository,
	attachmentRepo *repository.IssueAttachmentRepository,
	storage storage.Storage,
	cfg *config.Config,
	logger *zap.Logger,
	annotationRepo *repository.ScreenshotAnnotationRepository,
) *IssueService {
	s := &IssueService{
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
		readStateRepo:       readStateRepo,
		recRepo:             recRepo,
		pullRequestRepo:     pullRequestRepo,
		attachmentRepo:      attachmentRepo,
		annotationRepo:      annotationRepo,
		storage:             storage,
		cfg:                 cfg,
		logger:              logger,
		newClient: func(token, owner, repo string) gitHubIssueClient {
			return ghclient.NewClient(token, owner, repo)
		},
	}
	// 镜像的 client 工厂在调用时才读 s.newClient，测试替换对两条路径同时生效。
	s.mirror = &issueMirror{
		issueRepo:           issueRepo,
		gitHubMetaRepo:      gitHubMetaRepo,
		commentRepo:         commentRepo,
		timelineRepo:        timelineRepo,
		recRepo:             recRepo,
		syncStateRepo:       syncStateRepo,
		githubRepoLabelRepo: githubRepoLabelRepo,
		projectRepo:         projectRepo,
		cfg:                 cfg,
		logger:              logger,
		newClient: func(token, owner, repo string) gitHubIssueClient {
			return s.newClient(token, owner, repo)
		},
		syncing: make(map[string]struct{}),
	}
	return s
}
