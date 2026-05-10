# Server Pkg / Response

HTTP 响应格式化工具。统一所有 API 的响应格式。

## Public API

| Export | Type | Description |
|---|---|---|
| `Success(c, data)` | func | 返回 200 成功响应 `{"success": true, "data": ...}` |
| `Created(c, data)` | func | 返回 201 创建成功响应 |
| `Error(c, code, msg)` | func | 返回错误响应 `{"success": false, "error": ...}` |
| `Paginated(c, data, total, page, pageSize)` | func | 返回分页响应，含 `total`/`page`/`page_size` 元数据 |

## Internal Structure

| File | Purpose |
|---|---|
| `server/internal/pkg/response/response.go` | 响应格式化函数 |

## Dependents

| Used by | How |
|---|---|
| server-handler | 所有 Handler 通过此包返回响应 |

## Implementation Notes

- 成功响应格式：`{"success": true, "data": <any>}`
- 错误响应格式：`{"success": false, "error": {"code": <int>, "message": <string>}}`
- 分页响应在 `data` 外增加 `pagination` 字段
