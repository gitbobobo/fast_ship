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
	githubTokenHandler *handler.GitHubTokenHandler,
	dashboardHandler *handler.DashboardHandler,
	projectHandler *handler.ProjectHandler,
	versionHandler *handler.VersionHandler,
	issueHandler *handler.IssueHandler,
	issueCollabHandler *handler.IssueCollabHandler,
	recommendationHandler *handler.IssueRecommendationHandler,
	logHandler *handler.LogHandler,
	documentHandler *handler.DocumentHandler,
	artifactHandler *handler.ArtifactHandler,
	issueAttachmentHandler *handler.IssueAttachmentHandler,
	screenshotHandler *handler.ScreenshotHandler,
	screenshotAnnotationHandler *handler.ScreenshotAnnotationHandler,
	mediaProxyHandler *handler.GitHubMediaProxyHandler,
	t3EnvironmentHandler *handler.T3EnvironmentHandler,
	t3BindingHandler *handler.T3BindingHandler,
	notificationHandler *handler.NotificationHandler,
	reviewHandler *handler.ReviewHandler,
	reviewAgentHandler *handler.ReviewAgentHandler,
	reviewResolver service.RunTokenResolver,
	botHandler *handler.BotHandler,
	aiProviderHandler *handler.AIProviderHandler,
	selfLogHandler *handler.SelfLogHandler,
	githubWebhookHandler *handler.GitHubWebhookHandler,
	quotaHandler *handler.QuotaHandler,
	deploymentGitHubAppHandler *handler.DeploymentGitHubAppHandler,
	memberHandler *handler.MemberHandler,
	deploymentSettingsHandler *handler.DeploymentSettingsHandler,
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
			// 邀请注册前校验（INT-129）：公开，注册页提前显示邀请状态。
			// POST + 请求体：token 不落 URL 路径，避免被请求日志记下明文。
			auth.POST("/invites/validate", memberHandler.ValidateInvite)
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

		// JWT 必须 — 用户级 GitHub Token 库
		githubTokens := api.Group("/github-tokens", middleware.RequireJWT(cfg, authService))
		{
			githubTokens.GET("", githubTokenHandler.List)
			githubTokens.POST("", githubTokenHandler.Create)
			githubTokens.PUT("/:id", githubTokenHandler.Update)
			githubTokens.DELETE("/:id", githubTokenHandler.Delete)
			githubTokens.POST("/:id/detect", githubTokenHandler.Detect)
		}

		// JWT 必须 — 订阅套餐额度管理（INT-109）：供应商元数据、账号增删改、
		// 手动刷新与凭据测试仅 JWT；GET /accounts 对 API Key 只读开放（机器人取数通道）。
		quotaJWT := api.Group("/quota", middleware.RequireJWT(cfg, authService))
		{
			quotaJWT.GET("/providers", quotaHandler.ListProviders)
			quotaJWT.POST("/accounts", quotaHandler.CreateAccount)
			quotaJWT.PUT("/accounts/:id", quotaHandler.UpdateAccount)
			quotaJWT.DELETE("/accounts/:id", quotaHandler.DeleteAccount)
			quotaJWT.POST("/accounts/:id/refresh", quotaHandler.RefreshAccount)
			quotaJWT.POST("/test", quotaHandler.Test)
			// 浏览器设备授权（INT-117）：会话内含 deviceSecret，仅 JWT。
			quotaJWT.POST("/device-authorizations", quotaHandler.StartDeviceAuthorization)
			quotaJWT.GET("/device-authorizations/:id", quotaHandler.GetDeviceAuthorization)
			quotaJWT.DELETE("/device-authorizations/:id", quotaHandler.CancelDeviceAuthorization)
		}
		api.GET("/quota/accounts", middleware.RequireAuth(cfg, apiKeyRepo, authService), quotaHandler.ListAccounts)

		// JWT 必须 — 部署级 GitHub App 读取（INT-129）：所有登录用户可读
		// （成员页也显示安装入口）；写入/检测/Manifest 在下方管理员组。
		api.GET("/deployment/github-app", middleware.RequireJWT(cfg, authService), deploymentGitHubAppHandler.Get)

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

		// JWT / API Key 均可 — Issue 附件（与正文图片 assets 相互独立）
		api.POST("/issues/:iid/attachments", middleware.RequireAuth(cfg, apiKeyRepo, authService), issueAttachmentHandler.Upload)
		api.DELETE("/attachments/:aid", middleware.RequireAuth(cfg, apiKeyRepo, authService), issueAttachmentHandler.Delete)
		api.GET("/attachments/:aid/download", middleware.RequireAuthWithQueryToken(cfg, apiKeyRepo, authService, "token"), issueAttachmentHandler.Download)

		// 截图库 —— 上传、读与删除 JWT / API Key 均可（归属校验在 service 内按凭证用户走同一条链）；
		// 标题/维度修改仅 JWT；content 支持 ?token= 供 <img> 直链
		api.POST("/projects/:id/screenshots", middleware.RequireAuth(cfg, apiKeyRepo, authService), screenshotHandler.Upload)
		api.GET("/projects/:id/screenshots", middleware.RequireAuth(cfg, apiKeyRepo, authService), screenshotHandler.List)
		api.GET("/screenshot-screens/:sid/content", middleware.RequireAuthWithQueryToken(cfg, apiKeyRepo, authService, "token"), screenshotHandler.Content)
		api.HEAD("/screenshot-screens/:sid/content", middleware.RequireAuthWithQueryToken(cfg, apiKeyRepo, authService, "token"), screenshotHandler.Content)
		api.PATCH("/screenshot-screens/:sid", middleware.RequireJWT(cfg, authService), screenshotHandler.Update)
		api.DELETE("/screenshot-screens/:sid", middleware.RequireAuth(cfg, apiKeyRepo, authService), screenshotHandler.DeleteScreen)

		// 截图标注 —— 列表与裁剪图 JWT / API Key 均可；新建、删除与批量动作仅 JWT；
		// PUT 两类凭证均可，service 内再按凭证限制 API Key 只能置 resolved；crop 支持 ?token= 直链。
		api.GET("/projects/:id/screenshot-annotations", middleware.RequireAuth(cfg, apiKeyRepo, authService), screenshotAnnotationHandler.List)
		api.POST("/projects/:id/screenshot-annotations/batch", middleware.RequireJWT(cfg, authService), screenshotAnnotationHandler.Batch)
		api.POST("/screenshot-screens/:sid/annotations", middleware.RequireJWT(cfg, authService), screenshotAnnotationHandler.Create)
		api.PUT("/screenshot-annotations/:aid", middleware.RequireAuth(cfg, apiKeyRepo, authService), screenshotAnnotationHandler.Update)
		api.DELETE("/screenshot-annotations/:aid", middleware.RequireJWT(cfg, authService), screenshotAnnotationHandler.Delete)
		api.GET("/screenshot-annotations/:aid/crop", middleware.RequireAuthWithQueryToken(cfg, apiKeyRepo, authService, "token"), screenshotAnnotationHandler.Crop)
		api.HEAD("/screenshot-annotations/:aid/crop", middleware.RequireAuthWithQueryToken(cfg, apiKeyRepo, authService, "token"), screenshotAnnotationHandler.Crop)

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

		// JWT 必须 — T3 环境连接与 T3 客户端操作（INT-82/INT-85；涉及用户私有
		// 凭证与用户身份操作，API Key 一律拒绝 40301）
		t3 := api.Group("", middleware.RequireJWT(cfg, authService))
		{
			t3.GET("/environments", t3EnvironmentHandler.List)
			t3.POST("/environments", t3EnvironmentHandler.Create)
			t3.PUT("/environments/:id", t3EnvironmentHandler.Update)
			t3.DELETE("/environments/:id", t3EnvironmentHandler.Delete)
			t3.GET("/environments/:id/threads/:threadId", t3EnvironmentHandler.GetThread)
			t3.GET("/environments/:id/threads/:threadId/items/:itemId", t3EnvironmentHandler.GetThreadItem)
			t3.GET("/threads", t3EnvironmentHandler.ListThreads)
			// shell 变更推送（INT-132）：SSE 长连接，登录后订阅本用户全部环境
			// 的线程/状态变化事件。
			t3.GET("/threads/stream", t3EnvironmentHandler.StreamThreadEvents)

			// T3 客户端（INT-85）：启动器/工作区选项与全部写操作。
			// 只读会话（缺 orchestration:operate）在 service 层统一 40300。
			t3.GET("/environments/:id/composer-options", t3EnvironmentHandler.GetComposerOptions)
			t3.GET("/environments/:id/projects/:projectId/workspace-options", t3EnvironmentHandler.GetWorkspaceOptions)
			t3.POST("/environments/:id/threads", t3EnvironmentHandler.CreateThread)
			t3.POST("/environments/:id/threads/:threadId/messages", t3EnvironmentHandler.SendMessage)
			t3.POST("/environments/:id/threads/:threadId/stop", t3EnvironmentHandler.StopThread)
			t3.POST("/environments/:id/threads/:threadId/queue/resume", t3EnvironmentHandler.ResumeQueue)
			t3.POST("/environments/:id/threads/:threadId/queue/:runId/steer", t3EnvironmentHandler.SteerQueuedRun)
			t3.POST("/environments/:id/threads/:threadId/queue/:runId/cancel", t3EnvironmentHandler.CancelQueuedRun)
			t3.PUT("/environments/:id/threads/:threadId/queue/:runId", t3EnvironmentHandler.EditQueuedRun)
			t3.POST("/environments/:id/threads/:threadId/queue/:runId/reorder", t3EnvironmentHandler.ReorderQueuedRun)
			t3.POST("/environments/:id/threads/:threadId/requests/:requestId/respond", t3EnvironmentHandler.RespondToRequest)
			t3.POST("/environments/:id/threads/:threadId/requests/:requestId/dismiss", t3EnvironmentHandler.DismissRequest)
			t3.POST("/environments/:id/threads/:threadId/items/:itemId/secret-answer", t3EnvironmentHandler.AnswerSecretRequest)
			t3.POST("/environments/:id/attachments", t3EnvironmentHandler.UploadAttachment)
			t3.GET("/environments/:id/attachments/:attachmentId/content", t3EnvironmentHandler.GetAttachmentContent)
			t3.DELETE("/environments/:id/attachments/:attachmentId", t3EnvironmentHandler.DeleteAttachment)
		}

		// JWT / API Key 均可 — T3 项目发现与项目绑定（INT-89）：仅有的对 API Key
		// 开放的 T3 面，不含凭证、不含线程内容；其余 T3 端点仍仅 JWT。
		api.GET("/environments/t3-projects", middleware.RequireAuth(cfg, apiKeyRepo, authService), t3BindingHandler.ListEnvironmentProjects)
		api.GET("/projects/:id/t3-bindings", middleware.RequireAuth(cfg, apiKeyRepo, authService), t3BindingHandler.ListBindings)
		api.PUT("/projects/:id/t3-bindings/:environmentId", middleware.RequireAuth(cfg, apiKeyRepo, authService), t3BindingHandler.UpsertBinding)
		api.DELETE("/projects/:id/t3-bindings/:environmentId", middleware.RequireAuth(cfg, apiKeyRepo, authService), t3BindingHandler.DeleteBinding)

		// JWT 必须 — 部署设置读取（INT-129）：public_url 当前生效值与来源，
		// 所有登录用户可读；写入在下方管理员组。
		api.GET("/deployment/settings", middleware.RequireJWT(cfg, authService), deploymentSettingsHandler.Get)

		// 管理员（INT-129）：JWT + RequireAdmin，角色每请求查库现判，API Key
		// 在 RequireJWT 已被 40301 拒绝。成员与邀请、部署设置写入、部署级
		// GitHub App 的配置与 Manifest 创建。
		admin := api.Group("", middleware.RequireJWT(cfg, authService), middleware.RequireAdmin(authService))
		{
			admin.GET("/members", memberHandler.List)
			admin.PUT("/members/:uid/role", memberHandler.UpdateRole)
			admin.GET("/invites", memberHandler.ListInvites)
			admin.POST("/invites", memberHandler.CreateInvite)
			admin.DELETE("/invites/:id", memberHandler.RevokeInvite)
			admin.PUT("/deployment/settings", deploymentSettingsHandler.Update)
			admin.PUT("/deployment/github-app", deploymentGitHubAppHandler.Update)
			admin.POST("/deployment/github-app/test", deploymentGitHubAppHandler.Test)
			admin.POST("/deployment/github-app/manifest", deploymentGitHubAppHandler.CreateManifest)
			admin.POST("/deployment/github-app/manifest/complete", deploymentGitHubAppHandler.CompleteManifest)
		}
	}

	// JWT 必须 — 站内通知（read-all 为静态段，优先于 :id 匹配）
	api.GET("/notifications", middleware.RequireJWT(cfg, authService), notificationHandler.List)
	api.GET("/notifications/unread-count", middleware.RequireJWT(cfg, authService), notificationHandler.UnreadCount)
	api.POST("/notifications/read-all", middleware.RequireJWT(cfg, authService), notificationHandler.MarkAllRead)
	api.POST("/notifications/:id/read", middleware.RequireJWT(cfg, authService), notificationHandler.MarkRead)

	// JWT 必须 — PR 审查（INT-135）：审查总览、项目审查设置与记录。
	api.GET("/reviews", middleware.RequireJWT(cfg, authService), reviewHandler.ListProjects)
	api.GET("/projects/:id/review-settings", middleware.RequireJWT(cfg, authService), reviewHandler.GetSettings)
	api.PUT("/projects/:id/review-settings", middleware.RequireJWT(cfg, authService), reviewHandler.UpdateSettings)
	api.GET("/projects/:id/review-records", middleware.RequireJWT(cfg, authService), reviewHandler.ListRecords)
	api.POST("/projects/:id/reviews/rerun", middleware.RequireJWT(cfg, authService), reviewHandler.Rerun)

	// AI 提供商（INT-133）：内置目录读取与按用户隔离的 CRUD/测试连接，全部
	// 仅 JWT；test/catalog 为静态段，优先于 :id 匹配。
	aiProviders := api.Group("/ai-providers", middleware.RequireJWT(cfg, authService))
	{
		aiProviders.GET("", aiProviderHandler.List)
		aiProviders.POST("", aiProviderHandler.Create)
		aiProviders.POST("/test", aiProviderHandler.TestConnection)
		aiProviders.GET("/catalog", aiProviderHandler.Catalog)
		aiProviders.GET("/:id", aiProviderHandler.Get)
		aiProviders.PUT("/:id", aiProviderHandler.Update)
		aiProviders.DELETE("/:id", aiProviderHandler.Delete)
	}

	// JWT 必须 — 内置机器人（INT-101）：管理、主线、运行、调用、确认授权与
	// SSE 全部仅 JWT；test-connection 为静态段，优先于 :id 匹配。
	bots := api.Group("/bots", middleware.RequireJWT(cfg, authService))
	{
		bots.GET("", botHandler.List)
		bots.POST("", botHandler.Create)
		bots.POST("/test-connection", botHandler.TestConnection)
		bots.GET("/:id", botHandler.Get)
		bots.PUT("/:id", botHandler.Update)
		bots.DELETE("/:id", botHandler.Delete)
		bots.GET("/:id/messages", botHandler.ListMessages)
		bots.GET("/:id/chat", botHandler.GetChatSnapshot)
		bots.POST("/:id/messages", botHandler.SendMessage)
		bots.GET("/:id/events", botHandler.StreamEvents)
		bots.GET("/:id/runs", botHandler.ListRuns)
		bots.GET("/:id/runs/:runId", botHandler.GetRun)
		bots.POST("/:id/runs/:runId/stop", botHandler.StopRun)
		bots.GET("/:id/calls", botHandler.ListCalls)
		bots.GET("/:id/calls/:callId", botHandler.GetCall)
		bots.GET("/:id/calls/:callId/result", botHandler.GetCallResult)
		bots.POST("/:id/calls/:callId/decision", botHandler.CallDecision)
		bots.POST("/:id/calls/:callId/cancel", botHandler.CancelCall)
		bots.GET("/:id/authorizations", botHandler.ListAuthorizations)
		bots.DELETE("/:id/authorizations/:authorizationId", botHandler.RevokeAuthorization)
		// 定时任务（INT-103）：用户只能查看与开关，没有创建/编辑/删除端点；
		// 任务的创建/修改/删除只经机器人 fs_schedule 工具。INT-123 起可清空
		// 任务小状态（幂等），写入仍只经机器人 state_set。
		bots.GET("/:id/schedule-tasks", botHandler.ListScheduleTasks)
		bots.GET("/:id/schedule-tasks/:taskId", botHandler.GetScheduleTask)
		bots.GET("/:id/schedule-tasks/:taskId/runs", botHandler.ListScheduleTaskRuns)
		bots.PUT("/:id/schedule-tasks/:taskId/enabled", botHandler.SetScheduleTaskEnabled)
		bots.DELETE("/:id/schedule-tasks/:taskId/state", botHandler.ClearScheduleTaskState)
	}

	// 审查 Agent 端点（/api/review-agent，INT-135）：仅 Bearer 运行令牌
	// （fsr_）。读/上下文端点 requireLive（运行终态即拒）；result 允许
	// 结束后完全相同正文的结束回报重放。
	reviewAgent := api.Group("/review-agent")
	{
		live := reviewAgent.Group("", middleware.RequireRunToken(reviewResolver, true))
		{
			live.GET("/pull-request", reviewAgentHandler.PullRequest)
			live.GET("/pull-request/diff", reviewAgentHandler.Diff)
			live.GET("/history", reviewAgentHandler.History)
			live.GET("/screenshots", reviewAgentHandler.Screenshots)
			live.GET("/screenshots/:assetId", reviewAgentHandler.ScreenshotImage)
		}
		reviewAgent.POST("/result", middleware.RequireRunToken(reviewResolver, false), reviewAgentHandler.SubmitResult)
	}

	// Fast Ship 自身日志查询（INT-94，只读）：JWT（Web 管理员）与 fsk_ API Key
	// （代理）可查本人及系统级日志；fsr_ 令牌 40301。
	selfLogs := api.Group("/self-logs", middleware.RequireSelfLogAuth(cfg, apiKeyRepo, authService))
	{
		selfLogs.GET("", selfLogHandler.ListSelfLogs)
		selfLogs.GET("/aggregate", selfLogHandler.AggregateSelfLogs)
		selfLogs.GET("/trace", selfLogHandler.TraceSelfLogs)
	}

	// GitHub App webhook 入站（INT-93）：公开端点，X-Hub-Signature-256 验签。
	api.POST("/github/webhooks", githubWebhookHandler.Handle)

	// 自测观察面（INT-93）：仅 fake + debug 模式有内容；JWT 必须，不进 OpenAPI。
	api.GET("/debug/github-review-log", middleware.RequireJWT(cfg, authService), githubWebhookHandler.DebugReviewLog)

	setupWebRoutes(r, cfg.Server.WebDistDir)
}
