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
	ErrInvalidParams              = New(40001, "请求参数无效")
	ErrTargetBranchNotFound       = New(40002, "目标分支不存在")
	ErrProjectGitHubNotConfigured = New(40003, "该项目尚未关联 GitHub 仓库")
)

// 认证错误 40100-40199
var (
	ErrTokenInvalid        = New(40100, "Token 无效或已过期")
	ErrTokenExpired        = New(40101, "Token 已过期")
	ErrApiKeyInvalid       = New(40102, "API Key 无效")
	ErrLoginFailed         = New(40103, "用户名/邮箱或密码错误")
	ErrTokenBlacklist      = New(40104, "Token 已被注销")
	ErrRefreshTokenInvalid = New(40105, "Refresh Token 无效或已过期")
)

// 权限错误 40300-40399
var (
	ErrPermissionDenied = New(40300, "权限不足")
	ErrApiKeyForbidden  = New(40301, "API Key 无此操作权限")
	ErrNotOwner         = New(40302, "非资源所有者")
	ErrApiKeyRequired   = New(40303, "该操作仅限 API Key 调用")
)

// 资源不存在 40400-40499
var (
	ErrUserNotFound        = New(40400, "用户不存在")
	ErrProjectNotFound     = New(40401, "项目不存在")
	ErrVersionNotFound     = New(40402, "版本不存在")
	ErrArtifactNotFound    = New(40403, "安装包不存在")
	ErrApiKeyNotFound      = New(40404, "API Key 不存在")
	ErrIssueNotFound       = New(40405, "问题不存在")
	ErrIssueAssetNotFound  = New(40406, "问题图片不存在")
	ErrAISettingsNotFound  = New(40407, "请先在设置中配置 MiniMax API Key")
	ErrIssueCollabNotFound = New(40408, "协作区内容不存在")
	ErrLogBatchNotFound    = New(40409, "日志批次不存在")
	ErrDocumentNotFound    = New(40410, "文档不存在")
)

// 业务冲突 40900-40999
var (
	ErrUsernameExists      = New(40900, "用户名已存在")
	ErrEmailExists         = New(40901, "邮箱已存在")
	ErrProjectNameExists   = New(40902, "项目名称在当前用户下已存在")
	ErrVersionNumberExists = New(40903, "版本号在该项目下已存在")
	ErrVersionNotPending   = New(40904, "版本不是待发货状态，无法执行此操作")
	ErrVersionShipped      = New(40905, "版本已发货，无法修改")
	ErrShipPreCheckFailed  = New(40906, "发货前置校验未通过")
	ErrIssueSyncRunning    = New(40907, "问题同步正在进行中")
	ErrIssueReadOnly       = New(40908, "该问题为只读问题")
)

// 前置条件未满足 41200-41299
var (
	ErrBatchCloseTooMany = New(41201, "匹配问题数量过多，请缩小关闭范围后重试")
)

// 服务器内部错误 50000-50099
var (
	ErrInternal = New(50000, "服务器内部错误")
)

// GitHub API 错误 50200-50299
var (
	ErrGitHubAPI  = New(50200, "GitHub API 调用失败")
	ErrAIProvider = New(50201, "AI 服务调用失败")
)
