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
| `deferred_at` | TIMESTAMP | nullable；非 NULL 即「延后态」，整条推荐冻结保留、对 Agent 封闭（见下节） |
| `defer_note` | TEXT | 延后备注，空串表示无备注（JSON 侧为 null） |
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
| PUT | `/api/issues/:iid/recommendation` | **仅 API Key** | 覆盖 upsert；JWT 调用在 handler 内经 `requireApiKey` 返回 403（40303）；目标已是延后态返回 409（40911） |
| DELETE | `/api/issues/:iid/recommendation` | JWT / API Key | 硬删（删除=遗忘，可再被推荐）；推荐不存在返回 404（40411）；API Key 删延后项返回 409（40911），JWT 删延后项即彻底移除 |
| PUT | `/api/issues/:iid/recommendation/defer` | **仅 JWT** | 延后推荐（body `{"note": "..."}` 可选，note trim 后 ≤500 rune）；推荐不存在 404（40411）；已延后时重复调用覆盖 note 并刷新 `deferred_at`（幂等 upsert） |
| DELETE | `/api/issues/:iid/recommendation/defer` | **仅 JWT** | 移回推荐：清 `deferred_at`/`defer_note` 并刷新 `updated_at`；未延后时调用为幂等成功（返回当前项）；issue 非 `open` 返回 409（40910） |
| GET | `/api/recommendations?project_id=` | JWT / API Key | `project_id` 为空返回当前用户全部项目；非空校验项目归属 |

PUT 校验：目标 Issue 须 `state=open` 且 `workflow_status` ∈ {未设置, `todo`}（meta 行不存在视为未设置），否则 409（40910）；已延后的推荐对 Agent 封闭，PUT 返回 409（40911）；`reason` 1–500 字符、依赖 ≤20 且不含自身（40001）；目标或依赖 Issue 不存在（40405）；项目归属按 `issue → project → user_id` 链校验（40401）。依赖 Issue 所属项目也必须归属当前用户的项目（跨项目依赖允许，限同 owner）；不属于当前用户的依赖与不存在同等返回 40405，不区分「不存在」与「无权限」以免被用来枚举。

Upsert 把「读 issue、读 meta、可推荐性校验、依赖校验（含归属）、推荐 upsert、依赖替换」全部放进 `recRepo.Transaction` 同一事务，读写经 `*Tx` 变体（`FindByIDTx` / `ListByIDsTx` / `GetTx` / `ListByIssueIDsTx`，非 Tx 方法委托 Tx 版本走 `r.db`）。SQLite 单写者语义下保证校验时看到的状态与提交时一致，任一校验失败整体回滚不留半写状态。Defer/Restore/Delete 同样把「读推荐行 → 归属/延后检查 → 写」放进一个事务——尤其 Delete 的 `deferred_at` 判断必须在事务内完成：SQLite 单写者下事务内读到的是最新 committed 状态，先提交的延后一定被后续的删除请求看到，不存在「读到 active 后被 defer 仍按旧态删」的窗口。

GET 响应为 `{items: [...]}`，单条含 `issue` 摘要（id / project_id / project_name / source / sequence_number / reference / title / state / workflow_status）、`reason`、`priority`、`created_by`、时间戳与 `dependencies`（含 dep 的 reference / 标题 / 状态摘要），以及延后三字段：`status`（`active`/`deferred`，由 `deferred_at` 是否为空派生）、`deferred_at`（可空）、`defer_note`（可空，空串→null）。`reference` 与 Issue 列表同源（`buildIssueReference`：GitHub 为 `GH-<number>`，内部为 `INT-<sequence_number>`），推荐理由里常以它互相引用，前端直接展示。排序：active（`deferred_at IS NULL`）在前、延后项在后；组内先 `priority` 降序（high > medium > low，`CASE` 表达式），active 组再按 `updated_at` 降序、延后组按 `deferred_at` 降序，`issue_id` 兜底稳定序。一次全量，无分页——延后项也在列表中返回，由前端分组展示。

错误码：`ErrRecommendationNotFound`（40411）、`ErrIssueNotRecommendable`（40910）、`ErrRecommendationDeferred`（40911），均在 `errs` 对应区间 var 块。

## 延后处理（defer）

延后=压制而非遗忘：用户在 Web 端把某条推荐「以后再说」，整条推荐行（reason/priority/dependencies/created_by）原样冻结保留，仅 `deferred_at`/`defer_note`/`updated_at` 变化。语义要点：

- **仅作用于已存在的推荐行**：推荐不存在时 defer 返回 40411，不会凭空建行。
- **对 Agent 封闭**：`PUT recommendation` 撞延后项返回 40911（`ErrRecommendationDeferred`）；API Key `DELETE` 延后项同样 40911——防止 Agent 再推荐或抹掉用户的延后决定。彻底移除（删除=遗忘、可再被推荐）是 JWT 专属操作。
- **恢复只走手动**：`DELETE .../defer` 清延后字段、刷新 `updated_at` 即移回 active 组，无定时器、无自动复活。未延后时调用是幂等成功（返回当前项）。防御性校验 issue `state=open`，非 open 返回 40910（正常路径下该情形的行已被自动移除挂钩删除，此校验只兜底脏数据）。
- **自动移除挂钩对延后行照常生效**：issue 进 in_progress/done/closed 时同事务硬删推荐行，不区分 active/deferred（见下节）。

## 自动移除（硬删，不恢复）

