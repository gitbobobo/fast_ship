package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/handler"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubmedia"
	"github.com/godbobo/fast_ship/server/internal/pkg/storage"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/godbobo/fast_ship/server/internal/router"
	"github.com/godbobo/fast_ship/server/internal/service"
	"go.uber.org/zap"
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

func main() {
	// 加载配置
	cfgPath := "configs/config.yaml"
	if envPath := os.Getenv("CONFIG_PATH"); envPath != "" {
		cfgPath = envPath
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	// 初始化日志
	var zapLogger *zap.Logger
	if cfg.Server.Mode == "debug" {
		zapLogger, _ = zap.NewDevelopment()
	} else {
		zapLogger, _ = zap.NewProduction()
	}
	defer zapLogger.Sync()

	// 确保数据目录存在
	dbDir := filepath.Dir(cfg.Database.Path)
	if err := os.MkdirAll(dbDir, 0750); err != nil {
		log.Fatalf("创建数据目录失败: %v", err)
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
		log.Fatalf("连接数据库失败: %v", err)
	}

	// SQLite 文件锁是连接级竞争：单连接串行化避免跨连接 BUSY；
	// 空闲超时让毒化连接（如 COMMIT BUSY 后悬空的事务）被关闭回滚，避免永久持锁；
	// 持续流量下空闲计时会被反复重置，再用生命周期兜底强制回收。
	sqlDB, err := db.DB()
	if err != nil {
		log.Fatalf("获取数据库连接失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetConnMaxIdleTime(5 * time.Minute)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)

	dropLegacyLogTables(db, zapLogger)

	// 自动迁移
	if err := db.AutoMigrate(
		&model.User{},
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
		&model.Artifact{},
		&model.JWTBlacklist{},
		&model.RefreshToken{},
		&model.GitHubRepoLabel{},
		&model.IssueCollabDocument{},
		&model.IssueRecommendation{},
		&model.RecommendationDependency{},
		&model.LogRun{},
		&model.LogRunChunk{},
		&model.LogEntry{},
		&model.Document{},
	); err != nil {
		log.Fatalf("数据库迁移失败: %v", err)
	}

	// 创建唯一索引
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_projects_user_name ON projects(user_id, name)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_versions_project_version ON versions(project_id, version_number)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issues_project_sequence ON issues(project_id, sequence_number)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_github_meta_project_github_issue ON issue_github_meta(project_id, github_issue_id)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_github_meta_issue_id ON issue_github_meta(issue_id)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_comments_issue_github_comment ON issue_comments(issue_id, github_comment_id)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_comments_issue_idempotency_key ON issue_comments(issue_id, idempotency_key) WHERE idempotency_key <> ''")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_timeline_issue_event_key ON issue_timeline_events(issue_id, event_key)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_log_runs_project_last_entry ON log_runs(project_id, last_entry_at DESC)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_log_entries_run_timestamp ON log_entries(log_run_id, timestamp ASC)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_documents_project_parent ON documents(project_id, parent_id)")

	if err := dropLegacyCollabTables(db, zapLogger); err != nil {
		log.Fatalf("协作区数据迁移失败: %v", err)
	}

	if err := backfillIssueSourceModel(db); err != nil {
		log.Fatalf("问题数据迁移失败: %v", err)
	}
	if err := backfillIssueAssetStatusModel(db); err != nil {
		log.Fatalf("问题资源数据迁移失败: %v", err)
	}
	if err := backfillIssueReadStates(db, zapLogger); err != nil {
		log.Fatalf("已读水位数据迁移失败: %v", err)
	}

	// 初始化存储
	fileStorage := storage.NewLocalStorage(cfg.Upload.StoragePath)

	// 初始化 Repository
	userRepo := repository.NewUserRepository(db)
	userAISettingRepo := repository.NewUserAISettingRepository(db)
	userIssuePromptRepo := repository.NewUserIssuePromptSettingRepository(db)
	apiKeyRepo := repository.NewApiKeyRepository(db)
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
	recommendationRepo := repository.NewIssueRecommendationRepository(db)
	logRepo := repository.NewLogRepository(db)
	documentRepo := repository.NewDocumentRepository(db)
	artifactRepo := repository.NewArtifactRepository(db)
	jwtBlacklistRepo := repository.NewJWTBlacklistRepository(db)
	refreshTokenRepo := repository.NewRefreshTokenRepository(db)

	// 初始化 Service
	authService := service.NewAuthService(userRepo, jwtBlacklistRepo, refreshTokenRepo, cfg)
	aiService := service.NewAIService(userAISettingRepo, issueRepo, issueCommentRepo, projectRepo, cfg, zapLogger)
	issuePromptService := service.NewIssuePromptService(userIssuePromptRepo)
	apiKeyService := service.NewApiKeyService(apiKeyRepo)
	dashboardService := service.NewDashboardService(dashboardRepo)
	projectService := service.NewProjectService(projectRepo, versionRepo, issueSyncStateRepo, fileStorage, cfg)
	versionService := service.NewVersionService(versionRepo, projectRepo, fileStorage, cfg)
	issueService := service.NewIssueService(issueRepo, issueGitHubMetaRepo, issueCommentRepo, issueTimelineRepo, issueInternalMetaRepo, issueShipHookService, issueChecklistRepo, issueSyncStateRepo, issueAssetRepo, issueDraftAssetRepo, projectRepo, userRepo, githubRepoLabelRepo, issueReadStateRepo, recommendationRepo, fileStorage, cfg, zapLogger)
	issueCollabService := service.NewIssueCollabService(issueCollabRepo, issueRepo, projectRepo, userRepo)
	recommendationService := service.NewIssueRecommendationService(recommendationRepo, issueRepo, issueInternalMetaRepo, projectRepo)
	logService := service.NewLogService(logRepo, projectRepo)
	documentService := service.NewDocumentService(documentRepo, projectRepo)
	artifactService := service.NewArtifactService(artifactRepo, versionRepo, projectRepo, fileStorage)
	shipService := service.NewShipService(versionRepo, projectRepo, artifactRepo, issueRepo, issueShipHookRepo, issueService, fileStorage, cfg, zapLogger)
	mediaProxyService := githubmedia.NewProxyService(cfg.Upload.StoragePath)

	// 初始化 Handler
	authHandler := handler.NewAuthHandler(authService, fileStorage, cfg)
	aiHandler := handler.NewAIHandler(aiService)
	issuePromptHandler := handler.NewIssuePromptHandler(issuePromptService)
	apiKeyHandler := handler.NewApiKeyHandler(apiKeyService)
	dashboardHandler := handler.NewDashboardHandler(dashboardService)
	projectHandler := handler.NewProjectHandler(projectService)
	versionHandler := handler.NewVersionHandler(versionService, shipService)
	issueHandler := handler.NewIssueHandler(issueService, issueShipHookService, issueCollabService)
	issueCollabHandler := handler.NewIssueCollabHandler(issueCollabService)
	recommendationHandler := handler.NewIssueRecommendationHandler(recommendationService)
	logHandler := handler.NewLogHandler(logService)
	documentHandler := handler.NewDocumentHandler(documentService)
	artifactHandler := handler.NewArtifactHandler(artifactService)
	mediaProxyHandler := handler.NewGitHubMediaProxyHandler(mediaProxyService)

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

	// 设置 Gin
	gin.SetMode(cfg.Server.Mode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(middleware.CORS())
	r.MaxMultipartMemory = uploadMultipartMemoryLimit(cfg.Upload.MaxFileSize)

	// 注册路由
	router.Setup(r, cfg, authHandler, aiHandler, issuePromptHandler, apiKeyHandler, dashboardHandler, projectHandler, versionHandler, issueHandler, issueCollabHandler, recommendationHandler, logHandler, documentHandler, artifactHandler, mediaProxyHandler, authService, apiKeyRepo)

	// 启动服务
	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	zapLogger.Info("服务启动", zap.String("addr", addr))
	if err := r.Run(addr); err != nil {
		log.Fatalf("服务启动失败: %v", err)
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
