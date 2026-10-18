package errs

import "fmt"

// AppError 自定义业务错误类型
type AppError struct {
	Code    int
	Message string
}

func (e *AppError) Error() string {
	return fmt.Sprintf("[%d] %s", e.Code, e.Message)
}

func New(code int, message string) *AppError {
	return &AppError{Code: code, Message: message}
}

// 参数校验错误 40001-40099
var (
	ErrInvalidParams                   = New(40001, "请求参数无效")
	ErrTargetBranchNotFound            = New(40002, "目标分支不存在")
	ErrProjectGitHubNotConfigured      = New(40003, "该项目尚未关联 GitHub 仓库")
	ErrScreenshotImageTooLarge         = New(40004, "截图尺寸超出可处理范围")
	ErrPairingURLInvalid               = New(40005, "配对链接无效或缺少配对码")
	ErrProjectPublicGitHubTokenMissing = New(40006, "该项目未配置公开仓库 Token")
	ErrQuotaProviderUnknown            = New(40030, "未知的额度供应商")
	ErrQuotaCredentialsInvalid         = New(40031, "凭据字段缺失或非法")
	ErrQuotaDeviceAuthUnsupported      = New(40032, "该供应商不支持设备授权")
)

// 认证错误 40100-40199
var (
	ErrTokenInvalid        = New(40100, "Token 无效或已过期")
	ErrTokenExpired        = New(40101, "Token 已过期")
	ErrApiKeyInvalid       = New(40102, "API Key 无效")
	ErrLoginFailed         = New(40103, "用户名/邮箱或密码错误")
	ErrTokenBlacklist      = New(40104, "Token 已被注销")
	ErrRefreshTokenInvalid = New(40105, "Refresh Token 无效或已过期")
	ErrRunTokenInvalid     = New(40106, "运行令牌无效、已过期或已消费")
	ErrWebhookSignature    = New(40107, "Webhook 签名无效或缺失")
)

// 权限错误 40300-40399
var (
	ErrPermissionDenied     = New(40300, "权限不足")
	ErrApiKeyForbidden      = New(40301, "API Key 无此操作权限")
	ErrNotOwner             = New(40302, "非资源所有者")
	ErrApiKeyRequired       = New(40303, "该操作仅限 API Key 调用")
	ErrCredentialNotAllowed = New(40304, "该端点不允许此凭证类型")
)

// 资源不存在 40400-40499
var (
	ErrUserNotFound                 = New(40400, "用户不存在")
	ErrProjectNotFound              = New(40401, "项目不存在")
	ErrVersionNotFound              = New(40402, "版本不存在")
	ErrArtifactNotFound             = New(40403, "安装包不存在")
	ErrApiKeyNotFound               = New(40404, "API Key 不存在")
	ErrIssueNotFound                = New(40405, "问题不存在")
	ErrIssueAssetNotFound           = New(40406, "问题图片不存在")
	ErrAISettingsNotFound           = New(40407, "请先在设置中配置 MiniMax API Key")
	ErrIssueCollabNotFound          = New(40408, "协作区内容不存在")
	ErrLogRunNotFound               = New(40409, "日志运行不存在")
	ErrDocumentNotFound             = New(40410, "文档不存在")
	ErrRecommendationNotFound       = New(40411, "推荐不存在")
	ErrPullRequestNotFound          = New(40412, "PR 关联不存在")
	ErrScreenshotScreenNotFound     = New(40413, "界面不存在")
	ErrIssueAttachmentNotFound      = New(40415, "问题附件不存在")
	ErrScreenshotAnnotationNotFound = New(40416, "截图标注不存在")
	ErrT3EnvironmentNotFound        = New(40417, "T3 环境不存在")
	ErrT3ThreadNotFound             = New(40418, "T3 线程不存在")
	ErrT3ThreadItemNotFound         = New(40419, "T3 线程条目不存在")
	ErrGitHubTokenNotFound          = New(40420, "GitHub Token 不存在")
	ErrAutomationNotFound           = New(40421, "自动化不存在")
	ErrAutomationRunNotFound        = New(40422, "自动化运行不存在")
	ErrNotificationNotFound         = New(40423, "通知不存在")
	ErrBotNotFound                  = New(40424, "机器人不存在")
	ErrBotRunNotFound               = New(40425, "机器人运行不存在")
	ErrBotCallNotFound              = New(40426, "机器人调用不存在")
	ErrBotAuthorizationNotFound     = New(40427, "机器人授权不存在")
	ErrBotScheduleTaskNotFound      = New(40429, "定时任务不存在")
	ErrQuotaAccountNotFound         = New(40430, "额度账号不存在")
	ErrQuotaDeviceAuthNotFound      = New(40431, "授权会话不存在或已过期")
)

