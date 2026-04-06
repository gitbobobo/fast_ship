package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/handler"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/storage"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/godbobo/fast_ship/server/internal/router"
	"github.com/godbobo/fast_ship/server/internal/service"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

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
	if cfg.Server.Mode == "debug" {
		gormLogger = logger.Default.LogMode(logger.Info)
	}

	db, err := gorm.Open(sqlite.Open(cfg.Database.Path+"?_pragma=foreign_keys(1)"), &gorm.Config{
		Logger: gormLogger,
	})
	if err != nil {
		log.Fatalf("连接数据库失败: %v", err)
	}

	// 自动迁移
	if err := db.AutoMigrate(
		&model.User{},
		&model.ApiKey{},
		&model.Project{},
		&model.Version{},
		&model.Artifact{},
		&model.JWTBlacklist{},
	); err != nil {
		log.Fatalf("数据库迁移失败: %v", err)
	}

	// 创建唯一索引
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_projects_user_name ON projects(user_id, name)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_versions_project_version ON versions(project_id, version_number)")

	// 初始化存储
	fileStorage := storage.NewLocalStorage(cfg.Upload.StoragePath)

	// 初始化 Repository
	userRepo := repository.NewUserRepository(db)
	apiKeyRepo := repository.NewApiKeyRepository(db)
	projectRepo := repository.NewProjectRepository(db)
	versionRepo := repository.NewVersionRepository(db)
	artifactRepo := repository.NewArtifactRepository(db)
	jwtBlacklistRepo := repository.NewJWTBlacklistRepository(db)

	// 初始化 Service
	authService := service.NewAuthService(userRepo, jwtBlacklistRepo, cfg)
	apiKeyService := service.NewApiKeyService(apiKeyRepo)
	projectService := service.NewProjectService(projectRepo, cfg)
	versionService := service.NewVersionService(versionRepo, projectRepo, fileStorage)
	artifactService := service.NewArtifactService(artifactRepo, versionRepo, projectRepo, fileStorage)
	shipService := service.NewShipService(versionRepo, projectRepo, artifactRepo, fileStorage, cfg, zapLogger)

	// 初始化 Handler
	authHandler := handler.NewAuthHandler(authService)
	apiKeyHandler := handler.NewApiKeyHandler(apiKeyService)
	projectHandler := handler.NewProjectHandler(projectService)
	versionHandler := handler.NewVersionHandler(versionService, shipService)
	artifactHandler := handler.NewArtifactHandler(artifactService)

	// 启动 JWT 黑名单清理任务
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			if err := jwtBlacklistRepo.CleanExpired(); err != nil {
				zapLogger.Error("清理 JWT 黑名单失败", zap.Error(err))
			}
		}
	}()

	// 设置 Gin
	gin.SetMode(cfg.Server.Mode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(middleware.CORS())
	r.MaxMultipartMemory = cfg.Upload.MaxFileSize

	// 注册路由
	router.Setup(r, cfg, authHandler, apiKeyHandler, projectHandler, versionHandler, artifactHandler, authService, apiKeyRepo)

	// 启动服务
	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	zapLogger.Info("服务启动", zap.String("addr", addr))
	if err := r.Run(addr); err != nil {
		log.Fatalf("服务启动失败: %v", err)
	}
}
