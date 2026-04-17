package router

import (
	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/handler"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/godbobo/fast_ship/server/internal/service"
)

func Setup(
	r *gin.Engine,
	cfg *config.Config,
	authHandler *handler.AuthHandler,
	apiKeyHandler *handler.ApiKeyHandler,
	projectHandler *handler.ProjectHandler,
	versionHandler *handler.VersionHandler,
	issueHandler *handler.IssueHandler,
	artifactHandler *handler.ArtifactHandler,
	mediaProxyHandler *handler.GitHubMediaProxyHandler,
	authService *service.AuthService,
	apiKeyRepo *repository.ApiKeyRepository,
) {
	api := r.Group("/api")
	{
		api.GET("/github/media-proxy", middleware.RequireAuthWithQueryToken(cfg, apiKeyRepo, authService, "token"), mediaProxyHandler.Proxy)
		api.HEAD("/github/media-proxy", middleware.RequireAuthWithQueryToken(cfg, apiKeyRepo, authService, "token"), mediaProxyHandler.Proxy)

		// 公开接口
		auth := api.Group("/auth")
		{
			auth.POST("/register", authHandler.Register)
			auth.POST("/login", authHandler.Login)
		}

		// JWT 必须 — 用户信息
		authed := api.Group("", middleware.RequireAuth(cfg, apiKeyRepo, authService))
		{
			authed.POST("/auth/logout", authHandler.Logout)
			authed.GET("/auth/me", authHandler.GetMe)
			authed.PUT("/auth/me", authHandler.UpdateMe)
			authed.PUT("/auth/password", authHandler.UpdatePassword)
		}

		// JWT 必须 — API Key 管理
		apiKeys := api.Group("/api-keys", middleware.RequireJWT(cfg, authService))
		{
			apiKeys.GET("", apiKeyHandler.List)
			apiKeys.POST("", apiKeyHandler.Create)
			apiKeys.DELETE("/:id", apiKeyHandler.Delete)
		}

		// JWT 必须 — 项目写操作
		projectWrite := api.Group("/projects", middleware.RequireJWT(cfg, authService))
		{
			projectWrite.POST("", projectHandler.Create)
			projectWrite.PUT("/:id", projectHandler.Update)
			projectWrite.DELETE("/:id", projectHandler.Delete)
		}

		// JWT / API Key 均可 — 项目读操作
		projectRead := api.Group("/projects", middleware.RequireAuth(cfg, apiKeyRepo, authService))
		{
			projectRead.GET("", projectHandler.List)
			projectRead.GET("/:id", projectHandler.Get)
		}

		// JWT 必须 — 版本写操作（创建）
		versionWrite := api.Group("/projects/:id/versions", middleware.RequireJWT(cfg, authService))
		{
			versionWrite.POST("", versionHandler.Create)
		}

		issueWrite := api.Group("/projects/:id/issues", middleware.RequireJWT(cfg, authService))
		{
			issueWrite.POST("", issueHandler.Create)
			issueWrite.POST("/sync", issueHandler.Sync)
		}
		api.PUT("/issues/:iid", middleware.RequireJWT(cfg, authService), issueHandler.Update)
		api.POST("/issues/:iid/comments", middleware.RequireJWT(cfg, authService), issueHandler.CreateComment)
		api.PUT("/issues/:iid/internal-meta", middleware.RequireJWT(cfg, authService), issueHandler.UpdateInternalMeta)

		// JWT 必须 — 版本删除和发货
		api.DELETE("/versions/:vid", middleware.RequireJWT(cfg, authService), versionHandler.Delete)
		api.GET("/versions/:vid/ship-check", middleware.RequireJWT(cfg, authService), versionHandler.ShipCheck)
		api.POST("/versions/:vid/ship", middleware.RequireJWT(cfg, authService), versionHandler.Ship)

		// JWT / API Key 均可 — 版本读写
		api.GET("/projects/:id/versions", middleware.RequireAuth(cfg, apiKeyRepo, authService), versionHandler.List)
		api.GET("/versions/:vid", middleware.RequireAuth(cfg, apiKeyRepo, authService), versionHandler.Get)
		api.PUT("/versions/:vid", middleware.RequireAuth(cfg, apiKeyRepo, authService), versionHandler.Update)

		api.GET("/projects/:id/issues", middleware.RequireAuth(cfg, apiKeyRepo, authService), issueHandler.List)
		api.GET("/projects/:id/issues/filter-options", middleware.RequireAuth(cfg, apiKeyRepo, authService), issueHandler.FilterOptions)
		api.GET("/issues/:iid", middleware.RequireAuth(cfg, apiKeyRepo, authService), issueHandler.Get)
		api.GET("/issues/:iid/comments", middleware.RequireAuth(cfg, apiKeyRepo, authService), issueHandler.ListComments)
		api.GET("/issues/:iid/timeline", middleware.RequireAuth(cfg, apiKeyRepo, authService), issueHandler.ListTimeline)

		// JWT / API Key 均可 — 安装包操作
		api.POST("/versions/:vid/artifacts", middleware.RequireAuth(cfg, apiKeyRepo, authService), artifactHandler.Upload)
		api.DELETE("/artifacts/:aid", middleware.RequireAuth(cfg, apiKeyRepo, authService), artifactHandler.Delete)
		api.GET("/artifacts/:aid/download", middleware.RequireAuth(cfg, apiKeyRepo, authService), artifactHandler.Download)
	}

	setupWebRoutes(r, cfg.Server.WebDistDir)
}
