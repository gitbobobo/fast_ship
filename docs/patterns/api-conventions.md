# API Conventions

Fast Ship 的 RESTful API 设计约定，贯穿后端 Handler/Response 和前端 API Client/Hooks。

## Where It Appears

- **server-handler** — 所有 Handler 遵循统一的请求处理模式
- **server-pkg/response** — 统一响应格式化
- **server-pkg/errs** — 统一错误码
- **web-api** — ky 客户端配置和 API 模块
- **web-hooks** — TanStack Query hooks 统一数据获取模式

## Convention

### 响应格式

成功响应：
```json
{
  "success": true,
  "data": { ... }
}
```

分页响应：
```json
{
  "success": true,
  "data": [...],
  "pagination": {
    "total": 100,
    "page": 1,
    "page_size": 20
  }
}
```

错误响应：
```json
{
  "success": false,
  "error": {
    "code": 404,
    "message": "resource not found"
  }
}
```

### HTTP 方法约定

| 操作 | HTTP Method | 示例 |
|---|---|---|
| 列表 | GET | `GET /api/projects` |
| 详情 | GET | `GET /api/projects/:id` |
| 创建 | POST | `POST /api/projects` |
| 更新 | PUT | `PUT /api/projects/:id` |
| 删除 | DELETE | `DELETE /api/projects/:id` |
| 特殊操作 | POST | `POST /api/versions/:vid/ship` |

### URL 结构

- 基础路径：`/api`
- 资源嵌套：`/api/projects/:id/versions`、`/api/projects/:id/issues`
- 独立资源：`/api/versions/:vid`、`/api/issues/:iid`

### 前端 API 模块

每个 API 模块对应一个后端资源，导出纯函数：
```typescript
// web/src/lib/api/projects.ts
export function listProjects() { ... }
export function getProject(id: string) { ... }
export function createProject(data: CreateProjectInput) { ... }
```

### 前端 Hooks

每个 API 模块对应一组 TanStack Query hooks：
- 查询用 `useQuery`（如 `useProjects`）
- 修改用 `useMutation`（如 `useCreateProject`）
- Mutation 成功后 invalidate 对应 query key

## Examples

后端 Handler 模式（`server/internal/handler/project.go`）：
```go
func (h *ProjectHandler) Create(c *gin.Context) {
    var req CreateProjectRequest
    if err := c.ShouldBindJSON(&req); err != nil {
        response.Error(c, http.StatusBadRequest, err.Error())
        return
    }
    project, err := h.projectService.Create(c, userID, &req)
    if err != nil {
        // 错误处理...
        return
    }
    response.Created(c, project)
}
```

前端 Hook 模式（`web/src/lib/hooks/use-projects.ts`）：
```typescript
export function useCreateProject() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: createProject,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['projects'] })
    }
  })
}
```

## Adding to This Pattern

添加新 API 端点时：
1. 后端：在对应 Handler 添加方法 → 在 router.go 注册路由
2. 前端：在 `lib/api/` 添加 API 函数 → 在 `lib/hooks/` 添加 hook → 在页面组件中使用 hook
3. 遵循 `response.Success/Created/Error` 统一响应格式
4. Mutation 成功后务必 invalidate 相关 query key
