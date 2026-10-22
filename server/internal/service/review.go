package service

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubmedia"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// PR 审查（INT-135）：项目级开关 + 系统受控流程取代原通用自动化。
// 本服务承载：webhook 事务（幂等登记、防抖写库）、调度循环（防抖消费/排队/
// 派发/超时/同步/工作区清理）、运行令牌与 Agent 端点、GitHub 产出与 rerun。
// 事务内零 GitHub/T3 调用；网络调用全部在调度与端点的事务外。

// ErrDeliveryDuplicated 同一 X-GitHub-Delivery 的重复投递：handler 仍回 202，
// 但不重复处理。
var ErrDeliveryDuplicated = errors.New("review: delivery duplicated")

// reviewDeliveryRetention webhook 幂等账保留时长。
const reviewDeliveryRetention = 7 * 24 * time.Hour

// reviewT3 审查执行对 T3 的最小依赖面；*T3ThreadsService 实现，测试可换桩。
// CreateThreadForAgent 保留 launchSent 语义：未发出=明确失败，发出未确认=等超时。
type reviewT3 interface {
	CreateThreadForAgent(ctx context.Context, userID, envID string, req *T3CreateThreadRequest) (*T3ThreadLaunchResult, bool, error)
	StopThread(ctx context.Context, userID, envID, threadID string) (*T3ThreadStopResult, error)
	ThreadWorktree(ctx context.Context, userID, envID, threadID string) (*T3ThreadWorktree, error)
	ListWorktrees(ctx context.Context, userID, envID, cwd string) ([]T3Worktree, error)
	RemoveWorktree(ctx context.Context, userID, envID, cwd, path string, force bool) error
	ArchiveThread(ctx context.Context, userID, envID, threadID string) error
	// FailedPreparation 判读线程是否处于可重试的工作区准备失败态；
	// RetryPreparation 代发 prepared-run.retry（INT-147）。
	FailedPreparation(ctx context.Context, userID, envID, threadID, messageID string) (*T3FailedPreparation, error)
	RetryPreparation(ctx context.Context, userID, envID, threadID, t3RunID, commandID string) error
}

// reviewMedia 截图下载面：任意公网 https 图片、只收图片、拨号校验 IP；磁盘
// 缓存。*githubmedia.ProxyService（NewReviewMediaService 构造）实现，匿名与
// Bearer 两路。
type reviewMedia interface {
	FetchAndCache(ctx context.Context, rawURL string) (*githubmedia.CachedMedia, error)
	FetchAndCacheWithBearer(ctx context.Context, rawURL, bearer, scope string) (*githubmedia.CachedMedia, error)
}

// ReviewService PR 审查协调面：webhook 事务（幂等登记与防抖写库）与调度循环
// 驱动共用。事务内零 GitHub/T3 调用；提交后非阻塞 kick。1b 起的审查设置/
// 记录查询也挂在本服务上：gh、appSource、publicURL 服务于读/写面与 GitHub 产出。
type ReviewService struct {
	db           *gorm.DB
	projects     *repository.ProjectRepository
	deliveries   *repository.GitHubWebhookDeliveryRepository
	redeliveries *repository.GitHubWebhookRedeliveryRepository
	sink         *NotificationSink
	cfg          *config.Config
	logger       *zap.Logger

	// gh 是 App 安装令牌访问面；appSource 回答部署是否配置了 App 身份；
	// publicURL 回答部署对外地址。三者均可为 nil——对应前置条件按缺失处理。
	gh        PRGitHubClients
	appSource GitHubAppSource
	publicURL PublicURLSource
	// t3 线程/工作区执行面、media 截图缓存下载面；均可为 nil（对应阶段按失败处理）。
	// mediaBearer 为截图下载取 App 安装令牌（私有仓库附件）：入参安装 id；
	// 可为 nil（不支持凭证下载时保持匿名）。
	t3          reviewT3
	media       reviewMedia
	mediaBearer func(ctx context.Context, installationID int64) (string, error)

	// 调度循环状态：kick 与 cancel 都经 mu 保护，供 webhook goroutine 与
	// Start/Stop 并发安全。kickCh 缓冲 1：连续投递只补一轮 tick，不堆积。
	mu     sync.Mutex
	kickCh chan struct{}
	cancel chan struct{}

	// worktreeTries：归档/工作区读取等回收阶段的有限重试计数（进程内即可：
	// 进程重启后未完成的行下个 tick 重新计，上限内不会漏终态）。
	worktreeMu    sync.Mutex
	worktreeTries map[string]int

	// prepRetryChecked 准备失败检测的查询节流表（INT-147）：每运行最多每
	// reviewPrepRetryQueryEvery 查一次 T3 投影。进程内即可（重启丢失顶多多
	// 查一轮）；注册新项时顺手清过期项，不留无界增长。
	// prepRetryCursor 记住上一次扫描停在哪条运行上：阶段预算耗尽后下一轮
	// 从它的下一条继续（环形），避免队首几条反复吃满预算、尾部饿到超时。
	prepRetryMu      sync.Mutex
	prepRetryChecked map[string]time.Time
	prepRetryCursor  string
	// prepRetryReadTimeout / prepRetryRPCTimeout 覆盖默认超时（零值用常量）：
	// 测试注入小超时，验证「投影读与重试 RPC 各用独立预算」。
	prepRetryReadTimeout time.Duration
	prepRetryRPCTimeout  time.Duration

	// prLocks 每 PR 一把进程内锁：同一 PR 的 GitHub 同步串行，避免并发重复写。
	prLocksMu sync.Mutex
	prLocks   map[string]*sync.Mutex

	// projectLocks 每项目一把进程内锁：删除项目的清理与 launchRun 互斥，
	// 避免「清理时线程还没确认、清理后线程才建成」的孤儿线程。
	projectLocksMu sync.Mutex
	projectLocks   map[string]*sync.Mutex
}

