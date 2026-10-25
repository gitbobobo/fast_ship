package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/forge"
	"github.com/godbobo/fast_ship/server/internal/handler"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubapp"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubmedia"
	"github.com/godbobo/fast_ship/server/internal/pkg/selflog"
	"github.com/godbobo/fast_ship/server/internal/pkg/storage"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/godbobo/fast_ship/server/internal/router"
	"github.com/godbobo/fast_ship/server/internal/service"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type sqliteColumnInfo struct {
	Name string `gorm:"column:name"`
}

type sqliteIndexInfo struct {
	Name   string `gorm:"column:name"`
	Origin string `gorm:"column:origin"`
}

// autoMigrateModels 是启动时 AutoMigrate 的全量模型清单，测试也用它建全新库。
var autoMigrateModels = []interface{}{
	&model.User{},
	&model.UserGitHubToken{},
	&model.UserAISetting{},
	&model.UserIssuePromptSetting{},
	&model.ApiKey{},
	&model.Project{},
	&model.Version{},
	&model.Issue{},
	&model.IssueGitHubMeta{},
	&model.IssueComment{},
	&model.IssueTimelineEvent{},
	&model.IssueInternalMeta{},
	&model.IssueShipHook{},
	&model.IssueChecklistItem{},
	&model.IssueSyncState{},
	&model.IssueReadState{},
	&model.IssueReadCatchup{},
	&model.IssueAsset{},
	&model.IssueDraftAsset{},
	&model.IssueAttachment{},
	&model.Artifact{},
	&model.JWTBlacklist{},
	&model.RefreshToken{},
	&model.GitHubRepoLabel{},
	&model.IssueCollabDocument{},
	&model.IssuePullRequest{},
	&model.IssueRecommendation{},
	&model.RecommendationDependency{},
	&model.LogRun{},
	&model.LogRunChunk{},
	&model.LogEntry{},
	&model.Document{},
	&model.ScreenshotScreen{},
	&model.ScreenshotAnnotation{},
	&model.ScreenshotPendingDelete{},
	&model.T3Environment{},
	&model.ProjectT3Binding{},
	&model.Notification{},
	&model.GitHubWebhookDelivery{},
	&model.GitHubWebhookRedelivery{}, // INT-146 的 webhook 重投次数账。
	// PR 审查（INT-135）：项目级开关 + 审查运行/令牌/PR 总账/截图资产/防抖/已见 SHA。
	// 旧 automation_* 与 github_pr_* 表由 migrateReviewTables 在 AutoMigrate 前整表删除。
	&model.ProjectReviewSettings{},
	&model.ReviewRun{},
	&model.ReviewRunToken{},
	&model.ReviewPullRequest{},
	&model.ReviewAsset{},
	&model.ReviewDebounce{},
	&model.ReviewSeenSHA{},
	// 通用自动化下线时在途旧线程的墓碑（处理完即删行）。
	&model.LegacyAutomationThread{},
	&model.QuotaAccount{},
	// 部署级 App 与邀请注册（INT-129）：邀请表、部署设置单行表；users.role 列
	// 随 model.User 迁移，存量角色回填见 migrateUserRoles。用户级 integrations
	// 表在 B 轮删除，见 migrateDropIntegrations。
	&model.Invite{},
	&model.DeploymentSettings{},
	// 代码仓集成（INT-166）：forgejo 行表；projects 的两个集成引用列随
	// model.Project 迁移，存量行为 NULL（= 内置 github-default）。存量行的
	// host_key 回填见 migrateForgeHostKeys（必须先于 createBootIndexes 的
	// 同归一主机唯一索引）。
	&model.ForgeIntegration{},
}

func init() {
	autoMigrateModels = append(autoMigrateModels, repository.BotModels()...)
}

