# Web State

客户端状态管理。使用 Zustand 管理认证状态和主题偏好，均支持 localStorage 持久化。

## Public API

### Auth Store (`auth-store.ts`)

| Export | Type | Description |
|---|---|---|
| `useAuthStore` | hook | 认证状态：token, refreshToken, user, isAuthenticated |
| `setTokens(access, refresh)` | action | 设置 Token 对 |
| `setUser(user)` | action | 设置用户信息 |
| `clearAuth()` | action | 清除所有认证数据（登出） |

### Theme Store (`theme-store.ts`)

| Export | Type | Description |
|---|---|---|
| `useThemeStore` | hook | 主题状态：theme (light/dark/system) |

### Issue Context (`issue-list-context.ts`)

| Export | Type | Description |
|---|---|---|
| `useIssueListContext` | hook | Issue 列表过滤状态的 URL 参数持久化 |

## Internal Structure

| File | Purpose |
|---|---|
| `web/src/lib/store/auth-store.ts` | 认证状态管理，含 Token 持久化 |
| `web/src/lib/store/theme-store.ts` | 主题偏好管理 |
| `web/src/lib/issue-list-context.ts` | Issue 列表过滤状态的 URL 参数同步 |
| `web/src/lib/issue-workflow-status.ts` | 内部 Issue 工作流状态常量定义 |

## Dependencies

| Depends on | Why |
|---|---|
| `zustand` | 状态管理库 |
| `next-themes` | 主题切换实现 |

## Dependents

| Used by | How |
|---|---|
| web-api | client.ts 读取 Token 注入请求 header，401 时调用 clearAuth |
| web-routes | 页面组件读取用户信息、认证状态 |
| web-components | layout 组件读取用户信息和主题 |

## Implementation Notes

- Auth Store 使用 Zustand `persist` 中间件，将 Token 和用户信息保存到 localStorage
- Token 刷新逻辑在 `web-api/client.ts` 中处理，刷新成功后更新 auth-store
- `issue-list-context.ts` 将过滤参数（状态、标签等）编码到 URL query 参数中，实现可分享的过滤视图
