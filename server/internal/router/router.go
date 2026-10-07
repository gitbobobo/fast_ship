package router

import (
	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/handler"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/godbobo/fast_ship/server/internal/service"
)

func Setup(
	r *gin.Engine,
	cfg *config.Config,
	authHandler *handler.AuthHandler,
	aiHandler *handler.AIHandler,
	issuePromptHandler *handler.IssuePromptHandler,
	apiKeyHandler *handler.ApiKeyHandler,
	dashboardHandler *handler.DashboardHandler,
	projectHandler *handler.ProjectHandler,
	versionHandler *handler.VersionHandler,
	issueHandler *handler.IssueHandler,
	issueCollabHandler *handler.IssueCollabHandler,
	recommendationHandler *handler.IssueRecommendationHandler,
	logHandler *handler.LogHandler,
	documentHandler *handler.DocumentHandler,
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

		// JWT 必须 — 全局问题提示词配置
		issuePrompts := api.Group("/issue-prompts", middleware.RequireJWT(cfg, authService))
		{
			issuePrompts.GET("", issuePromptHandler.GetPrompts)
			issuePrompts.PUT("", issuePromptHandler.UpdatePrompts)
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

		// JWT 必须 — Issue 项目级写操作（草稿资产/同步/批量关闭）
		issueWrite := api.Group("/projects/:id/issues", middleware.RequireJWT(cfg, authService))
		{
			issueWrite.POST("/assets", issueHandler.UploadDraftAsset)
			issueWrite.POST("/sync", issueHandler.Sync)
			issueWrite.POST("/batch-close", issueHandler.BatchCloseDone)
		}
		// Issue 编辑类写操作 —— JWT 或 API Key 均可（API Key 用于自动化/Agent 场景）
		api.POST("/projects/:id/issues", middleware.RequireAuth(cfg, apiKeyRepo, authService), issueHandler.Create)
		api.PUT("/issues/:iid", middleware.RequireAuth(cfg, apiKeyRepo, authService), issueHandler.Update)
		api.POST("/issues/:iid/assets", middleware.RequireAuth(cfg, apiKeyRepo, authService), issueHandler.UploadAsset)
		api.PUT("/issues/:iid/internal-meta", middleware.RequireAuth(cfg, apiKeyRepo, authService), issueHandler.UpdateInternalMeta)
		// 批量工作流更新 —— 全局路径，代理持跨项目 issue UUID 一次写入多条；静态段优先于 :iid 匹配。
		api.PUT("/issues/internal-meta", middleware.RequireAuth(cfg, apiKeyRepo, authService), issueHandler.BatchUpdateInternalMeta)
		api.PUT("/issues/:iid/checklist", middleware.RequireAuth(cfg, apiKeyRepo, authService), issueHandler.ReplaceChecklist)
		api.PUT("/issues/:iid/ship-hook", middleware.RequireJWT(cfg, authService), issueHandler.UpsertShipHook)
		api.DELETE("/issues/:iid/ship-hook", middleware.RequireJWT(cfg, authService), issueHandler.DeleteShipHook)
		// 推荐任务 —— PUT 仅 API Key（Agent），JWT 调用在 handler 内返回 403(40303)；DELETE 两类凭证均可（延后项仅 JWT 可删，API Key 返回 40911）。
		api.PUT("/issues/:iid/recommendation", middleware.RequireAuth(cfg, apiKeyRepo, authService), recommendationHandler.Upsert)
		api.DELETE("/issues/:iid/recommendation", middleware.RequireAuth(cfg, apiKeyRepo, authService), recommendationHandler.Delete)
		// 延后/恢复推荐 —— 仅 JWT（Web 看板操作，仿 ship-hook 挂法）
		api.PUT("/issues/:iid/recommendation/defer", middleware.RequireJWT(cfg, authService), recommendationHandler.Defer)
		api.DELETE("/issues/:iid/recommendation/defer", middleware.RequireJWT(cfg, authService), recommendationHandler.Restore)
		// checklist-suggestions 对 API Key 开放：供 Agent 自动化生成清单建议；其余 AI 端点（generate-title / settings）仍限 JWT。
		api.POST("/issues/:iid/checklist-suggestions", middleware.RequireAuth(cfg, apiKeyRepo, authService), aiHandler.SuggestIssueChecklist)
		// JWT 必须 — Issue 评论
		api.POST("/issues/:iid/comments", middleware.RequireJWT(cfg, authService), issueHandler.CreateComment)
		// JWT 必须 — 标记 Issue 评论已读（GET 与 API Key 不消未读）
		api.POST("/issues/:iid/read", middleware.RequireJWT(cfg, authService), issueHandler.MarkRead)

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
		// Issue ↔ PR 关联 —— JWT / API Key 均可；attach 输入为 PR URL，PR 元信息由服务端拉取。
		api.POST("/issues/:iid/pull-requests", middleware.RequireAuth(cfg, apiKeyRepo, authService), issueHandler.AttachPullRequest)
		api.POST("/issues/:iid/pull-requests/sync", middleware.RequireAuth(cfg, apiKeyRepo, authService), issueHandler.SyncPullRequests)
		api.DELETE("/issues/:iid/pull-requests/:id", middleware.RequireAuth(cfg, apiKeyRepo, authService), issueHandler.DetachPullRequest)
		// 推荐任务列表 —— 供 Agent 与看板「推荐」弹框拉取；project_id 为空时返回当前用户全部项目。
		api.GET("/recommendations", middleware.RequireAuth(cfg, apiKeyRepo, authService), recommendationHandler.List)

		// 人机协作区 —— GET/DELETE 两类凭证均可；PUT 写端点仅 API Key（Agent），JWT 调用在 handler 内返回 403(40303)。
		issueCollab := api.Group("/issues/:iid/collab", middleware.RequireAuth(cfg, apiKeyRepo, authService))
		{
			issueCollab.GET("", issueCollabHandler.GetArea)
			issueCollab.DELETE("", issueCollabHandler.ClearArea)
			issueCollab.PUT("/consensus", issueCollabHandler.UpsertForKind(model.CollabDocumentKindConsensus))
			issueCollab.DELETE("/consensus", issueCollabHandler.DeleteForKind(model.CollabDocumentKindConsensus))
			issueCollab.PUT("/summary", issueCollabHandler.UpsertForKind(model.CollabDocumentKindSummary))
			issueCollab.DELETE("/summary", issueCollabHandler.DeleteForKind(model.CollabDocumentKindSummary))
		}

		// JWT / API Key 均可 — 安装包操作
		api.POST("/versions/:vid/artifacts", middleware.RequireAuth(cfg, apiKeyRepo, authService), artifactHandler.Upload)
		api.DELETE("/artifacts/:aid", middleware.RequireAuth(cfg, apiKeyRepo, authService), artifactHandler.Delete)
		api.GET("/artifacts/:aid/download", middleware.RequireAuthWithQueryToken(cfg, apiKeyRepo, authService, "token"), artifactHandler.Download)

		// JWT / API Key 均可 — 日志
		api.POST("/projects/:id/logs", middleware.RequireAuth(cfg, apiKeyRepo, authService), logHandler.Upload)
		api.GET("/projects/:id/logs", middleware.RequireAuth(cfg, apiKeyRepo, authService), logHandler.ListEntries)
		api.GET("/projects/:id/log-runs", middleware.RequireAuth(cfg, apiKeyRepo, authService), logHandler.ListRuns)
		api.GET("/projects/:id/log-runs/:run_id", middleware.RequireAuth(cfg, apiKeyRepo, authService), logHandler.GetRun)
		api.DELETE("/projects/:id/log-runs/:run_id", middleware.RequireAuth(cfg, apiKeyRepo, authService), logHandler.DeleteRun)
		api.DELETE("/projects/:id/logs", middleware.RequireAuth(cfg, apiKeyRepo, authService), logHandler.DeleteByProject)

		// JWT / API Key 均可 — 文档
		api.GET("/projects/:id/documents", middleware.RequireAuth(cfg, apiKeyRepo, authService), documentHandler.List)
		api.POST("/projects/:id/documents", middleware.RequireAuth(cfg, apiKeyRepo, authService), documentHandler.Create)
		api.GET("/documents/:doc_id", middleware.RequireAuth(cfg, apiKeyRepo, authService), documentHandler.Get)
		api.PUT("/documents/:doc_id", middleware.RequireAuth(cfg, apiKeyRepo, authService), documentHandler.Update)
		api.DELETE("/documents/:doc_id", middleware.RequireAuth(cfg, apiKeyRepo, authService), documentHandler.Delete)
	}

	setupWebRoutes(r, cfg.Server.WebDistDir)
}
