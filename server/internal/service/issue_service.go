package service

import (
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/githubapi"
	ghclient "github.com/godbobo/fast_ship/server/internal/pkg/github"
	"github.com/godbobo/fast_ship/server/internal/pkg/storage"
	"github.com/godbobo/fast_ship/server/internal/repository"
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
	credentials         *ProjectCredentials
	newClient           ghclient.ClientFactory
	// newPRClient 构造关联 PR 读取客户端（attach/sync）；测试注入替身的缝。
	newPRClient func(token string) githubapi.GitHubClient
	mirror      *issueMirror
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
	tokenRepo *repository.UserGitHubTokenRepository,
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
		credentials:         newProjectCredentials(projectRepo, tokenRepo, cfg, logger),
		newClient: func(token, owner, repo string) ghclient.Client {
			return ghclient.NewClient(token, owner, repo)
		},
		newPRClient: func(token string) githubapi.GitHubClient {
			return ghclient.NewPRClient(token)
		},
	}
	// 镜像的 client 工厂在调用时才读 s.newClient，测试替换对两条路径同时生效；
	// 凭证模块与 IssueService 共用同一实例。
	s.mirror = &issueMirror{
		issueRepo:           issueRepo,
		gitHubMetaRepo:      gitHubMetaRepo,
		commentRepo:         commentRepo,
		timelineRepo:        timelineRepo,
		recRepo:             recRepo,
		syncStateRepo:       syncStateRepo,
		githubRepoLabelRepo: githubRepoLabelRepo,
		projectRepo:         projectRepo,
		logger:              logger,
		credentials:         s.credentials,
		newClient: func(token, owner, repo string) ghclient.Client {
			return s.newClient(token, owner, repo)
		},
		syncing: make(map[string]struct{}),
	}
	return s
}
