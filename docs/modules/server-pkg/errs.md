# Server Pkg / Errs

统一错误码定义。定义业务级错误常量，配合 `response` 包返回标准化错误响应。

## Public API

| Export | Type | Description |
|---|---|---|
| `ErrNotFound` | `error` | 资源不存在 |
| `ErrUnauthorized` | `error` | 未认证 |
| `ErrForbidden` | `error` | 无权限 |
| `ErrBadRequest` | `error` | 请求参数错误 |
| `ErrConflict` | `error` | 资源冲突（如重复创建） |
| `ErrInternal` | `error` | 内部错误 |
| `New(msg)` | func | 创建自定义错误 |

## Internal Structure

| File | Purpose |
|---|---|
| `server/internal/pkg/errs/errors.go` | 错误类型和常量定义 |

## Dependents

| Used by | How |
|---|---|
| server-service | 返回业务错误 |
| server-handler | 检查错误类型并返回对应 HTTP 状态码 |
