package service

import "github.com/godbobo/fast_ship/server/internal/api"

// API 请求/响应类型的契约真相源是 api/types.gen.go（oapi-codegen 自 api/openapi.yaml
// 生成）。这里为既有 service 层名字提供类型别名，避免引用点搬运包名；
// 字段名以生成类型为准（Id/Url 风格），构造点相应调整。
//
// 例外：UpdateProfileRequest/UpdateProjectRequest 不带 binding tag——handler 用
// 手写输入结构绑定（字段指针化后，omitempty 对显式空串的跳过语义不等价），
// UpdateDocumentRequest 同理保留手写（parent_id 需省略/null/字符串三态）。
type (
	// auth
	RegisterRequest       = api.RegisterRequest
	LoginRequest          = api.LoginRequest
	UpdateProfileRequest  = api.UpdateProfileRequest
	UpdatePasswordRequest = api.UpdatePasswordRequest
	RefreshRequest        = api.RefreshRequest
	AuthResponse          = api.AuthResponse
	RefreshResponse       = api.RefreshResponse
	UserResponse          = api.User

	// api keys
	CreateApiKeyRequest = api.CreateApiKeyRequest
	ApiKeyResponse      = api.ApiKeyCreated

	// projects
	CreateProjectRequest  = api.CreateProjectRequest
	UpdateProjectRequest  = api.UpdateProjectRequest
	ProjectResponse       = api.Project
	LatestVersionResponse = api.LatestVersion
	BranchResponse        = api.Branch

	// versions & ship
	CreateVersionRequest = api.CreateVersionRequest
	UpdateVersionRequest = api.UpdateVersionRequest
	ShipCheckResponse    = api.ShipCheck
	ShipCheckItem        = api.ShipCheckItem
	PendingIssueHook     = api.PendingIssueHook

	// issues
	CreateInternalIssueRequest        = api.CreateIssueRequest
	UpdateInternalIssueRequest        = api.UpdateIssueRequest
	CreateInternalIssueCommentRequest = api.CreateIssueCommentJSONBody
	ReplaceIssueChecklistRequest      = api.ReplaceIssueChecklistJSONBody
	IssueChecklistItemInput           = api.IssueChecklistItemInput
	IssueResponse                     = api.Issue
	IssueActorResponse                = api.IssueActor
	IssueLabelResponse                = api.IssueLabel
	IssueMilestoneResponse            = api.IssueMilestone
	IssueReactionSummaryResponse      = api.IssueReactionSummary
	IssueGitHubResponse               = api.IssueGitHubMeta
	IssueCommentResponse              = api.IssueComment
	IssueTimelineEventResponse        = api.IssueTimelineEvent
	IssueSyncResponse                 = api.IssueSyncResult
	IssueFilterOptionsResponse        = api.IssueFilterOptions
	IssueSyncStateResponse            = api.IssueSyncState
	IssueInternalMetaResponse         = api.IssueInternalMeta
	IssueChecklistItemResponse        = api.IssueChecklistItem
	IssueAssetResponse                = api.IssueAsset
	BatchCloseDoneIssueFailure        = api.IssueBatchFailure
	BatchCloseDoneIssuesResponse      = api.IssueBatchResult

	// issue ship hooks
	UpsertShipHookRequest        = api.UpsertShipHookRequest
	IssueShipHookActionResult    = api.IssueShipHookActionResult
	IssueShipHookResultsResponse = api.IssueShipHookResults
	IssueShipHookResponse        = api.IssueShipHook

	// issue collab
	IssueCollabActorResponse = api.IssueCollabDoc_Author
	IssueCollabDocResponse   = api.IssueCollabDoc
	IssueCollabAreaResponse  = api.IssueCollabArea
	UpsertIssueCollabRequest = api.UpsertIssueCollabRequest

	// issue recommendations
	UpsertIssueRecommendationRequest = api.UpsertIssueRecommendationRequest
	DeferIssueRecommendationRequest  = api.DeferIssueRecommendationRequest
	RecommendationIssueSummary       = api.RecommendationIssueSummary
	RecommendationDependencyResponse = api.RecommendationDependency
	IssueRecommendationResponse      = api.IssueRecommendation
	IssueRecommendationListResponse  = api.ListIssueRecommendations200JSONResponseBody_Data

	// issue prompts（model.IssuePromptItem 为存储形态；出入参转换见 issue_prompt.go）
	IssuePromptsResponse      = api.IssuePrompts
	UpdateIssuePromptsRequest = api.UpdateIssuePromptsJSONBody

	// internal meta 批量更新（workflow_status 可空由 service 校验）
	BatchUpdateInternalMetaItem = api.BatchUpdateIssueInternalMetaJSONBody_Items

	// ai
	UpdateAISettingsRequest           = api.UpdateAISettingsRequest
	AISettingsResponse                = api.AISettings
	GenerateTitleResponse             = api.GenerateTitleResponse
	IssueChecklistSuggestionsResponse = api.IssueChecklistSuggestions
	IssueChecklistSuggestionItem      = api.IssueChecklistSuggestions_Items

	// dashboard
	DashboardOverviewResponse          = api.DashboardOverview
	DashboardProjectOpenIssuePoint     = api.DashboardProjectOpenIssuePoint
	DashboardDailyResolvedPoint        = api.DashboardDailyResolvedPoint
	DashboardDailyResolvedProjectPoint = api.DashboardDailyResolvedProjectPoint

	// documents
	CreateDocumentRequest = api.CreateDocumentRequest
	DocumentListItem      = api.DocumentListItem
	DocumentDetail        = api.Document
	DocumentListData      = api.ListDocuments200JSONResponseBody_Data

	// logs
	UploadLogsRequest = api.UploadLogsRequest
	LogEntryInput     = api.LogEntryInput
	UploadLogsResult  = api.UploadLogsResult
	LogEntryItem      = api.LogEntry
	LogRunItem        = api.LogRun
)