func main() {
	// 启动期引导日志：配置与日志管道就绪前的失败只能走 stderr。
	boot := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	fatal := func(msg string, err error) {
		boot.Error(msg, slog.String("error", err.Error()))
		os.Exit(1)
	}

	// 加载配置
	cfgPath := "configs/config.yaml"
	if envPath := os.Getenv("CONFIG_PATH"); envPath != "" {
		cfgPath = envPath
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		fatal("加载配置失败", err)
	}

	// 自身日志管道（INT-94）：slog → 异步批量写独立 SQLite 日志库；
	// 写库失败只计数丢弃，绝不反压业务。
	if err := os.MkdirAll(filepath.Dir(cfg.Logging.SelfLogPath()), 0750); err != nil {
		fatal("创建自身日志目录失败", err)
	}
	selfLogStore, err := selflog.OpenStore(cfg.Logging.SelfLogPath())
	if err != nil {
		fatal("打开自身日志库失败", err)
	}
	selfLogPipeline, err := selflog.Start(selflog.Options{
		Sink:     selfLogStore,
		MinLevel: selfLogMinLevel(cfg.Logging.MinLevel),
		Retention: selflog.RetentionOptions{
			KeepDays:   cfg.Logging.SelfLogRetentionDays(),
			MaxEntries: cfg.Logging.SelfLogMaxEntries(),
			Interval:   cfg.Logging.SelfLogCleanupInterval(),
		},
	})
	if err != nil {
		fatal("启动自身日志管道失败", err)
	}
	defer selfLogPipeline.Close()
	appLogger := slog.New(selfLogPipeline.Handler())

	// 初始化日志：zap 保留给既有服务层；stdout/err 可读输出之外，
	// 通过 zapcore Tee 把同一份日志桥接进自身日志库（INT-94）。
	var stdoutCore zapcore.Core
	stackLevel := zapcore.ErrorLevel
	if cfg.Server.Mode == "debug" {
		stdoutCore = zapcore.NewCore(
			zapcore.NewConsoleEncoder(zap.NewDevelopmentEncoderConfig()),
			zapcore.Lock(os.Stderr), zapcore.DebugLevel)
		stackLevel = zapcore.WarnLevel
	} else {
		stdoutCore = zapcore.NewCore(
			zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
			zapcore.Lock(os.Stdout), zapcore.InfoLevel)
	}
	zapLogger := zap.New(
		zapcore.NewTee(stdoutCore, selflog.NewZapCore(selfLogPipeline.IngestHandler(), zapcore.DebugLevel)),
		zap.AddStacktrace(stackLevel),
	)
	defer zapLogger.Sync()

	// 确保数据目录存在
	dbDir := filepath.Dir(cfg.Database.Path)
	if err := os.MkdirAll(dbDir, 0750); err != nil {
		fatal("创建数据目录失败", err)
	}

	// 初始化数据库
	gormLogger := logger.Default.LogMode(logger.Silent)
	if cfg.Database.LogSQL {
		gormLogger = logger.Default.LogMode(logger.Info)
	}

	db, err := gorm.Open(sqlite.Open(cfg.Database.Path+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(30000)"), &gorm.Config{
		Logger: gormLogger,
	})
	if err != nil {
		fatal("连接数据库失败", err)
	}

	// SQLite 文件锁是连接级竞争：单连接串行化避免跨连接 BUSY；
	// 空闲超时让毒化连接（如 COMMIT BUSY 后悬空的事务）被关闭回滚，避免永久持锁；
	// 持续流量下空闲计时会被反复重置，再用生命周期兜底强制回收。
	sqlDB, err := db.DB()
	if err != nil {
		fatal("获取数据库连接失败", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetConnMaxIdleTime(5 * time.Minute)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)

	// 初始化存储（迁移要清理旧截图文件，先于迁移创建）
	fileStorage := storage.NewLocalStorage(cfg.Upload.StoragePath)

	// 全部库迁移收敛到一个入口：--dry-run 在任何写入之前只读统计并退出（INT-99）。
	if err := runBootMigrations(db, fileStorage, zapLogger, dryRun(), foreignKeyBackupPath(cfg.Database.Path), []byte(cfg.Encryption.Key)); err != nil {
		fatal("数据库迁移失败", err)
	}
	if dryRun() {
		boot.Info("dry-run 结束，未做任何修改")
		return
	}

	// 机器人（INT-101）：部分索引 / 复合索引 / 消息防改写守护在 AutoMigrate
	// 之后集中补齐；任何一条失败都启动退出，绝不带错运行。
	if err := repository.EnsureBotSchema(db); err != nil {
		fatal("机器人表结构初始化失败", err)
	}

	// 初始化 Repository
	userRepo := repository.NewUserRepository(db)
	userAISettingRepo := repository.NewUserAISettingRepository(db)
	userIssuePromptRepo := repository.NewUserIssuePromptSettingRepository(db)
	apiKeyRepo := repository.NewApiKeyRepository(db)
	githubTokenRepo := repository.NewUserGitHubTokenRepository(db)
	dashboardRepo := repository.NewDashboardRepository(db)
	projectRepo := repository.NewProjectRepository(db)
	versionRepo := repository.NewVersionRepository(db)
	issueRepo := repository.NewIssueRepository(db)
	issueGitHubMetaRepo := repository.NewIssueGitHubMetaRepository(db)
	issueReadStateRepo := repository.NewIssueReadStateRepository(db)
	issueCommentRepo := repository.NewIssueCommentRepository(db)
	issueTimelineRepo := repository.NewIssueTimelineRepository(db)
	issueInternalMetaRepo := repository.NewIssueInternalMetaRepository(db)
	issueShipHookRepo := repository.NewIssueShipHookRepository(db)
	issueShipHookService := service.NewIssueShipHookService(issueRepo, projectRepo, issueShipHookRepo)
	issueChecklistRepo := repository.NewIssueChecklistRepository(db)
	issueSyncStateRepo := repository.NewIssueSyncStateRepository(db)
	issueAssetRepo := repository.NewIssueAssetRepository(db)
	issueDraftAssetRepo := repository.NewIssueDraftAssetRepository(db)
	githubRepoLabelRepo := repository.NewGitHubRepoLabelRepository(db)
	issueCollabRepo := repository.NewIssueCollabRepository(db)
	issuePullRequestRepo := repository.NewIssuePullRequestRepository(db)
	issueAttachmentRepo := repository.NewIssueAttachmentRepository(db)
	recommendationRepo := repository.NewIssueRecommendationRepository(db)
	logRepo := repository.NewLogRepository(db)
	documentRepo := repository.NewDocumentRepository(db)
	artifactRepo := repository.NewArtifactRepository(db)
	screenshotRepo := repository.NewScreenshotRepository(db)
	screenshotAnnotationRepo := repository.NewScreenshotAnnotationRepository(db)
	t3EnvironmentRepo := repository.NewT3EnvironmentRepository(db)
	projectT3BindingRepo := repository.NewProjectT3BindingRepository(db)
	jwtBlacklistRepo := repository.NewJWTBlacklistRepository(db)
	refreshTokenRepo := repository.NewRefreshTokenRepository(db)
	notificationRepo := repository.NewNotificationRepository(db)
	webhookDeliveryRepo := repository.NewGitHubWebhookDeliveryRepository(db)
	quotaAccountRepo := repository.NewQuotaAccountRepository(db)
	// 部署级 App 与邀请注册（INT-129）。
	inviteRepo := repository.NewInviteRepository(db)
	deploymentSettingsRepo := repository.NewDeploymentSettingsRepository(db)
	// 代码仓集成（INT-166）。
	forgeIntegrationRepo := repository.NewForgeIntegrationRepository(db)

	// 业务库就绪后再注入归属解析器：注入前的入库日志只能是启动类（显式 system_scope）。
	selfLogPipeline.SetOwnerResolver(service.NewSelfLogOwnerResolver(db))

	// 模块化日志（INT-94）：按模块打上 module 字段，便于自身日志按模块检索。
	githubLogger := zapLogger.With(zap.String("module", "github"))
	t3Logger := zapLogger.With(zap.String("module", "t3"))
	reviewLogger := zapLogger.With(zap.String("module", "review"))
	quotaLogger := zapLogger.With(zap.String("module", "quota"))

	// 初始化 Service
	authService := service.NewAuthService(userRepo, jwtBlacklistRepo, refreshTokenRepo, cfg, appLogger.With(slog.String("module", "auth")))
	// 邀请注册（INT-129）：可选依赖，注入后「已有用户时凭邀请注册」才生效。
	authService.SetInviteRepository(inviteRepo)
	aiService := service.NewAIService(userAISettingRepo, issueRepo, issueCommentRepo, projectRepo, cfg, zapLogger.With(zap.String("module", "ai")))
	issuePromptService := service.NewIssuePromptService(userIssuePromptRepo, t3EnvironmentRepo)
	apiKeyService := service.NewApiKeyService(apiKeyRepo, appLogger.With(slog.String("module", "api_key")))
	githubTokenService := service.NewGitHubTokenService(githubTokenRepo, cfg, githubLogger)
	// 部署设置（INT-129）：public_url 网页配置的唯一读取入口；既有读取点
	// 统一改经它解析。
	deploymentSettingsService := service.NewDeploymentSettingsService(deploymentSettingsRepo, cfg)
	// 集成（INT-129）：部署级 GitHub App 身份，管理员配置、保存即生效。
	integrationService := service.NewIntegrationService(deploymentSettingsRepo, cfg, githubLogger)
	integrationService.SetPublicURLSource(deploymentSettingsService)
	// 代码仓集成（INT-166）：forgejo CRUD 与连接检测；github-default 只读映射
	// 上一行的部署级 App 身份。
	forgeIntegrationService := service.NewForgeIntegrationService(forgeIntegrationRepo, integrationService, cfg, githubLogger)
	// Manifest 一键创建（INT-129）：state 与换凭证的流程面，落库后的读取/验签
	// 仍走 integrationService。
	manifestService := service.NewGitHubAppManifestService(deploymentSettingsRepo, deploymentSettingsService, cfg, githubLogger)
	memberService := service.NewMemberService(userRepo, inviteRepo, appLogger.With(slog.String("module", "member")))
	dashboardService := service.NewDashboardService(dashboardRepo)
	// 代码仓集成（INT-166）：项目仓库 URL 的非 github 主机按 base_url 匹配
	// forgejo 集成并派生 code_integration_id 绑定。
	projectService := service.NewProjectService(projectRepo, versionRepo, issueSyncStateRepo, githubTokenRepo, fileStorage, cfg, githubLogger, forgeIntegrationRepo)
	versionService := service.NewVersionService(versionRepo, projectRepo, githubTokenRepo, fileStorage, cfg, githubLogger)
	issueService := service.NewIssueService(issueRepo, issueGitHubMetaRepo, issueCommentRepo, issueTimelineRepo, issueInternalMetaRepo, issueShipHookService, issueChecklistRepo, issueSyncStateRepo, issueAssetRepo, issueDraftAssetRepo, projectRepo, githubTokenRepo, userRepo, githubRepoLabelRepo, issueReadStateRepo, recommendationRepo, issuePullRequestRepo, issueAttachmentRepo, fileStorage, cfg, githubLogger, screenshotAnnotationRepo)
	issueCollabService := service.NewIssueCollabService(issueCollabRepo, issueRepo, projectRepo, userRepo)
	recommendationService := service.NewIssueRecommendationService(recommendationRepo, issueRepo, issueInternalMetaRepo, projectRepo)
	logService := service.NewLogService(logRepo, projectRepo)
	documentService := service.NewDocumentService(documentRepo, projectRepo)
	artifactService := service.NewArtifactService(artifactRepo, versionRepo, projectRepo, fileStorage)
	issueAttachmentService := service.NewIssueAttachmentService(issueAttachmentRepo, issueRepo, projectRepo, fileStorage, cfg, githubLogger)
	screenshotService := service.NewScreenshotService(screenshotRepo, projectRepo, fileStorage, cfg)
	screenshotAnnotationService := service.NewScreenshotAnnotationService(screenshotAnnotationRepo, screenshotRepo, issueRepo, projectRepo, fileStorage)
	shipService := service.NewShipService(versionRepo, projectRepo, githubTokenRepo, artifactRepo, issueRepo, issueShipHookRepo, issueService, fileStorage, cfg, githubLogger)
	// 两个下载面各管各的：mediaProxyService 是 /api/github/media-proxy 用的
	// GitHub 域名白名单面；reviewMediaService 是 PR 审查截图下载面，放行
	// 任意公网 https 图片、靠拨号时校验解析 IP 防 SSRF，不与代理共用白名单。
	mediaProxyService := githubmedia.NewProxyService(cfg.Upload.StoragePath)
	// 截图下载面的凭证闸门要知道本部署的 GitHub 实例（GHE 拉私有附件）：只对
	// 同一实例（host[:port]）的附件路径带 App 安装令牌重试，公共 GitHub 部署
	// 下即 github.com。
	reviewMediaService := githubmedia.NewReviewMediaService(cfg.Upload.StoragePath,
		githubmedia.GitHubWebHost(cfg.GitHubApp.APIBaseURL), cfg.ReviewMedia.TrustedPrivateHosts,
		func() []string {
			// allow_private_network 的 Forgejo 集成主机随名单动态放行：自建
			// 实例常在内网，API 已获准私网拨号，截图面不能把同一主机的附件
			// 全拦成 ErrBlockedUpstreamAddress。名单外主机仍走 IP 校验。
			rows, lerr := forgeIntegrationRepo.List()
			if lerr != nil {
				return nil
			}
			var hosts []string
			for i := range rows {
				row := &rows[i]
				if !row.AllowPrivateNetwork {
					continue
				}
				for _, raw := range []string{row.BaseURL, row.APIBaseURL} {
					if u, perr := url.Parse(strings.TrimSpace(raw)); perr == nil && u.Hostname() != "" {
						hosts = append(hosts, u.Hostname())
					}
				}
			}
			return hosts
		})
	// shell 订阅 hub（INT-116）：每环境常驻活跃/归档两条 WS，列表与运行态读取
	// 从内存回答；环境重配/删除时由环境服务清 entry。这里注册停机钩子：当前
	// main 进程没有优雅停机路径、defer 通常不会执行（进程被信号终止时由 OS
	// 回收连接），留着是为了任何未来新增的停机/测试路径能统一收敛。
	shellHub := service.NewT3ShellHub(t3Logger)
	defer shellHub.Close()
	t3EnvironmentService := service.NewT3EnvironmentService(t3EnvironmentRepo, cfg, t3Logger, shellHub)
	t3ThreadsService := service.NewT3ThreadsService(t3EnvironmentRepo, cfg, t3Logger, shellHub)
	// 环境常驻订阅预热（INT-132）：Ensure 不等待同步（订阅在后台建立），这里
	// 同步跑完只是一次列表读取加每环境一次内存准入；库迁移（runBootMigrations）
	// 早已完成，读到的就是落库的全部环境。
	t3EnvironmentService.PrewarmShellSubscriptions(context.Background())
	t3BindingService := service.NewT3BindingService(t3ThreadsService, projectRepo, projectT3BindingRepo)
	notificationSink := service.NewNotificationSink(db)
	notificationService := service.NewNotificationService(notificationRepo)

	// 内置机器人（INT-101）：管理、主线循环、后台执行与 SSE。
	botService := service.NewBotService(db, cfg, zapLogger.With(zap.String("module", "bot")), projectService, issueService, t3ThreadsService, t3BindingService)
	// AI 提供商（INT-133）：按用户隔离的 provider 配置与内置模型目录。
	aiProviderService := service.NewAIProviderService(db, cfg, zapLogger.With(zap.String("module", "bot")))
	aiProviderHandler := handler.NewAIProviderHandler(aiProviderService)

	// GitHub App（INT-93；INT-129 起部署级身份）：debug 自测 fake 仍完全走
	// config（恒返回 fake 的解析器）；其余一律经 githubapp.Clients 现取部署
	// 生效配置（部署设置行 > config.yaml > 未配置），集成页保存后立即生效，
	// 无需重启。未配置时 PR 代发端点报「GitHub App 未配置」，webhook 端点
	// 密钥为空 503。
	var ghPR service.PRGitHubClients
	var ghFake *githubapp.Fake
	if cfg.GitHubApp.Fake && cfg.Server.Mode == "debug" {
		ghFake = githubapp.NewFake()
		ghPR = githubapp.StaticClients{Client: ghFake}
		zapLogger.Info("GitHub App fake 模式（debug 自测）")
	} else {
		clients := githubapp.NewClients(func(ctx context.Context) (githubapp.RuntimeConfig, bool, error) {
			appCfg, ok, err := integrationService.Current(ctx)
			if err != nil || !ok {
				return githubapp.RuntimeConfig{}, false, err
			}
			return githubapp.RuntimeConfig{AppID: appCfg.AppID, PrivateKeyPEM: appCfg.PrivateKeyPEM}, true, nil
		})
		// 本地端到端可把 GitHub API 指向 mock（github_app.api_base_url）。
		clients.SetAPIBase(cfg.GitHubApp.APIBaseURL)
		ghPR = clients
	}

	// 订阅套餐额度（INT-109）：凭据加密落库 + 定时抓取；quota.enabled=false 时
	// 接口仍可用（手动刷新正常），只是没有后台轮。
	quotaService := service.NewQuotaService(quotaAccountRepo, cfg, quotaLogger, nil)
	quotaScheduler := service.NewQuotaScheduler(quotaService, quotaAccountRepo, cfg, quotaLogger)
	// HTTP 侧新建/换凭据/手动刷新写入更早的 next_retry_at 时，叫醒可能在睡长定时器的调度器。
	quotaService.SetKick(quotaScheduler.Kick)

	// PR 审查（INT-135）：webhook 幂等入口 + 审查调度循环（防抖/排队/派发/
	// 超时/同步/工作区清理）。ghPR 是 App 访问面，App 身份与 public_url
	// 分别取部署集成与部署设置服务；T3 线程/工作区与截图缓存复用现有服务。
	// 截图下载的令牌来源（INT-167）：按运行/设置的集成身份解析审查客户端，
	// 再向支持凭证的实现取 bearer——GitHub 是安装令牌，Forgejo 是机器人 PAT
	// （经 forgeSource 现取，review_assets 只对 GitHub 实例的附件路径使用）；
	// fake/不支持凭证的实现保持匿名（返回空串）。
	forgeSource := service.NewForgeClientSource(forgeIntegrationRepo, cfg, reviewLogger)
	reviewClients := forge.NewReviewClients(ghPR, forgeSource)
	mediaBearer := func(ctx context.Context, integrationID string, installationID int64) (string, error) {
		client, err := reviewClients.ClientFor(ctx, integrationID, "", installationID)
		if err != nil {
			return "", err
		}
		source, ok := client.(forge.BearerSource)
		if !ok {
			return "", nil
		}
		return source.Bearer(ctx)
	}
	reviewService := service.NewReviewService(db, projectRepo, webhookDeliveryRepo, notificationSink,
		ghPR, integrationService, deploymentSettingsService, t3ThreadsService, reviewMediaService, mediaBearer,
		forgeIntegrationRepo, cfg, reviewLogger)
	// 删除项目会 CASCADE 掉审查运行行，先让审查服务尽力停线程/归档/回收工作区。
	projectService.SetReviewCleaner(reviewService)
	reviewService.Start()

	// 初始化 Handler
	authHandler := handler.NewAuthHandler(authService, fileStorage, cfg)
	aiHandler := handler.NewAIHandler(aiService)
	issuePromptHandler := handler.NewIssuePromptHandler(issuePromptService)
	apiKeyHandler := handler.NewApiKeyHandler(apiKeyService)
	githubTokenHandler := handler.NewGitHubTokenHandler(githubTokenService)
	dashboardHandler := handler.NewDashboardHandler(dashboardService)
	projectHandler := handler.NewProjectHandler(projectService)
	versionHandler := handler.NewVersionHandler(versionService, shipService)
	issueHandler := handler.NewIssueHandler(issueService, issueShipHookService, issueCollabService)
	issueCollabHandler := handler.NewIssueCollabHandler(issueCollabService)
	recommendationHandler := handler.NewIssueRecommendationHandler(recommendationService)
	logHandler := handler.NewLogHandler(logService)
	documentHandler := handler.NewDocumentHandler(documentService)
	artifactHandler := handler.NewArtifactHandler(artifactService)
	issueAttachmentHandler := handler.NewIssueAttachmentHandler(issueAttachmentService, cfg)
	screenshotHandler := handler.NewScreenshotHandler(screenshotService, cfg)
	screenshotAnnotationHandler := handler.NewScreenshotAnnotationHandler(screenshotAnnotationService)
	mediaProxyHandler := handler.NewGitHubMediaProxyHandler(mediaProxyService)
	t3EnvironmentHandler := handler.NewT3EnvironmentHandler(t3EnvironmentService, t3ThreadsService)
	t3BindingHandler := handler.NewT3BindingHandler(t3BindingService)
	notificationHandler := handler.NewNotificationHandler(notificationService)
	reviewHandler := handler.NewReviewHandler(reviewService)
	reviewAgentHandler := handler.NewReviewAgentHandler(reviewService)
	botHandler := handler.NewBotHandler(botService)
	selfLogHandler := handler.NewSelfLogHandler(service.NewSelfLogService(selfLogStore))
	// webhook 密钥来源：fake 自测路径完全走 config；其余每次请求现取部署唯一
	// 密钥（部署设置行 > config 兜底，行内为空仍 503），集成页保存后立即生效。
	var webhookSecrets handler.WebhookSecretSource = integrationService
	if ghFake != nil {
		webhookSecrets = handler.StaticWebhookSecret(cfg.GitHubApp.WebhookSecret)
	}
	githubWebhookHandler := handler.NewGitHubWebhookHandler(reviewService, webhookSecrets, ghFake)
	// Forgejo webhook（INT-167 任务 3）：按集成行的 webhook secret 验签，同一
	// 凭证来源现取（forgeSource），集成密钥保存后立即生效。
	forgeWebhookHandler := handler.NewForgeWebhookHandler(reviewService, forgeSource)
	quotaHandler := handler.NewQuotaHandler(quotaService)
	deploymentGitHubAppHandler := handler.NewDeploymentGitHubAppHandler(integrationService, manifestService)
	memberHandler := handler.NewMemberHandler(memberService)
	deploymentSettingsHandler := handler.NewDeploymentSettingsHandler(deploymentSettingsService)
	forgeIntegrationHandler := handler.NewForgeIntegrationHandler(forgeIntegrationService)

	cleanAuthArtifacts := func() {
		if err := jwtBlacklistRepo.CleanExpired(); err != nil {
			zapLogger.Error("清理 JWT 黑名单失败", zap.Error(err))
		}
		if err := refreshTokenRepo.CleanExpired(); err != nil {
			zapLogger.Error("清理 Refresh Token 失败", zap.Error(err))
		}
	}
	cleanAuthArtifacts()
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			cleanAuthArtifacts()
		}
	}()

	if err := issueService.CleanupExpiredPendingIssueAssets(); err != nil {
		zapLogger.Warn("清理过期问题图片失败", zap.Error(err))
	}
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			if err := issueService.CleanupExpiredPendingIssueAssets(); err != nil {
				zapLogger.Warn("清理过期问题图片失败", zap.Error(err))
			}
		}
	}()

	if cfg.Issues.AutoSyncEnabled {
		interval := time.Duration(cfg.Issues.AutoSyncIntervalMinutes) * time.Minute
		if interval <= 0 {
			interval = 15 * time.Minute
		}

		zapLogger.Info("issue auto sync enabled",
			zap.Duration("interval", interval),
			zap.Bool("run_on_startup", cfg.Issues.AutoSyncOnStartup),
		)

		go func() {
			ticker := time.NewTicker(interval)
			defer ticker.Stop()

			if cfg.Issues.AutoSyncOnStartup {
				issueService.SyncAllProjectsIncremental(context.Background())
			}
			for range ticker.C {
				issueService.SyncAllProjectsIncremental(context.Background())
			}
		}()
	}

	if cfg.Quota.Enabled {
		quotaScheduler.Start()
	}

	// 机器人（INT-101）：启动恢复在所有 worker 之前；恢复完成后才启动
	// runtime 与后台 worker。
	if err := botService.RecoverOnStartup(); err != nil {
		fatal("机器人启动恢复失败", err)
	}
	botService.Start()

	// 设置 Gin：请求日志中间件自带 recover 与 5xx 堆栈采集，替代 gin.Recovery。
	gin.SetMode(cfg.Server.Mode)
	r := gin.New()
	r.Use(middleware.RequestLog(appLogger))
	r.Use(middleware.CORS())
	r.MaxMultipartMemory = uploadMultipartMemoryLimit(cfg.Upload.MaxFileSize)

	// 注册路由
	router.Setup(r, cfg, authHandler, aiHandler, issuePromptHandler, apiKeyHandler, githubTokenHandler, dashboardHandler, projectHandler, versionHandler, issueHandler, issueCollabHandler, recommendationHandler, logHandler, documentHandler, artifactHandler, issueAttachmentHandler, screenshotHandler, screenshotAnnotationHandler, mediaProxyHandler, t3EnvironmentHandler, t3BindingHandler, notificationHandler, reviewHandler, reviewAgentHandler, reviewService, botHandler, aiProviderHandler, selfLogHandler, githubWebhookHandler, forgeWebhookHandler, quotaHandler, deploymentGitHubAppHandler, memberHandler, deploymentSettingsHandler, forgeIntegrationHandler, authService, apiKeyRepo)

	// 启动服务。ReadHeaderTimeout 挡慢速请求头长期占连接；不设 Read/WriteTimeout
	// 保住上传/下载/媒体代理等长传输；IdleTimeout 回收 keep-alive 空闲连接。
	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	appLogger.Info("服务启动", slog.String("addr", addr), slog.Bool(selflog.AttrSystemScope, true))
	srv := &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		fatal("服务启动失败", err)
	}
}

