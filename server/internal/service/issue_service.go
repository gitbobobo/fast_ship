package service

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/forge"
	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	ghclient "github.com/godbobo/fast_ship/server/internal/pkg/github"
	"github.com/godbobo/fast_ship/server/internal/pkg/storage"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"go.uber.org/zap"
)

var issueAssetContentPattern = regexp.MustCompile(`(?:https?://[^/\s"')]+)?/api/issues/assets/([0-9a-fA-F-]+)/content(?:\?[^)\s"']*)?`)

const (
	issueAssetSniffBytes        = 512
	issueAssetPendingTTL        = 24 * time.Hour
	batchCloseDoneMaxIssues     = 200
	batchCloseDoneMaxFailures   = 50
	batchCloseDoneTimeout       = 30 * time.Second
	batchCloseGitHubConcurrency = 4
	// 单个在途 GitHub PATCH 的独立上限（INT-185）：整批取消后已发出的请求仍要
	// 走完并落库，但不允许个别挂起请求把 worker 拖死。
	batchCloseGitHubPatchTimeout = 15 * time.Second
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
	// 关联 PR 的 Forgejo 侧（INT-181）：forgeIntegrationRepo 按主机键查集成，
	// forgeSource 按集成行现取访问三件套（生产接 ForgeClientSource），
	// newForgejoPRReader 由三件套组装读取客户端（测试注入替身的缝）。
	forgeIntegrationRepo *repository.ForgeIntegrationRepository
	forgeSource          issueForgeSource
	newForgejoPRReader   func(httpClient *http.Client, apiBase, token string) forgePRReader
	mirror               *issueMirror
	// batchCloseStorer 批量关闭的本地落地注入点（仅测试用，INT-185 R11）；
	// 生产为 nil，回落到 s.mirror。
	batchCloseStorer batchCloseMirrorStorer
	// runCloseInternalTx 内部关闭事务注入点（仅测试用，INT-185 R14 Note）：
	// 生产为 nil 走真实事务；测试注入一个返回包装错误的函数断言归因。
	runCloseInternalTx func(ids []string, now time.Time) ([]string, error)
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
	forgeIntegrationRepo *repository.ForgeIntegrationRepository,
	// forgeSource Forgejo 集成凭证面：与审查 resolver 共用同一实例（同一份
	// 传输层缓存），由装配点注入；测试可传 nil（forgejo 链路会报未配置）。
	forgeSource issueForgeSource,
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
		forgeIntegrationRepo: forgeIntegrationRepo,
		forgeSource:          forgeSource,
		newForgejoPRReader: func(httpClient *http.Client, apiBase, token string) forgePRReader {
			return forge.NewForgejoReviewClient(httpClient, apiBase, token)
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

// withRepos 返回绑定 ctx 的服务视图：热点端点（INT-187）的前置读库借连接
// 等待随 ctx 取消——MaxOpenConns(1) 下借连接排队没有超时上限，不绑 ctx
// 的读库无法随请求取消。浅拷贝——其余字段（logger、cfg、clients 工厂、
// forgeSource、mirror）共用不可变；credentials/shipHookService 同样浅拷贝
// 并只替换其中走库的字段。nil ctx 退化 Background（测试与内部调用用）。
func (s *IssueService) withRepos(ctx context.Context) *IssueService {
	if ctx == nil {
		ctx = context.Background()
	}
	cp := *s
	cp.issueRepo = s.issueRepo.WithContext(ctx)
	cp.projectRepo = s.projectRepo.WithContext(ctx)
	cp.internalMetaRepo = s.internalMetaRepo.WithContext(ctx)
	cp.githubRepoLabelRepo = s.githubRepoLabelRepo.WithContext(ctx)
	cp.readStateRepo = s.readStateRepo.WithContext(ctx)
	cp.pullRequestRepo = s.pullRequestRepo.WithContext(ctx)
	cp.forgeIntegrationRepo = s.forgeIntegrationRepo.WithContext(ctx)
	if s.credentials != nil {
		cred := *s.credentials
		cred.projectRepo = cp.projectRepo
		cred.tokenRepo = s.credentials.tokenRepo.WithContext(ctx)
		cp.credentials = &cred
	}
	if s.shipHookService != nil {
		hook := *s.shipHookService
		hook.shipHookRepo = s.shipHookService.shipHookRepo.WithContext(ctx)
		cp.shipHookService = &hook
	}
	return &cp
}

// loadStoreErr 热点前置读库失败归类：ctx 取消/预算到点与真实存储故障分开
// 报，不然用户只看到通用 500 无法判断该重试还是查库；ctx 错误经 Wrap 保留
// 错误链——调用方与测试仍能 errors.Is 到原始的 context.Canceled/
// DeadlineExceeded。
func loadStoreErr(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return errs.Wrap(errs.ErrInternal.Code, "读取超时，请稍后重试", err)
	}
	return errs.ErrInternal
}
