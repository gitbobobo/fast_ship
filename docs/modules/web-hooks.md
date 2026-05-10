# Web Hooks

TanStack Query hooks。封装所有服务端数据获取逻辑，使用 React Query 管理缓存、加载状态和自动刷新。

## Public API

| Hook | File | Description |
|---|---|---|
| `useProjects` | `use-projects.ts` | 项目列表和 CRUD mutations |
| `useProject` | `use-projects.ts` | 单个项目详情 |
| `useProjectBranches` | `use-projects.ts` | 项目分支列表 |
| `useVersions` | `use-versions.ts` | 版本列表 |
| `useVersion` | `use-versions.ts` | 版本详情 |
| `useCreateVersion` | `use-versions.ts` | 创建版本 mutation |
| `useShipVersion` | `use-versions.ts` | Ship 版本 mutation（含 shipCheck 前置检查） |
| `useIssues` | `use-issues.ts` | Issue 列表（含复杂过滤和无限滚动） |
| `useIssue` | `use-issues.ts` | Issue 详情 |
| `useArtifacts` | `use-artifacts.ts` | 产物上传和下载 |
| `useAISettings` | `use-ai.ts` | AI 设置 |
| `useApiKeys` | `use-api-keys.ts` | API Key CRUD |
| `useNavigationHistory` | `use-navigation-history.ts` | 导航历史记录管理 |

## Internal Structure

| File | Purpose |
|---|---|
| `web/src/lib/hooks/use-projects.ts` | 项目相关 hooks |
| `web/src/lib/hooks/use-versions.ts` | 版本相关 hooks，含 Ship 流程 |
| `web/src/lib/hooks/use-issues.ts` | Issue 相关 hooks，含过滤和分页 |
| `web/src/lib/hooks/use-artifacts.ts` | 产物上传/下载 hooks |
| `web/src/lib/hooks/use-ai.ts` | AI 设置和 Checklist 建议 |
| `web/src/lib/hooks/use-api-keys.ts` | API Key 管理 hooks |
| `web/src/lib/hooks/use-navigation-history.ts` | 浏览器导航历史工具 |

## Dependencies

| Depends on | Why |
|---|---|
| web-api | 所有 API 调用函数 |
| `@tanstack/react-query` | 数据缓存和状态管理 |

## Dependents

| Used by | How |
|---|---|
| web-routes | 页面组件通过 hooks 获取和操作数据 |

## Implementation Notes

- Issue 列表使用 `useInfiniteQuery` 实现无限滚动加载
- Mutation 成功后自动 invalidate 相关 query key 以刷新列表
- `useShipVersion` 内部先调用 `shipCheck` 验证，通过后再执行 `ship`
- Query key 按资源层级组织：`['projects']`, `['projects', id]`, `['projects', id, 'versions']` 等