// dryRun 报告是否带 --dry-run 启动标志：只执行迁移预演打印统计后退出。
func dryRun() bool {
	for _, arg := range os.Args[1:] {
		if arg == "--dry-run" {
			return true
		}
	}
	return false
}

// runBootMigrations 是服务启动的全部库迁移入口，按固定顺序执行：
//
//  1. --dry-run：在任何写入之前只读统计截图库折叠迁移将做什么，打印后直接返回；
//     旧日志表删除、外键修复、AutoMigrate 一律不碰，「未做任何修改」成立。
//  2. 正常启动：先修存量库外键（AutoMigrate 隐式重建表会撞悬空行），再给存量
//     多版本库补可空文件列（有数据行上加 NOT NULL 列会被 SQLite 拒绝），
//     然后 AutoMigrate、截图库折叠迁移（先于变体唯一索引：索引要建在
//     platform 迁移完成的数据上）、索引登记与各数据回填。
//
// 测试用它从旧表结构走完整升级路径。
func runBootMigrations(db *gorm.DB, fileStorage storage.Storage, logger *zap.Logger, dryRun bool, fkBackupPath string, encryptionKey []byte) error {
	if dryRun {
		stats, err := collectScreenshotMigrationStats(db)
		if err != nil {
			return err
		}
		logger.Info("截图库迁移 dry-run",
			zap.Int64("screens", stats.Screens),
			zap.Int64("versions_total", stats.VersionTotal),
			zap.Int64("versions_to_delete", stats.ObsoleteVersions))
		return nil
	}

	dropLegacyLogTables(db, logger)

	// 修正存量库上与模型不符的外键。必须先于 AutoMigrate：
	// 缺失外键交给 AutoMigrate 会隐式整表重建，悬空行还在就会直接失败。
	if err := repairLegacyForeignKeys(db, logger, fkBackupPath); err != nil {
		return fmt.Errorf("外键约束迁移失败: %w", err)
	}

	// 截图库折叠的前置：给存量 screenshot_screens 补可空文件列，避免
	// AutoMigrate 在有数据行的旧表上加 NOT NULL 列被拒绝。
	if err := prepareScreenshotFoldMigration(db); err != nil {
		return fmt.Errorf("截图文件列准备失败: %w", err)
	}

	// 通用自动化与旧 PR 评审账整表删除（INT-135）：拍板不迁移旧数据，
	// 必须在 AutoMigrate 之前执行，避免旧模型外键/索引干扰新表。
	if err := migrateReviewTables(db, logger); err != nil {
		return fmt.Errorf("审查表切换迁移失败: %w", err)
	}

	// 审查表加 integration_id 维度（INT-167）：键表整表重建（SQLite 不能 ALTER
	// 主键）、非键表加列回填，必须在 AutoMigrate 之前——重建出的就是新结构，
	// AutoMigrate 只补普通索引。
	if err := migrateReviewIntegrationIDs(db, logger); err != nil {
		return fmt.Errorf("审查表 integration_id 迁移失败: %w", err)
	}

	// 用户级 integrations 表删除（INT-129 B 轮）：GitHub App 身份改为部署级
	// （deployment_settings），用户级配置已确认无存量数据，整表丢弃。必须先于
	// AutoMigrate——模型已从清单移除，留着只会带着悬空外键与旧索引。
	if err := migrateDropIntegrations(db, logger); err != nil {
		return fmt.Errorf("integrations 表删除迁移失败: %w", err)
	}

	// 自动迁移
	if err := db.AutoMigrate(autoMigrateModels...); err != nil {
		return err
	}

	// 截图库折叠迁移（INT-99）：把多版本库收敛为单文件截图。
	if err := migrateScreenshots(db, fileStorage, logger); err != nil {
		return fmt.Errorf("截图库迁移失败: %w", err)
	}

	// AI 提供商聚合（INT-133）：存量 bots 的地址+密钥去重建 provider、回填
	// provider_id 并删除旧列。去重按解密后的明文密钥聚合（密文因随机 nonce
	// 各不相同），需要加密配置。必须在 AutoMigrate 之后（依赖 ai_providers 与
	// provider_id 列）、EnsureBotSchema 之前（索引建在新形状上）。
	if err := migrateBotAIProviders(db, logger, encryptionKey); err != nil {
		return fmt.Errorf("AI 提供商迁移失败: %w", err)
	}

	// 机器人围栏 nonce（INT-136）：只为空值行生成一次，已有的 nonce 永不
	// 改写。必须在 AutoMigrate 之后（依赖 bots.untrusted_nonce 列）。
	if err := migrateBotUntrustedNonce(db, logger); err != nil {
		return fmt.Errorf("机器人围栏 nonce 迁移失败: %w", err)
	}

	// 存量定时任务复核提示（INT-136）：确认机制上线前创建的任务其指令从未
	// 经确认卡复核，升级后向所在机器人主线发一次性提示（只提示不阻断）。
	// 必须在 AutoMigrate 之后（依赖 bot_schedule_tasks.confirmed 列）。
	if err := migrateLegacyScheduleNotice(db, logger); err != nil {
		return fmt.Errorf("存量定时任务复核提示迁移失败: %w", err)
	}

	// 代码集成 host_key 回填（INT-166）：必须在 createBootIndexes 之前——
	// 同归一主机的部分唯一索引建在回填完的数据上。
	if err := migrateForgeHostKeys(db, logger); err != nil {
		return fmt.Errorf("代码集成 host_key 回填失败: %w", err)
	}

	// api_base_url 列语义改「空 = 推导」：存量展开值一次性回收（幂等）。
	if err := migrateForgeAPIDerived(db, logger); err != nil {
		return fmt.Errorf("代码集成 api_base_url 迁移失败: %w", err)
	}

	// projects.github_host 回填（INT-166）：NULL 存量行的旧渲染语义是公共
	// github.com，收成 'github.com' 保住 GHE 部署下的 code_repo_url 链接。
	if err := migrateProjectGithubHosts(db, logger); err != nil {
		return fmt.Errorf("项目 github_host 迁移失败: %w", err)
	}

	if err := createBootIndexes(db); err != nil {
		return fmt.Errorf("创建手工索引失败: %w", err)
	}

	// 用户角色（INT-129）：AutoMigrate 补齐 users.role 后，存量库可能一个
	// admin 都没有——把最早创建的用户提为 admin（幂等）。
	if err := migrateUserRoles(db, logger); err != nil {
		return fmt.Errorf("用户角色迁移失败: %w", err)
	}

	if err := dropLegacyCollabTables(db, logger); err != nil {
		return fmt.Errorf("协作区数据迁移失败: %w", err)
	}

	if err := backfillIssueSourceModel(db); err != nil {
		return fmt.Errorf("问题数据迁移失败: %w", err)
	}
	if err := backfillIssueAssetStatusModel(db); err != nil {
		return fmt.Errorf("问题资源数据迁移失败: %w", err)
	}
	if err := backfillIssueReadStates(db, logger); err != nil {
		return fmt.Errorf("已读水位数据迁移失败: %w", err)
	}

	// 项目 GitHub Token 密文迁移进用户级 Token 库并切换到引用列（INT-84）。
	// 依赖 AutoMigrate 已建好 user_github_tokens 与 projects 引用列；
	// 解密失败等错误直接启动失败，宁可起不来也不带错跑。
	if err := migrateProjectGitHubTokens(db, encryptionKey, logger); err != nil {
		return fmt.Errorf("项目 GitHub Token 迁移失败: %w", err)
	}

	// 项目代码仓库列（INT-140）：AutoMigrate 给存量库加出的两列是 NULL，
	// 回填空串以保持「空 = 与公开仓库相同」的不变量（幂等）。
	if err := backfillProjectCodeRepo(db, logger); err != nil {
		return fmt.Errorf("项目代码仓库列回填失败: %w", err)
	}
	return nil
}

