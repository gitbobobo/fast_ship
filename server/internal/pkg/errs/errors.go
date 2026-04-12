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
	ErrInvalidParams = New(40001, "请求参数无效")
)

// 认证错误 40100-40199
var (
	ErrTokenInvalid   = New(40100, "Token 无效或已过期")
	ErrTokenExpired   = New(40101, "Token 已过期")
	ErrApiKeyInvalid  = New(40102, "API Key 无效")
	ErrLoginFailed    = New(40103, "用户名/邮箱或密码错误")
	ErrTokenBlacklist = New(40104, "Token 已被注销")
)

// 权限错误 40300-40399
var (
	ErrPermissionDenied = New(40300, "权限不足")
	ErrApiKeyForbidden  = New(40301, "API Key 无此操作权限")
	ErrNotOwner         = New(40302, "非资源所有者")
)

// 资源不存在 40400-40499
var (
	ErrUserNotFound     = New(40400, "用户不存在")
	ErrProjectNotFound  = New(40401, "项目不存在")
	ErrVersionNotFound  = New(40402, "版本不存在")
	ErrArtifactNotFound = New(40403, "安装包不存在")
	ErrApiKeyNotFound   = New(40404, "API Key 不存在")
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
)

// 服务器内部错误 50000-50099
var (
	ErrInternal = New(50000, "服务器内部错误")
)

// GitHub API 错误 50200-50299
var (
	ErrGitHubAPI = New(50200, "GitHub API 调用失败")
)