// 业务冲突 40900-40999
var (
	ErrUsernameExists           = New(40900, "用户名已存在")
	ErrEmailExists              = New(40901, "邮箱已存在")
	ErrProjectNameExists        = New(40902, "项目名称在当前用户下已存在")
	ErrVersionNumberExists      = New(40903, "版本号在该项目下已存在")
	ErrVersionNotPending        = New(40904, "版本不是待发货状态，无法执行此操作")
	ErrVersionShipped           = New(40905, "版本已发货，无法修改")
	ErrShipPreCheckFailed       = New(40906, "发货前置校验未通过")
	ErrIssueSyncRunning         = New(40907, "问题同步正在进行中")
	ErrIssueReadOnly            = New(40908, "该问题为只读问题")
	ErrLogRunEntryLimitExceeded = New(40909, "该运行日志条数已达上限")
	ErrIssueNotRecommendable    = New(40910, "该 issue 当前状态不可被推荐")
	ErrRecommendationDeferred   = New(40911, "该推荐已被用户延后处理，请勿再次推荐")
	ErrT3EnvironmentNameExists  = New(40912, "环境名称在当前用户下已存在")
	ErrGitHubTokenInUse         = New(40913, "该 Token 仍被项目引用，请先解除或更换引用")
	// 消息不含目标记录信息；替换撞指纹时由 service 用 New 追加已有记录的 title/id。
	ErrGitHubTokenDuplicate        = New(40914, "相同的 Token 已存在")
	ErrGitHubTokenConflict         = New(40915, "GitHub Token 已被其他请求更新")
	ErrT3StateConflict             = New(40916, "T3 状态冲突")
	ErrAutomationRunReportConflict = New(40917, "运行回报冲突：运行已结束或回报与已受理的结束回报不一致")
	ErrPageBoundaryInvalid         = New(40918, "分页边界条目已不存在，请从头重新拉取")
	ErrAutomationActive            = New(40919, "自动化有进行中的运行（或后置操作待执行），暂不能删除")
	ErrRunNotPRReview              = New(40920, "运行不是进行中的 GitHub PR 审查，无法使用 PR 端点")
	ErrScreenshotVariantExists     = New(40921, "相同界面标识与平台/语言/亮度组合的截图已存在")
	// 机器人（INT-101）：幂等键冲突、状态机冲突、删除前置条件与决定互斥。
	ErrBotClientMessageConflict = New(40925, "相同 client_message_id 已用于不同正文")
	ErrBotCallStateConflict     = New(40926, "调用状态已变化，操作未生效")
	ErrBotDeleteConflict        = New(40927, "机器人存在进行中的运行或未终结的调用，请先停止或等待完成")
	ErrBotRunStateConflict      = New(40928, "运行状态已变化，操作未生效")
	// 集成（INT-119）：同 provider 下 App ID 全库唯一——webhook 验签候选逐行
	// 验签，重复行会让创建顺序靠后者永远收不到投递。
	ErrIntegrationAppIDTaken = New(40929, "该 App ID 已被其他用户配置")
)

// 请求过于频繁 42900-42999
var (
	ErrQuotaRefreshTooOften = New(42900, "刷新过于频繁")
)

// 前置条件未满足 41200-41299
var (
	ErrBatchCloseTooMany = New(41201, "匹配问题数量过多，请缩小关闭范围后重试")
	ErrPublicURLMissing  = New(41202, "启用自动化前需配置 server.public_url（FAST_SHIP_SERVER_PUBLIC_URL）")
)

// 服务器内部错误 50000-50099
var (
	ErrInternal = New(50000, "服务器内部错误")
)

// GitHub API 错误 50200-50299
var (
	ErrGitHubAPI               = New(50200, "GitHub API 调用失败")
	ErrAIProvider              = New(50201, "AI 服务调用失败")
	ErrT3Upstream              = New(50202, "T3 环境调用失败")
	ErrBotModelUpstream        = New(50203, "机器人模型服务调用失败")
	ErrQuotaDeviceAuthUpstream = New(50204, "额度供应商授权服务不可用")
)
