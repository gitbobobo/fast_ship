package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/githubapi"
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
	&model.Automation{},
	&model.AutomationRun{},
	&model.AutomationRunToken{},
	&model.Notification{},
	&model.GitHubWebhookDelivery{},
	&model.GitHubPRDebounce{},
	&model.GitHubPRReview{},
	&model.GitHubPRSeenSHA{},
	&model.QuotaAccount{},
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
	automationRepo := repository.NewAutomationRepository(db)
	automationRunRepo := repository.NewAutomationRunRepository(db)
	automationRunTokenRepo := repository.NewAutomationRunTokenRepository(db)
	notificationRepo := repository.NewNotificationRepository(db)
	quotaAccountRepo := repository.NewQuotaAccountRepository(db)

	// 业务库就绪后再注入归属解析器：注入前的入库日志只能是启动类（显式 system_scope）。
	selfLogPipeline.SetOwnerResolver(service.NewSelfLogOwnerResolver(db))

	// 模块化日志（INT-94）：按模块打上 module 字段，便于自身日志按模块检索。
	githubLogger := zapLogger.With(zap.String("module", "github"))
	t3Logger := zapLogger.With(zap.String("module", "t3"))
	automationLogger := zapLogger.With(zap.String("module", "automation"))
	quotaLogger := zapLogger.With(zap.String("module", "quota"))

	// 初始化 Service
	authService := service.NewAuthService(userRepo, jwtBlacklistRepo, refreshTokenRepo, cfg, appLogger.With(slog.String("module", "auth")))
	aiService := service.NewAIService(userAISettingRepo, issueRepo, issueCommentRepo, projectRepo, cfg, zapLogger.With(zap.String("module", "ai")))
	issuePromptService := service.NewIssuePromptService(userIssuePromptRepo, t3EnvironmentRepo)
	apiKeyService := service.NewApiKeyService(apiKeyRepo, appLogger.With(slog.String("module", "api_key")))
	githubTokenService := service.NewGitHubTokenService(githubTokenRepo, cfg, githubLogger)
	dashboardService := service.NewDashboardService(dashboardRepo)
	projectService := service.NewProjectService(projectRepo, versionRepo, issueSyncStateRepo, githubTokenRepo, fileStorage, cfg, githubLogger)
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
	mediaProxyService := githubmedia.NewProxyService(cfg.Upload.StoragePath)
	t3EnvironmentService := service.NewT3EnvironmentService(t3EnvironmentRepo, cfg, t3Logger)
	t3ThreadsService := service.NewT3ThreadsService(t3EnvironmentRepo, cfg, t3Logger)
	t3BindingService := service.NewT3BindingService(t3ThreadsService, projectRepo, projectT3BindingRepo)
	notificationSink := service.NewNotificationSink(db)
	notificationService := service.NewNotificationService(notificationRepo)
	automationService := service.NewAutomationService(db, automationRepo, automationRunRepo, t3EnvironmentRepo, projectRepo, issueRepo, notificationSink, cfg, automationLogger)

	// 内置机器人（INT-101）：管理、主线循环、后台执行与 SSE。
	botService := service.NewBotService(db, cfg, zapLogger.With(zap.String("module", "bot")), projectService, issueService, t3ThreadsService, t3BindingService)

	// GitHub App（INT-93）：真实安装令牌客户端或 debug 自测 fake；凭据缺失时
	// PR 代发端点报错，webhook 端点因验签密钥为空一律 503。
	var ghPR githubapi.GitHubClient
	var ghFake *githubapp.Fake
	if cfg.GitHubApp.Fake && cfg.Server.Mode == "debug" {
		ghFake = githubapp.NewFake()
		ghPR = ghFake
		zapLogger.Info("GitHub App fake 模式（debug 自测）")
	} else if cfg.GitHubApp.AppID != "" && cfg.GitHubApp.PrivateKeyPath != "" {
		creds := &githubapp.AppCredentials{
			AppID:          cfg.GitHubApp.AppID,
			PrivateKeyPath: cfg.GitHubApp.PrivateKeyPath,
		}
		ghPR = githubapp.NewClient(githubapp.NewInstallationTokenStore(creds))
	}
	automationAgentService := service.NewAutomationAgentService(db, automationRunRepo, automationRunTokenRepo, t3ThreadsService, notificationSink, automationLogger)
	automationAgentService.SetPRGitHub(ghPR)
	automationScheduler := service.NewAutomationScheduler(db, automationRepo, automationRunRepo, automationRunTokenRepo, t3ThreadsService, notificationSink, issueRepo, cfg, automationLogger)
	automationAgentService.SetKick(automationScheduler.Kick)

	// 订阅套餐额度（INT-109）：凭据加密落库 + 定时抓取；quota.enabled=false 时
	// 接口仍可用（手动刷新正常），只是没有后台轮。
	quotaService := service.NewQuotaService(quotaAccountRepo, cfg, quotaLogger, nil)
	quotaScheduler := service.NewQuotaScheduler(quotaService, quotaAccountRepo, cfg, quotaLogger)
	// HTTP 侧新建/换凭据/手动刷新写入更早的 next_retry_at 时，叫醒可能在睡长定时器的调度器。
	quotaService.SetKick(quotaScheduler.Kick)

	// PR 评审协调面（INT-93）：webhook 事务、防抖开轮、排队提升、评论/check 同步。
	prReviewService := service.NewPRReviewService(db,
		automationRepo, automationRunRepo, projectRepo,
		repository.NewGitHubWebhookDeliveryRepository(db),
		repository.NewGitHubPRDebounceRepository(db),
		repository.NewGitHubPRReviewRepository(db),
		ghPR, t3ThreadsService, notificationSink, cfg, zapLogger)
	automationScheduler.UsePRReview(prReviewService)
	automationAgentService.SetPRReview(prReviewService)

	// Issue 写路径接入事件出口：事务内匹配自动化并预写运行（INT-90）。
	issueService.SetIssueEventSink(automationService)

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
	automationHandler := handler.NewAutomationHandler(automationService)
	notificationHandler := handler.NewNotificationHandler(notificationService)
	automationAgentHandler := handler.NewAutomationAgentHandler(automationAgentService)
	botHandler := handler.NewBotHandler(botService)
	selfLogHandler := handler.NewSelfLogHandler(service.NewSelfLogService(selfLogStore))
	githubWebhookHandler := handler.NewGitHubWebhookHandler(prReviewService, cfg.GitHubApp.WebhookSecret, ghFake)
	quotaHandler := handler.NewQuotaHandler(quotaService)

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

	// 自动化协调循环（INT-90）：到期 cron、待认领运行、超时与后置统一由它推进。
	automationScheduler.Start()

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
	router.Setup(r, cfg, authHandler, aiHandler, issuePromptHandler, apiKeyHandler, githubTokenHandler, dashboardHandler, projectHandler, versionHandler, issueHandler, issueCollabHandler, recommendationHandler, logHandler, documentHandler, artifactHandler, issueAttachmentHandler, screenshotHandler, screenshotAnnotationHandler, mediaProxyHandler, t3EnvironmentHandler, t3BindingHandler, automationHandler, notificationHandler, automationAgentHandler, botHandler, selfLogHandler, githubWebhookHandler, quotaHandler, authService, apiKeyRepo)

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

	// 自动迁移
	if err := db.AutoMigrate(autoMigrateModels...); err != nil {
		return err
	}

	// 截图库折叠迁移（INT-99）：把多版本库收敛为单文件截图。
	if err := migrateScreenshots(db, fileStorage, logger); err != nil {
		return fmt.Errorf("截图库迁移失败: %w", err)
	}

	createBootIndexes(db)

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
	return nil
}