推荐只在「还没开始做」时有意义，Issue 状态前移即删除。挂载点在 service 层，覆盖全部写路径：

- `IssueService.UpdateInternalMeta`（`issue_meta.go`）：`workflow_status` 被置为 `in_progress` / `done` 时，在同一事务内 `recRepo.DeleteTx`。ship hook 自动流转、看板拖拽、API 写状态都经此函数。
- `IssueService.UpdateInternalIssue`（`issue.go`）：`state` 变为 `closed` 时同事务删除。内部 Issue 关闭与 `BatchCloseDoneIssues` 都经此路径。
- `IssueService.upsertGitHubIssue`（`issue_sync.go`）：GitHub 同步落地 `state=closed` 时，issue 的 Create/Save 与 `recRepo.DeleteTx` 放在 `issueRepo.Transaction` 同一事务，Delete 失败回滚不留「已关闭仍被推荐」残留；非 closed 路径不变。GitHub 侧关单、Web 端关 GitHub issue（经 `UpdateInternalIssue` → GitHub API → `upsertGitHubIssue`）均覆盖。

项目删除 / Issue 级联删除由 FK `OnDelete: CASCADE` 兜底，无额外代码。

## 前端结构

- `web/src/lib/api/recommendations.ts`：`recommendationApi.list(projectId?)` / `remove(issueId)`。
- `web/src/lib/hooks/use-recommendations.ts`：`useRecommendations(projectId?)`（queryKey `['recommendations', projectId ?? 'all']`，`refetchInterval: 60000`，`refetchOnWindowFocus: true`）+ `useRemoveRecommendation()`（成功后 invalidate `['recommendations']`）。
- 弹框由 `web/src/routes/board/components/` 下三个文件组成：
  - `recommended-issues-button.tsx`：按钮 + 受控 Dialog 壳 + 移除确认 AlertDialog；持有选中态（`selectedIssueId`）、`pendingRemove` 与 `window` 键盘监听（仅 `open` 时挂载）。`DialogContent` 为 `sm:max-w-[1100px]` 双栏。
  - `recommendation-list-pane.tsx`：左栏优先级分组单行列表（`reference + 标题`），组标题吸顶，组内保持服务端 `updated_at` 降序，点击行 = 选中（不再是 Link）。分组常量 `RECOMMENDATION_PRIORITY_GROUPS` 在此定义，选中序与展示序一致。
  - `recommendation-detail-pane.tsx`：右栏选中项只读详情（不含评论/时间线）。按展示的 issueId 走 `useIssue` + `useIssueCollab`：reference/状态徽标/标题、推荐理由、前置依赖 chip、正文 `GitHubContent`、只读任务清单（含进度）、标签、`CollaborationArea readOnly`（无内容则不渲染）。底部操作栏：复制提示词（带 `reason`）、移除推荐、「打开完整详情页」Link（同标签页）。前置依赖 chip 点击切到该依赖的 peek 视图（可为推荐列表外的 issue），顶部「← 返回 <reference>」回链；peek 下隐藏推荐理由与移除入口，复制提示词不带 reason。切换选中项时父组件以 `key` 重挂载本组件，peek 自动复位。
- 键盘：`↑`/`↓`（等价 `j`/`k`）跨组移动选中并 `preventDefault`（右栏滚动交给滚轮/触控板）；`Enter` 调 `CopyIssuePromptButton` 的 `handleRef.activate()`——单模板直接复制、多模板点开选择器；`Esc` 走 base-ui 原生关闭。事件源落在 `role="menu"`/`role="alertdialog"` 浮层（提示词选择器、移除确认框）内时全部放行，按键归浮层。
- 选中项生命周期：打开默认选中最高优先级第一条；选中项被移除或 60s 轮询消失时顺延同位置（下一条），越界退上一条；列表清空自动关弹框（按钮本就随之隐藏）。
- `CopyIssuePromptButton`（`web/src/components/issues/copy-issue-prompt-button.tsx`）新增可选 `handleRef`：`CopyIssuePromptButtonHandle.activate()` 暴露「单模板复制 / 多模板开选择器」语义，供弹框 Enter 键复用；复制本身不改 issue 状态、不自动流转。
- `CollaborationArea`（`web/src/components/issues/collaboration-area.tsx`）新增 `readOnly` prop：隐藏「清空协作区」与各节删除按钮，内部 AlertDialog 不再渲染，组件方可嵌入推荐弹框右栏。
- 详情页兜底（`web/src/routes/projects/$id/issues/$iid.tsx`）：`useRecommendations(projectId)` 命中当前 issue 时（不看来源，同一 queryKey 命中弹框缓存），在正文卡片上方渲染 `RecommendationBanner`（Sparkles + 优先级徽标 + 推荐理由 + 前置依赖 Link chip）；正文卡片的 `CopyIssuePromptButton` 传入 `reason`，复制出的提示词在 `问题ID` 后带 `推荐理由：` 行。
- 挂载：`board/index.tsx` 筛选行多选 toggle 之后，`activeProjectId || undefined` 传入（看板无「全部项目」态，空串等同全量）。

## 刷新策略

推荐列表不做服务端推送，前端按 60s 轮询 + 窗口聚焦 refetch。按钮在列表为空或加载失败时整体隐藏（`return null`），删除最后一条后按钮随之消失。自动移除是写路径副作用：下次轮询时推荐自然消失，无需前端主动同步。