// createBootIndexes 登记全部手工索引：复合/部分/表达式索引 AutoMigrate 建不了。
// 普通索引创建失败按幂等忽略（历史口径），唯一约束索引失败返回 error 让启动
// 失败——索引没建上而服务照常跑，查重就只剩应用层兜底。
func createBootIndexes(db *gorm.DB) error {
	// 创建唯一索引
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_projects_user_name ON projects(user_id, name)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_versions_project_version ON versions(project_id, version_number)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issues_project_sequence ON issues(project_id, sequence_number)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_github_meta_project_github_issue ON issue_github_meta(project_id, github_issue_id)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_github_meta_issue_id ON issue_github_meta(issue_id)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_comments_issue_github_comment ON issue_comments(issue_id, github_comment_id)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_comments_issue_idempotency_key ON issue_comments(issue_id, idempotency_key) WHERE idempotency_key <> ''")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_timeline_issue_event_key ON issue_timeline_events(issue_id, event_key)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_pull_requests_issue_pr ON issue_pull_requests(issue_id, provider, repo_full_name, number)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_log_runs_project_last_entry ON log_runs(project_id, last_entry_at DESC)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_log_entries_run_timestamp ON log_entries(log_run_id, timestamp ASC)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_documents_project_parent ON documents(project_id, parent_id)")
	// webhook 反查关联项目（owner/repo 大小写不敏感）。
	db.Exec("CREATE INDEX IF NOT EXISTS idx_projects_github_repo ON projects(github_owner, github_repo)")
	// 截图变体唯一性（INT-99）：(project_id, screen_key, platform, locale, theme)，
	// NULL 与 NULL 视为相等（COALESCE 表达式索引）。旧 (project_id, screen_key)
	// 唯一索引必须 DROP：多维度变体在同一 screen_key 下合法。
	db.Exec("DROP INDEX IF EXISTS idx_screenshot_screens_project_key")
	db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_screenshot_screens_variant
	ON screenshot_screens(project_id, screen_key, COALESCE(platform,''), COALESCE(locale,''), COALESCE(theme,''))`)
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_t3_environments_user_name ON t3_environments(user_id, name)")

	// PR 审查（INT-135）：轮次/SHA 反查、用户时间序、在途截止、组合幂等唯一。
	db.Exec("CREATE INDEX IF NOT EXISTS idx_review_runs_pr_round ON review_runs(project_id, repo_full_name, pr_number, round_number)")
	// head 反查的主读路径已带上 project_id（账本按项目分区），索引前缀对齐；
	// 旧的无 project_id 同名索引 DROP 重建。
	db.Exec("DROP INDEX IF EXISTS idx_review_runs_pr_head")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_review_runs_pr_head ON review_runs(project_id, repo_full_name, pr_number, head_sha)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_review_runs_user_created ON review_runs(user_id, created_at DESC)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_review_runs_active_deadline ON review_runs(deadline_at) WHERE status IN ('dispatched', 'running')")
	// 组合幂等唯一索引列定义历经三次演变（补 round_number、补 environment_id、
	// 补 integration_id），只能 DROP 重建：同模型不同环境是合法的不同组合，
	// 同名仓库跨集成也是不同组合，旧键缺列会把它们静默吞掉（DoNothing 当幂等命中）。
	db.Exec("DROP INDEX IF EXISTS idx_review_runs_combination")
	db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_review_runs_combination
	ON review_runs(project_id, integration_id, repo_full_name, pr_number, head_sha, round_number, kind, environment_id, instance_id, model, model_options_json)`)
	db.Exec("CREATE INDEX IF NOT EXISTS idx_review_pull_requests_project_updated ON review_pull_requests(project_id, updated_at DESC)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_review_pull_requests_sync_pending ON review_pull_requests(sync_pending) WHERE sync_pending = 1")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_review_debounces_fire ON review_debounces(fire_at)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_project_review_settings_user ON project_review_settings(user_id)")
	// 同一集成内的同一仓库只允许一个项目开启审查（开轮/同步按集成+仓库反查
	// 唯一项目的前提）：设置保存时先在事务内检查并返回 40922，这条部分唯一
	// 索引兜并发竞态。integration_id 键首（INT-167）：同名仓库在 GitHub 与
	// Forgejo 两个集成下可各自被一个项目启用。列定义历经一次演变（旧定义不
	// 带 integration_id），参照 idx_forge_integrations_host_key 的 sqlite_master
	// 存稿比对模式：与期望定义不等才 DROP 重建，当期定义跳过。
	const reviewRepoIndexSQL = "CREATE UNIQUE INDEX IF NOT EXISTS idx_project_review_settings_repo ON project_review_settings(integration_id, repo_full_name) WHERE enabled = 1"
	var reviewRepoIdxDef []struct {
		SQL string `gorm:"column:sql"`
	}
	if err := db.Raw("SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_project_review_settings_repo'").Scan(&reviewRepoIdxDef).Error; err != nil {
		return fmt.Errorf("读取 idx_project_review_settings_repo 定义失败: %w", err)
	}
	if len(reviewRepoIdxDef) > 0 && reviewRepoIdxDef[0].SQL != strings.Replace(reviewRepoIndexSQL, " IF NOT EXISTS", "", 1) {
		if err := db.Exec("DROP INDEX IF EXISTS idx_project_review_settings_repo").Error; err != nil {
			return fmt.Errorf("重建 idx_project_review_settings_repo 失败: %w", err)
		}
	}
	if err := db.Exec(reviewRepoIndexSQL).Error; err != nil {
		return fmt.Errorf("创建 idx_project_review_settings_repo 失败: %w", err)
	}
	// 同归一主机只允许一条 forgejo 集成（INT-166）：保存时 service 层先查重
	// 并返回带集成名的 40001（idx_forge_integrations_host_key 的归一键在 SQL
	// 里算不出来），这条部分唯一索引兜查重通过后同主机被并发抢注的窗口；
	// host_key 为空串的存量脏行不参与唯一约束。谓词含 kind='forgejo'——与
	// FindByHostKey/ensureHostAvailable 的过滤同口径，未来其它 kind 落表不
	// 会被索引拦截却查重看不见。建不上（如存量数据已有同 host_key 冲突）
	// 必须启动失败——带缺口跑等于并发抢注窗口裸奔。IF NOT EXISTS 不更新
	// 存量谓词：比对 sqlite_master 存稿（入库文本即建索引原文去掉
	// IF NOT EXISTS），与期望定义不等才 DROP 重建——子串匹配会在谓词
	// 再次演进时漏重建，无条件重建又留下约束失效窗口。
	const hostKeyIndexSQL = "CREATE UNIQUE INDEX IF NOT EXISTS idx_forge_integrations_host_key ON forge_integrations(host_key) WHERE host_key <> '' AND kind = 'forgejo'"
	var idxDef []struct {
		SQL string `gorm:"column:sql"`
	}
	if err := db.Raw("SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_forge_integrations_host_key'").Scan(&idxDef).Error; err != nil {
		return fmt.Errorf("读取 idx_forge_integrations_host_key 定义失败: %w", err)
	}
	if len(idxDef) > 0 && idxDef[0].SQL != strings.Replace(hostKeyIndexSQL, " IF NOT EXISTS", "", 1) {
		if err := db.Exec("DROP INDEX IF EXISTS idx_forge_integrations_host_key").Error; err != nil {
			return fmt.Errorf("重建 idx_forge_integrations_host_key 失败: %w", err)
		}
	}
	if err := db.Exec(hostKeyIndexSQL).Error; err != nil {
		return fmt.Errorf("创建 idx_forge_integrations_host_key 失败: %w", err)
	}
	// 截图源地址唯一索引由 model.ReviewAsset 的 uniqueIndex 标签随 AutoMigrate
	// 建出（idx_review_assets_source，integration_id 键首）；按 (repo, pr, head)
	// 的查询走它的第二段前缀。
	db.Exec("DROP INDEX IF EXISTS idx_review_assets_pr_head")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_notifications_user_dedup ON notifications(user_id, dedup_key)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_notifications_user_updated ON notifications(user_id, updated_at DESC, id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_notifications_unread ON notifications(user_id, updated_at DESC) WHERE read_at IS NULL")
	return nil
}