// createBootIndexes 登记全部手工索引：复合/部分/表达式索引 AutoMigrate 建不了。
func createBootIndexes(db *gorm.DB) {
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

	// 自动化（INT-90）：复合/部分索引 AutoMigrate 建不了，全部手工登记。
	db.Exec("CREATE INDEX IF NOT EXISTS idx_automations_enabled_trigger_next ON automations(enabled, trigger_type, next_run_at)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_automations_user_trigger_project ON automations(user_id, trigger_type, issue_project_id)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_automation_runs_automation_trigger ON automation_runs(automation_id, trigger_key)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_automation_runs_user_triggered ON automation_runs(user_id, triggered_at DESC, id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_automation_runs_automation_triggered ON automation_runs(automation_id, triggered_at DESC, id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_automation_runs_active_deadline ON automation_runs(deadline_at) WHERE status IN ('dispatched', 'running')")
	// 同一自动化至多一个活动运行（未结束）或一个待后置运行——重入/删除约束的
	// 数据库兜底。只约束 cron/issue 触发：GitHub PR 触发（INT-93）一轮多模型
	// 并行 + 排队，允许同一规则多行未结束。必须 DROP 再 CREATE：
	// CREATE IF NOT EXISTS 不会替换已有定义（INT-90 存量库升级）。
	db.Exec("DROP INDEX IF EXISTS idx_automation_runs_active_once")
	db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_automation_runs_active_once
	ON automation_runs(automation_id)
	WHERE trigger_type IN ('cron','issue_created','issue_status_changed','issue_label_added')
	  AND (finished_at IS NULL OR post_state = 'pending')`)
	// PR 审查：按 repo+pr+sha 找同一轮运行（SHA 变化取消、轮次聚合）。
	db.Exec("CREATE INDEX IF NOT EXISTS idx_automation_runs_pr_head ON automation_runs(repo_full_name, pr_number, head_sha)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_notifications_user_dedup ON notifications(user_id, dedup_key)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_notifications_user_updated ON notifications(user_id, updated_at DESC, id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_notifications_unread ON notifications(user_id, updated_at DESC) WHERE read_at IS NULL")
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
