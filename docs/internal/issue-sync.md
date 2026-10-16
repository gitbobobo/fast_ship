# Issue 同步（GitHub）

`POST /api/projects/{id}/issues/sync` 与后台增量任务共用 `issueMirror.Sync`（`server/internal/service/issue_mirror.go`）：分页拉取远端 Issues（`since` 增量），逐条 upsert `issues` 与 `issue_github_meta`，然后按 Issue 同步评论与时间线两个镜像集合。同一项目用内存锁防并发，失败经 `failSync` 落 `issue_sync_states.last_error`。push 路径（创建/更新 Issue、发评论）经 `issueMirror.upsertGitHubIssue`/`refreshTimelineAfterPush`/`recordPushedComment` 把远端响应写回本地，不经同步锁。

## 集合拉取水位

`issue_github_meta` 上的 `collections_synced_updated_at` / `collections_synced_comments` 记录上次成功镜像评论/时间线集合时远端 Issue 的 `updated_at` 与评论数（不在 meta upsert 的覆盖列里）。逐条处理时 `needsCollectionSync` 按它判断：远端 `updated_at` 晚于水位，或远端评论数与水位不一致——`updated_at` 只有秒级精度，同一秒内新增的评论靠评论数发现——才请求该 Issue 的评论/时间线接口；否则整段跳过。水位为空（新 Issue、加列前的旧 meta 行）一律重拉一次做 bootstrap。

时间线镜像替换与水位变更在同一个数据库事务里提交（`syncTimeline`：`ReplaceSyncedTx` + `AdvanceCollectionsWatermarkTx`）：并发的 push 路径也会替换时间线且不经同步锁，两者若分开提交会出现「旧镜像 + 新水位」，缺失内容再也不会被判据发现。对集合镜像的每一次本地写入，要么在同一个事务里推进水位（完整镜像），要么在同一个事务里清空水位（部分写入，入口 `writePartialCollections`）。push 路径的两类部分写入都走后者：时间线刷新（`refreshTimelineAfterPush`：`ReplaceSyncedTx`）只重拉了时间线、没有重拉评论，不能推进；评论写回（`recordPushedComment`：评论 upsert + issue 保存 + meta upsert）只把远端响应里的一条评论写回本地，而 push 的响应可能比后台同步刚镜像的远端状态旧。两者清空水位后，该 Issue **下一次被增量列表返回时**后台同步才会重拉它的两个集合。

跳过时响应里的 `SyncedCommentCount`/`SyncedTimelineCount` 取该 Issue 现有镜像行数（`CountSynced`/`Count`），与重拉路径一样表达「镜像集合大小」而非本轮写入行数。水位只在评论和时间线都成功镜像后随时间线事务推进；任一集合失败水位不动，下一轮重试。

已知限制：判据只认 `updated_at` 与评论数两个信号。远端在同 1 秒内先变更、同步恰好夹在中间、又发生第二次变更时，两次变更的 `updated_at` 与评论数可能完全相同，不会被本轮发现，要等该 Issue 下次变更才补上（窗口为亚秒级）。另外，不改动 `updated_at` 的远端时间线事件（如交叉引用、提交引用）也不会单独触发重拉。push 路径清空的集合水位只在该 Issue 下次被增量列表（`since` 窗口）返回时才有机会生效——若同一轮别的 Issue 把项目水位推得更远，该 Issue 暂时不会被列出，本地镜像要等它下次远端变更才补齐（这一点改动前就存在）。

## 事务边界

评论的原子单位是「单个 Issue 的单类集合」：一个短事务内完成全部 upsert + 缺失清理，入口是 `IssueCommentRepository.ReplaceSynced`。时间线同构（入口 `IssueTimelineRepository.ReplaceSyncedTx`），但额外与集合水位变更（`AdvanceCollectionsWatermarkTx` / `ClearCollectionsWatermarkTx`）共享同一事务，理由见「集合拉取水位」；issue 主表/meta 的其余字段不与子集合同一事务。事务内部 = 逐条 `UpsertTx` + `DeleteMissingTx`。

- **先拉全再写**：`syncComments`/`syncTimeline` 把全部远端分页拉完并映射成 model 后才进事务。任一分页失败 → 该集合保持同步前数据，一个字也不写。
- **事务内无网络**：事务闭包里只执行数据库操作；拉取发生在事务外。
- **失败整体回滚**：事务内任一 upsert 或清理失败 → 已执行变更一并撤销，返回 error（上层 `failSync` 记失败状态）。同步计数只反映已提交的行数。

## 清理语义与来源保护

keep 集合由本轮拉取的行推导：评论按 `github_comment_id`，时间线按 `event_key`。远端空集合 = 清掉该 Issue 该类的全部镜像行。

- 评论清理只动 `source='github'`：`DeleteMissing`/`DeleteMissingTx` 带 source 过滤，`source='internal'` 的本地评论（`github_comment_id` 为负数合成 ID）永不进入清理候选集。
- 时间线事件全部是远端投影，无来源概念，清理按 event_key 集合差。
- PR 关联的 manual/synced 保护边界见 [issue-pull-request-links.md](issue-pull-request-links.md)：`DeleteMissingSynced` 只清 `link_origin=synced` 的行，由 `Upsert` 的 link_origin 只升不降约束保证。

## 冲突键

- 评论：`(issue_id, github_comment_id)`；时间线：`(issue_id, event_key)`。重复同步幂等，不产生重复行；既有行保留主键、刷新全部镜像字段。
- 注意：评论 upsert 的更新列包含 `author_user_id` 与 `idempotency_key`，而同步映射不带这两个字段——应用内发到 GitHub 的评论被同步覆盖时幂等键会被清空；防重发兜底靠评论正文里的 `fast-ship-hook:` marker（见 `service/issue_comments.go`）。
- 时间线 `event_key`：`gh:<event_id>`；无事件 ID 时用 `fallback:` + 事件类型/时间/actor 等字段拼接的兜底键。