// selfLogMinLevel 把配置中的级别名转为 slog 级别；未知取值按 INFO。
func selfLogMinLevel(name string) slog.Level {
	switch name {
	case "debug", "DEBUG":
		return slog.LevelDebug
	case "warn", "WARN":
		return slog.LevelWarn
	case "error", "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func backfillIssueSourceModel(db *gorm.DB) error {
	hasIssuesGitHubID, err := hasSQLiteColumn(db, "issues", "github_issue_id")
	if err != nil {
		return err
	}

	if hasIssuesGitHubID {
		if err := db.Exec(`
			UPDATE issues
			SET source = 'github'
			WHERE github_issue_id IS NOT NULL
			  AND github_issue_id > 0
		`).Error; err != nil {
			return err
		}

		if err := db.Exec(`
			UPDATE issues
			SET sequence_number = number
			WHERE (sequence_number IS NULL OR sequence_number = 0)
			  AND number IS NOT NULL
			  AND number > 0
		`).Error; err != nil {
			return err
		}

		if err := db.Exec(`
			INSERT INTO issue_github_meta (
				issue_id,
				project_id,
				github_issue_id,
				github_node_id,
				number,
				html_url,
				author_association,
				assignees_json,
				labels_json,
				milestone_json,
				reactions_json,
				comments_count,
				locked,
				active_lock_reason,
				synced_at,
				raw_json
			)
			SELECT
				issues.id,
				issues.project_id,
				issues.github_issue_id,
				issues.github_node_id,
				issues.number,
				issues.html_url,
				issues.author_association,
				issues.assignees_json,
				issues.labels_json,
				issues.milestone_json,
				issues.reactions_json,
				issues.comments_count,
				issues.locked,
				issues.active_lock_reason,
				issues.synced_at,
				issues.raw_json
			FROM issues
			WHERE issues.github_issue_id IS NOT NULL
			  AND issues.github_issue_id > 0
			  AND NOT EXISTS (
				SELECT 1
				FROM issue_github_meta meta
				WHERE meta.issue_id = issues.id
			  )
		`).Error; err != nil {
			return err
		}
	}

	// 对已经迁移过、但 source 被旧默认值写成 internal 的记录做幂等修复。
	if err := db.Exec(`
		UPDATE issues
		SET source = 'github'
		WHERE EXISTS (
			SELECT 1
			FROM issue_github_meta meta
			WHERE meta.issue_id = issues.id
		)
	`).Error; err != nil {
		return err
	}

	if hasCommentSource, err := hasSQLiteColumn(db, "issue_comments", "source"); err != nil {
		return err
	} else if hasCommentSource {
		if err := db.Exec(`
			UPDATE issue_comments
			SET source = 'github'
			WHERE source IS NULL OR source = ''
		`).Error; err != nil {
			return err
		}
	}

	if err := dropLegacyIssueColumns(db); err != nil {
		return err
	}

	return nil
}

func backfillIssueAssetStatusModel(db *gorm.DB) error {
	hasStatus, err := hasSQLiteColumn(db, "issue_assets", "status")
	if err != nil || !hasStatus {
		return err
	}

	return db.Exec(`
		UPDATE issue_assets
		SET status = 'attached'
		WHERE status IS NULL OR status = ''
	`).Error
}

func backfillIssueReadStates(db *gorm.DB, logger *zap.Logger) error {
	var catchup model.IssueReadCatchup
	if err := db.First(&catchup).Error; err == nil {
		return nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	now := time.Now().UTC()

	return db.Transaction(func(tx *gorm.DB) error {
		var rows []struct {
			UserID    string
			IssueID   string
			Watermark string
		}
		if err := tx.Raw(`
			SELECT p.user_id AS user_id,
			       i.id AS issue_id,
			       COALESCE(
			         (SELECT MAX(c.created_at) FROM issue_comments c WHERE c.issue_id = i.id),
			         i.updated_at
			       ) AS watermark
			FROM issues i
			JOIN projects p ON p.id = i.project_id
			WHERE i.source = ?
		`, string(model.IssueSourceGitHub)).Scan(&rows).Error; err != nil {
			return err
		}

		txReadRepo := repository.NewIssueReadStateRepository(tx)
		for _, row := range rows {
			watermark, err := parseSQLiteTimestamp(row.Watermark)
			if err != nil {
				return fmt.Errorf("parse watermark for issue %s: %w", row.IssueID, err)
			}
			if err := txReadRepo.Advance(row.UserID, row.IssueID, watermark); err != nil {
				return err
			}
		}

		if err := tx.Create(&model.IssueReadCatchup{ID: 1, CompletedAt: now}).Error; err != nil {
			return err
		}

		logger.Info("已为存量 GitHub Issue 写入已读水位")
		return nil
	})
}

func dropLegacyLogTables(db *gorm.DB, logger *zap.Logger) {
	hasBatchID, err := hasSQLiteColumn(db, "log_entries", "batch_id")
	if err != nil {
		logger.Warn("检查遗留日志表失败", zap.Error(err))
		return
	}
	if hasBatchID {
		if err := db.Exec("DROP TABLE IF EXISTS log_entries").Error; err != nil {
			logger.Warn("删除遗留日志表失败", zap.String("table", "log_entries"), zap.Error(err))
		}
	}

	if err := db.Exec("DROP TABLE IF EXISTS log_batches").Error; err != nil {
		logger.Warn("删除遗留日志表失败", zap.String("table", "log_batches"), zap.Error(err))
	}
}

func dropLegacyCollabTables(db *gorm.DB, logger *zap.Logger) error {
	legacyTables := []string{
		"issue_collab_notes",
		"issue_collab_questions",
		"issue_collab_suggestions",
		"issue_collab_plans",
		"issue_collab_reviews",
	}
	for _, table := range legacyTables {
		if err := db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s", table)).Error; err != nil {
			logger.Warn("删除遗留协作区表失败", zap.String("table", table), zap.Error(err))
		}
	}

	hasSummaries, err := sqliteTableExists(db, "issue_collab_summaries")
	if err != nil {
		return fmt.Errorf("检查 issue_collab_summaries 表: %w", err)
	}
	if hasSummaries {
		if err := db.Exec(`
			INSERT OR IGNORE INTO issue_collab_documents (
				issue_id, kind, body, author_user_id, author_kind, created_at, updated_at
			)
			SELECT issue_id, 'summary', body, author_user_id, author_kind, created_at, updated_at
			FROM issue_collab_summaries
		`).Error; err != nil {
			return fmt.Errorf("迁移 issue_collab_summaries 数据: %w", err)
		}
		if err := db.Exec("DROP TABLE IF EXISTS issue_collab_summaries").Error; err != nil {
			return fmt.Errorf("删除 issue_collab_summaries 表: %w", err)
		}
	}

	hasConsensus, err := sqliteTableExists(db, "issue_collab_consensus")
	if err != nil {
		return fmt.Errorf("检查 issue_collab_consensus 表: %w", err)
	}
	if hasConsensus {
		if err := db.Exec(`
			INSERT OR IGNORE INTO issue_collab_documents (
				issue_id, kind, body, author_user_id, author_kind, created_at, updated_at
			)
			SELECT issue_id, 'consensus', body, author_user_id, author_kind, created_at, updated_at
			FROM issue_collab_consensus
		`).Error; err != nil {
			return fmt.Errorf("迁移 issue_collab_consensus 数据: %w", err)
		}
		if err := db.Exec("DROP TABLE IF EXISTS issue_collab_consensus").Error; err != nil {
			return fmt.Errorf("删除 issue_collab_consensus 表: %w", err)
		}
	}

	return nil
}

func sqliteTableExists(db *gorm.DB, tableName string) (bool, error) {
	var name string
	err := db.Raw(`
		SELECT name FROM sqlite_master
		WHERE type = 'table' AND name = ?
	`, tableName).Scan(&name).Error
	if err != nil {
		return false, err
	}
	return name == tableName, nil
}

func uploadMultipartMemoryLimit(maxFileSize int64) int64 {
	const defaultLimit = int64(32 << 20)
	if maxFileSize <= 0 || maxFileSize > defaultLimit {
		return defaultLimit
	}
	return maxFileSize
}

func dropLegacyIssueColumns(db *gorm.DB) error {
	// 旧版本把 GitHub issue 元数据直接放在 issues 表里。
	// 当前模型已经拆到 issue_github_meta，需要删除遗留列，避免旧的 NOT NULL 约束继续影响内部 issue 创建。
	legacyColumns := []string{
		"github_issue_id",
		"github_node_id",
		"number",
		"html_url",
		"author_association",
		"assignees_json",
		"labels_json",
		"milestone_json",
		"reactions_json",
		"comments_count",
		"locked",
		"active_lock_reason",
		"synced_at",
		"raw_json",
	}

	for _, column := range legacyColumns {
		hasColumn, err := hasSQLiteColumn(db, "issues", column)
		if err != nil {
			return err
		}
		if !hasColumn {
			continue
		}
		if err := dropSQLiteIndexesByColumn(db, "issues", column); err != nil {
			return err
		}
		if err := db.Exec(fmt.Sprintf("ALTER TABLE %q DROP COLUMN %q", "issues", column)).Error; err != nil {
			return err
		}
	}

	return nil
}

func dropSQLiteIndexesByColumn(db *gorm.DB, tableName, columnName string) error {
	var indexes []sqliteIndexInfo
	if err := db.Raw(fmt.Sprintf("PRAGMA index_list(%q)", tableName)).Scan(&indexes).Error; err != nil {
		return err
	}

	for _, index := range indexes {
		if index.Name == "" || strings.HasPrefix(index.Name, "sqlite_autoindex") {
			continue
		}

		var columns []sqliteColumnInfo
		if err := db.Raw(fmt.Sprintf("PRAGMA index_info(%q)", index.Name)).Scan(&columns).Error; err != nil {
			return err
		}

		matchesColumn := false
		for _, column := range columns {
			if column.Name == columnName {
				matchesColumn = true
				break
			}
		}
		if !matchesColumn {
			continue
		}

		if err := db.Exec(fmt.Sprintf("DROP INDEX IF EXISTS %q", index.Name)).Error; err != nil {
			return err
		}
	}

	return nil
}

func hasSQLiteColumn(db *gorm.DB, tableName, columnName string) (bool, error) {
	var columns []sqliteColumnInfo
	if err := db.Raw(fmt.Sprintf("PRAGMA table_info(%s)", tableName)).Scan(&columns).Error; err != nil {
		return false, err
	}
	for _, column := range columns {
		if column.Name == columnName {
			return true, nil
		}
	}
	return false, nil
}

// SQLite 把 DATETIME 存成 TEXT。Raw Scan 进 time.Time 会被 glebarez 拒绝。
func parseSQLiteTimestamp(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05Z07:00",
		"2006-01-02 15:04:05-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unparsed timestamp %q", raw)
}