func NewReviewService(
	db *gorm.DB,
	projects *repository.ProjectRepository,
	deliveries *repository.GitHubWebhookDeliveryRepository,
	sink *NotificationSink,
	gh PRGitHubClients,
	appSource GitHubAppSource,
	publicURL PublicURLSource,
	t3 reviewT3,
	media reviewMedia,
	mediaBearer func(ctx context.Context, installationID int64) (string, error),
	cfg *config.Config,
	logger *zap.Logger,
) *ReviewService {
	return &ReviewService{
		db: db, projects: projects, deliveries: deliveries,
		redeliveries: repository.NewGitHubWebhookRedeliveryRepository(db),
		sink:         sink, gh: gh, appSource: appSource, publicURL: publicURL,
		t3: t3, media: media, mediaBearer: mediaBearer,
		cfg: cfg, logger: logger,
		worktreeTries:    map[string]int{},
		prepRetryChecked: map[string]time.Time{},
		prLocks:          map[string]*sync.Mutex{},
		projectLocks:     map[string]*sync.Mutex{},
	}
}

// HandleGitHubWebhook webhook 入口：github_webhook_deliveries 幂等登记 +
// 触发判定/SHA 前进/取消旧轮/防抖写行，全部在同一事务内（零网络调用）；
// 重复投递返回 ErrDeliveryDuplicated。
func (s *ReviewService) HandleGitHubWebhook(ctx context.Context, event, deliveryID string, body []byte) error {
	now := time.Now().UTC()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		inserted, err := s.deliveries.InsertTx(tx, &model.GitHubWebhookDelivery{
			DeliveryID: deliveryID,
			Event:      event,
			ReceivedAt: now,
		})
		if err != nil {
			return err
		}
		if !inserted {
			return ErrDeliveryDuplicated
		}
		return s.handleWebhookTx(tx, event, body, now)
	})
	if err != nil {
		if errors.Is(err, ErrDeliveryDuplicated) {
			return err
		}
		s.logger.Error("处理 GitHub webhook 失败",
			zap.String("delivery", deliveryID), zap.String("event", event), zap.Error(err))
		return err
	}
	// 事务提交后踢调度循环（循环未启动时为 no-op）。
	s.notifyReviewLoop()
	return nil
}

// lockProject 项目级互斥：launchRun 与删除项目的清理互斥。返回解锁函数。
func (s *ReviewService) lockProject(projectID string) func() {
	s.projectLocksMu.Lock()
	mu, ok := s.projectLocks[projectID]
	if !ok {
		mu = &sync.Mutex{}
		s.projectLocks[projectID] = mu
	}
	s.projectLocksMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

// notifyReviewLoop 非阻塞踢一轮调度 tick。
func (s *ReviewService) notifyReviewLoop() {
	s.mu.Lock()
	kickCh := s.kickCh
	s.mu.Unlock()
	if kickCh != nil {
		select {
		case kickCh <- struct{}{}:
		default:
		}
	}
}

// RunTokenIdentity 从运行令牌推出的身份：运行与其归属用户。
// /api/review-agent 端点（1c）经 RequireRunToken 消费。
type RunTokenIdentity struct {
	UserID string
	RunID  string
}

// RunTokenResolver 是 middleware.RequireRunToken 的令牌解析依赖；
// 由 ReviewService 在 1c 实现。
type RunTokenResolver interface {
	ResolveRunToken(ctx context.Context, rawToken string, requireLive bool) (*RunTokenIdentity, error)
}

// PRGitHubClients 解析审查链路的 GitHub App 访问面（INT-129 部署级身份：
// 每套部署一个 App，客户端按部署当前配置现取）。
type PRGitHubClients interface {
	ClientFor(ctx context.Context) (githubapi.GitHubClient, error)
}

// GitHubAppSource github_app 前置条件的读取面：当前生效的部署级 App 身份；
// ok=false 表示未配置（*IntegrationService 实现，fake debug 模式另由
// cfg.GitHubApp.Fake + server.mode=debug 判定）。
type GitHubAppSource interface {
	Current(ctx context.Context) (GitHubAppConfig, bool, error)
}
