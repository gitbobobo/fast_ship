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
	aiHandler *handler.AIHandler,
	apiKeyHandler *handler.ApiKeyHandler,
	dashboardHandler *handler.DashboardHandler,
	projectHandler *handler.ProjectHandler,
	versionHandler *handler.VersionHandler,
	issueHandler *handler.IssueHandler,
	issueCollabHandler *handler.IssueCollabHandler,
	artifactHandler *handler.ArtifactHandler,
	mediaProxyHandler *handler.GitHubMediaProxyHandler,
	authService *service.AuthService,
	apiKeyRepo *repository.ApiKeyRepository,
) {
	api := r.Group("/api")
	{
		api.GET("/github/media-proxy", middleware.RequireAuthWithQueryToken(cfg, apiKeyRepo, authService, "token"), mediaProxyHandler.Proxy)
		api.HEAD("/github/media-proxy", middleware.RequireAuthWithQueryToken(cfg, apiKeyRepo, authService, "token"), mediaProxyHandler.Proxy)
		api.GET("/issues/assets/:aid/content", middleware.RequireAuthWithQueryToken(cfg, apiKeyRepo, authService, "token"), issueHandler.AssetContent)
		api.HEAD("/issues/assets/:aid/content", middleware.RequireAuthWithQueryToken(cfg, apiKeyRepo, authService, "token"), issueHandler.AssetContent)

		// 公开接口
		auth := api.Group("/auth")
		{
			auth.POST("/register", authHandler.Register)
			auth.POST("/login", authHandler.Login)
			auth.POST("/refresh", authHandler.Refresh)
		}

		// JWT 必须 — 用户信息
		authed := api.Group("", middleware.RequireAuth(cfg, apiKeyRepo, authService))
		{
			authed.POST("/auth/logout", authHandler.Logout)
			authed.GET("/auth/me", authHandler.GetMe)
			authed.PUT("/auth/me", authHandler.UpdateMe)
			authed.PUT("/auth/password", authHandler.UpdatePassword)
			authed.POST("/auth/avatar", authHandler.UploadAvatar)
		}

		// 头像访问（支持 query token 和公开访问）
		api.GET("/avatars/:uid/:filename", middleware.RequireAuthWithQueryToken(cfg, apiKeyRepo, authService, "token"), authHandler.GetAvatar)

		ai := api.Group("/ai", middleware.RequireJWT(cfg, authService))
		{
			ai.GET("/settings", aiHandler.GetSettings)
			ai.PUT("/settings", aiHandler.UpdateSettings)
			ai.POST("/generate-title", aiHandler.GenerateTitle)
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
			projectRead.GET("/:id/branches", projectHandler.GetBranches)
		}

		api.GET("/dashboard/overview", middleware.RequireAuth(cfg, apiKeyRepo, authService), dashboardHandler.Overview)

		// JWT 必须 — 版本写操作（创建）
		versionWrite := api.Group("/projects/:id/versions", middleware.RequireJWT(cfg, authService))
		{
			versionWrite.POST("", versionHandler.Create)
		}

		issueWrite := api.Group("/projects/:id/issues", middleware.RequireJWT(cfg, authService))
		{
			issueWrite.POST("/assets", issueHandler.UploadDraftAsset)
			issueWrite.POST("/sync", issueHandler.Sync)
			issueWrite.POST("/batch-close", issueHandler.BatchCloseDone)
		}
		api.POST("/projects/:id/issues", middleware.RequireAuth(cfg, apiKeyRepo, authService), issueHandler.Create)
		api.PUT("/issues/:iid", middleware.RequireAuth(cfg, apiKeyRepo, authService), issueHandler.Update)
		// Issue 编辑类写操作 —— JWT 或 API Key 均可（API Key 用于自动化/Agent 场景）
		api.POST("/issues/:iid/assets", middleware.RequireAuth(cfg, apiKeyRepo, authService), issueHandler.UploadAsset)
		api.POST("/issues/:iid/comments", middleware.RequireAuth(cfg, apiKeyRepo, authService), issueHandler.CreateComment)
		api.PUT("/issues/:iid/internal-meta", middleware.RequireAuth(cfg, apiKeyRepo, authService), issueHandler.UpdateInternalMeta)
		api.PUT("/issues/:iid/checklist", middleware.RequireAuth(cfg, apiKeyRepo, authService), issueHandler.ReplaceChecklist)
		// checklist-suggestions 对 API Key 开放：供 Agent 自动化生成清单建议；其余 AI 端点（generate-title / settings）仍限 JWT。
		api.POST("/issues/:iid/checklist-suggestions", middleware.RequireAuth(cfg, apiKeyRepo, authService), aiHandler.SuggestIssueChecklist)

		// JWT 必须 — 版本删除和发货
		api.DELETE("/versions/:vid", middleware.RequireJWT(cfg, authService), versionHandler.Delete)
		api.GET("/versions/:vid/ship-check", middleware.RequireJWT(cfg, authService), versionHandler.ShipCheck)
		api.POST("/versions/:vid/ship", middleware.RequireJWT(cfg, authService), versionHandler.Ship)

		// JWT / API Key 均可 — 版本读写
		api.GET("/projects/:id/versions", middleware.RequireAuth(cfg, apiKeyRepo, authService), versionHandler.List)
		api.GET("/versions/:vid", middleware.RequireAuth(cfg, apiKeyRepo, authService), versionHandler.Get)
		api.PUT("/versions/:vid", middleware.RequireAuth(cfg, apiKeyRepo, authService), versionHandler.Update)

		api.GET("/projects/:id/issues", middleware.RequireAuth(cfg, apiKeyRepo, authService), issueHandler.List)
		api.GET("/projects/:id/issues/count", middleware.RequireAuth(cfg, apiKeyRepo, authService), issueHandler.Count)
		api.GET("/projects/:id/issues/filter-options", middleware.RequireAuth(cfg, apiKeyRepo, authService), issueHandler.FilterOptions)
		api.GET("/projects/:id/issues/repo-labels", middleware.RequireAuth(cfg, apiKeyRepo, authService), issueHandler.RepoLabels)
		api.GET("/issues/:iid", middleware.RequireAuth(cfg, apiKeyRepo, authService), issueHandler.Get)
		api.GET("/issues/:iid/comments", middleware.RequireAuth(cfg, apiKeyRepo, authService), issueHandler.ListComments)
		api.GET("/issues/:iid/timeline", middleware.RequireAuth(cfg, apiKeyRepo, authService), issueHandler.ListTimeline)

		// 人机协作区 —— GET/DELETE 两类凭证均可；PUT 写端点仅 API Key（Agent），JWT 调用在 handler 内返回 403(40303)。
		issueCollab := api.Group("/issues/:iid/collab", middleware.RequireAuth(cfg, apiKeyRepo, authService))
		{
			issueCollab.GET("", issueCollabHandler.GetArea)
			issueCollab.DELETE("", issueCollabHandler.ClearArea)
			issueCollab.PUT("/suggestions", issueCollabHandler.ReplaceSuggestions)
			issueCollab.DELETE("/suggestions", issueCollabHandler.ClearSuggestions)
			issueCollab.PUT("/plan", issueCollabHandler.UpsertPlan)
			issueCollab.DELETE("/plan", issueCollabHandler.DeletePlan)
			issueCollab.PUT("/review", issueCollabHandler.UpsertReview)
			issueCollab.DELETE("/review", issueCollabHandler.DeleteReview)
			issueCollab.PUT("/summary", issueCollabHandler.UpsertSummary)
			issueCollab.DELETE("/summary", issueCollabHandler.DeleteSummary)
		}

		// JWT / API Key 均可 — 安装包操作
		api.POST("/versions/:vid/artifacts", middleware.RequireAuth(cfg, apiKeyRepo, authService), artifactHandler.Upload)
		api.DELETE("/artifacts/:aid", middleware.RequireAuth(cfg, apiKeyRepo, authService), artifactHandler.Delete)
		api.GET("/artifacts/:aid/download", middleware.RequireAuthWithQueryToken(cfg, apiKeyRepo, authService, "token"), artifactHandler.Download)
	}

	setupWebRoutes(r, cfg.Server.WebDistDir)
}
