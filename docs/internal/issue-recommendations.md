# 推荐任务（Issue Recommendations）

Agent（API Key）把 Issue 标记为「推荐做」，附推荐理由与前置依赖 Issue；用户在 Web 看板「推荐」弹框中查看并可移除。本文档记录数据模型、端点、自动移除挂钩与前端结构，供后续代理维护参考。

## 数据模型

两张表，GORM `AutoMigrate` 注册（`server/cmd/server/main.go`），无外置迁移工具。

### `issue_recommendations`

每个 Issue 至多一条推荐，`issue_id` 即主键。

| 字段 | 类型 | 说明 |
|---|---|---|
| `issue_id` | TEXT | 主键；FK → `issues`，`OnDelete: CASCADE` |
| `project_id` | TEXT | 冗余项目 ID + 索引（仿 `IssueShipHook` 的 denormalized project_id 模式），列表按项目过滤不再回查 issues |
| `reason` | TEXT | 推荐理由，1–500 字符 |
| `priority` | TEXT | `high` / `medium` / `low`，默认 `medium` |
| `created_by` | TEXT | 提交者 API Key 名称 |
| `created_at` / `updated_at` | DATETIME | upsert 覆盖时保留 `created_at`、刷新 `updated_at` |

### `recommendation_dependencies`

前置依赖，复合主键 `(issue_id, dep_issue_id)`。

| 字段 | 类型 | 说明 |
|---|---|---|
| `issue_id` | TEXT | 主键之一；FK → `issues`，`OnDelete: CASCADE`（注意：指向 issues，不指向 issue_recommendations） |
| `dep_issue_id` | TEXT | 主键之一；FK → `issues`，`OnDelete: CASCADE` |
| `position` | INTEGER | 写入时按数组顺序 0..n；读取按 `position ASC` 保持插入序 |
| `created_at` | DATETIME | |

依赖行外键指向 `issues` 而非推荐行，因此删除推荐时依赖行**不会**级联，由 `IssueRecommendationRepository.DeleteTx` 显式删除；Issue 本身被删除时两表均由 `issues` 的 `OnDelete: CASCADE` 兜底。

## 端点与权限

| 方法 | 路径 | 凭证 | 说明 |
|---|---|---|---|
| PUT | `/api/issues/:iid/recommendation` | **仅 API Key** | 覆盖 upsert；JWT 调用在 handler 内经 `requireApiKey` 返回 403（40303） |
| DELETE | `/api/issues/:iid/recommendation` | JWT / API Key | 推荐不存在返回 404（40411） |
| GET | `/api/recommendations?project_id=` | JWT / API Key | `project_id` 为空返回当前用户全部项目；非空校验项目归属 |

PUT 校验：目标 Issue 须 `state=open` 且 `workflow_status` ∈ {未设置, `todo`}（meta 行不存在视为未设置），否则 409（40910）；`reason` 1–500 字符、依赖 ≤20 且不含自身（40001）；目标或依赖 Issue 不存在（40405）；项目归属按 `issue → project → user_id` 链校验（40401）。依赖 Issue 所属项目也必须归属当前用户（跨项目依赖允许，限同 owner）；不属于当前用户的依赖与不存在同等返回 40405，不区分「不存在」与「无权限」以免被用来枚举。

Upsert 把「读 issue、读 meta、可推荐性校验、依赖校验（含归属）、推荐 upsert、依赖替换」全部放进 `recRepo.Transaction` 同一事务，读写经 `*Tx` 变体（`FindByIDTx` / `ListByIDsTx` / `GetTx` / `ListByIssueIDsTx`，非 Tx 方法委托 Tx 版本走 `r.db`）。SQLite 单写者语义下保证校验时看到的状态与提交时一致，任一校验失败整体回滚不留半写状态。

GET 响应为 `{items: [...]}`，单条含 `issue` 摘要（id / project_id / project_name / source / sequence_number / title / state / workflow_status）、`reason`、`priority`、`created_by`、时间戳与 `dependencies`（含 dep 标题/状态摘要）。排序：`priority` 降序（high > medium > low，`CASE` 表达式）→ `updated_at` 降序。一次全量，无分页。

错误码：`ErrRecommendationNotFound`（40411）、`ErrIssueNotRecommendable`（40910），均在 `errs` 对应区间 var 块。

## 自动移除（硬删，不恢复）

推荐只在「还没开始做」时有意义，Issue 状态前移即删除。挂载点在 service 层，覆盖全部写路径：

- `IssueService.UpdateInternalMeta`（`issue_meta.go`）：`workflow_status` 被置为 `in_progress` / `done` 时，在同一事务内 `recRepo.DeleteTx`。ship hook 自动流转、看板拖拽、API 写状态都经此函数。
- `IssueService.UpdateInternalIssue`（`issue.go`）：`state` 变为 `closed` 时同事务删除。内部 Issue 关闭与 `BatchCloseDoneIssues` 都经此路径。
- `IssueService.upsertGitHubIssue`（`issue_sync.go`）：GitHub 同步落地 `state=closed` 时，issue 的 Create/Save 与 `recRepo.DeleteTx` 放在 `issueRepo.Transaction` 同一事务，Delete 失败回滚不留「已关闭仍被推荐」残留；非 closed 路径不变。GitHub 侧关单、Web 端关 GitHub issue（经 `UpdateInternalIssue` → GitHub API → `upsertGitHubIssue`）均覆盖。

项目删除 / Issue 级联删除由 FK `OnDelete: CASCADE` 兜底，无额外代码。

## 前端结构

- `web/src/lib/api/recommendations.ts`：`recommendationApi.list(projectId?)` / `remove(issueId)`。
- `web/src/lib/hooks/use-recommendations.ts`：`useRecommendations(projectId?)`（queryKey `['recommendations', projectId ?? 'all']`，`refetchInterval: 60000`，`refetchOnWindowFocus: true`）+ `useRemoveRecommendation()`（成功后 invalidate `['recommendations']`）。
- `web/src/routes/board/components/recommended-issues-button.tsx`：按钮 + 受控 Dialog + AlertDialog 确认删除，全部同文件。
- 挂载：`board/index.tsx` 筛选行多选 toggle 之后，`activeProjectId || undefined` 传入（看板无「全部项目」态，空串等同全量）。

## 刷新策略

推荐列表不做服务端推送，前端按 60s 轮询 + 窗口聚焦 refetch。按钮在列表为空或加载失败时整体隐藏（`return null`），删除最后一条后按钮随之消失。自动移除是写路径副作用：下次轮询时推荐自然消失，无需前端主动同步。
