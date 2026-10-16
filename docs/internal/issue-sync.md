# Issue 同步（GitHub）

`POST /api/projects/{id}/issues/sync` 与后台增量任务共用 `issueMirror.Sync`（`server/internal/service/issue_mirror.go`）：分页拉取远端 Issues（`since` 增量），逐条 upsert `issues` 与 `issue_github_meta`，然后按 Issue 同步评论与时间线两个镜像集合；列表循环之后还有对空水位 Issue 的有界兜底重拉（`healStaleCollections`，见「集合拉取水位」）。同一项目用内存锁防并发，失败经 `failSync` 落 `issue_sync_states.last_error`。push 路径（创建/更新 Issue、发评论）经 `issueMirror.upsertGitHubIssue`/`refreshTimelineAfterPush`/`markCollectionsStaleAfterPush`/`recordPushedComment` 把远端响应写回本地，不经同步锁。

## 集合拉取水位

`issue_github_meta` 上的 `collections_synced_updated_at` / `collections_synced_comments` 记录上次成功镜像评论/时间线集合时远端 Issue 的 `updated_at` 与评论数（不在 meta upsert 的覆盖列里）。逐条处理时 `needsCollectionSync` 按它判断：远端 `updated_at` 晚于水位，或远端评论数与水位不一致——`updated_at` 只有秒级精度，同一秒内新增的评论靠评论数发现——才请求该 Issue 的评论/时间线接口；否则整段跳过。水位为空（新 Issue、加列前的旧 meta 行）一律重拉一次做 bootstrap。

完整镜像是一个原子单元：一次同步先在事务外拉完评论与时间线两个集合（`fetchComments` / `fetchTimeline`），再在**同一个数据库事务**里复核失效版本、替换评论、替换时间线并推进水位（`writeFullCollections`：`AdvanceCollectionsWatermarkIfVersionTx` + `ReplaceSyncedTx`×2）。不能拆成多个事务：并发的 push 路径也写集合且不经同步锁，若拆开，push 的写回可以落在两轮取数之间、以旧正文覆盖镜像，而最后提交的时间线事务仍会推进水位——留下「旧镜像 + 已确认水位」，跳过判据（远端 `updated_at`/评论数 vs 水位）再也发现不了该 Issue，缺失内容不再自愈（INT-104 第三轮审查的 C<P<T 反例：评论替换(新正文) → push 写回(同 id 旧正文)+清水位 → 时间线替换+推进水位；评论与时间线之间隔着取数网络往返，窗口真实存在）。

但原子性只保证「集合与水位一致」，保证不了「并发 push 的失效意图存活」：若完整镜像拿的是 push 之前的旧快照、又在 push 之后提交，它仍会把 push 清空的水位写回非空，失效信号被抹掉（INT-105 第二轮审查的逃逸序列）。所以每次完整镜像在取数前记下 `collections_invalidated_version`，写库事务内复核：版本被 push 递增过就放弃这次旧快照、保留空水位待重拉（`writeFullCollections` 返回 advanced=false）。版本号而非时间戳，是为了辨别同一秒内的失效。复核与写入在同一事务内，MaxOpenConns=1 下与并发 push 的失效事务串行。

对集合镜像的每一次本地写入，要么是完整镜像（同事务复核版本并推进水位），要么是部分写入（同事务写单集合并让水位失效，入口 `writePartialCollections`：`InvalidateCollectionsTx` 清空水位、递增 `collections_invalidated_version`，并只在健康→失效时分配兜底队列位置——等待中的重复失效不改变它）。push 路径的三类部分写入都走后者：时间线刷新（`refreshTimelineAfterPush`：`ReplaceSyncedTx`）只重拉了时间线、没有重拉评论，不能推进；评论写回（`recordPushedComment`：评论 upsert + issue 保存 + meta upsert）只把远端响应里的一条评论写回本地，而 push 的响应可能比后台同步刚镜像的远端状态旧；仅状态变更的关闭/重开（`markCollectionsStaleAfterPush`，INT-105）连集合都没写、也不做任何网络取数，只让水位失效——看板「关闭全部」与发货钩子关单因此每条 Issue 只承担一次状态 PATCH 的往返，`closed` 等时间线事件留给增量同步补齐。三者失效后，该 Issue 下一次被增量列表返回时后台同步会重拉它的两个集合；若它落到增量窗口之外，则由列表之后的有界兜底重拉补齐（见下）。

跳过时响应里的 `SyncedCommentCount`/`SyncedTimelineCount` 取该 Issue 现有镜像行数（`CountSynced`/`Count`），与重拉路径一样表达「镜像集合大小」而非本轮写入行数。水位只在评论和时间线都成功镜像后随完整镜像事务推进；任一集合取数失败本轮一个集合也不写、水位不动，下一轮重试。

已知限制：判据只认 `updated_at` 与评论数两个信号。远端在同 1 秒内先变更、同步恰好夹在中间、又发生第二次变更时，两次变更的 `updated_at` 与评论数可能完全相同，不会被本轮发现，要等该 Issue 下次变更才补上（窗口为亚秒级）。另外，不改动 `updated_at` 的远端时间线事件（如交叉引用、提交引用）也不会单独触发重拉。

空水位是**持久信号**：它不仅被增量列表消费，还有独立于列表的兜底消费路径。`Sync` 在增量列表循环结束后执行 `healStaleCollections`（INT-105）：经 `IssueGitHubMetaRepository.ListStaleCollectionsByProject` 取本项目 `collections_synced_updated_at IS NULL` 的 meta，按 `collections_heal_queued_at ASC` 取至多 `staleCollectionsHealLimit`（50）条，逐个 `GetIssue` 取远端快照、`fetchComments` + `fetchTimeline` + `writeFullCollections` 完整重拉两个集合并推进水位（水位用 `GetIssue` 返回的远端 `updated_at`/评论数，本地 `issues.updated_at` 会被 GORM 自动更新与本地工作流操作污染，不是远端时间戳）。因此即便某 Issue 落到增量窗口之外——并发 Sync 已按旧数据跳过它、又用更大的 `maxIssueUpdatedAt` 推进游标越过它的 `updated_at`——它也会在后续同步里被兜底补齐；配合完整镜像的失效版本复核，哨兵式的时间线缺失不再出现（INT-105）。兜底上限是防 GitHub 限流的有界补充而非收敛手段：空水位也可能来自加列前的旧 meta 行，会在多轮同步里逐步排空；`collections_heal_queued_at` 是持久队列位置：条目健康→失效（push 清空水位）或新 meta 创建时首次分配，等待期间再被 push（仍为空水位）不改变它（所以新进条目插不到旧条目前），每次实际兜底尝试后才移到队尾；完整镜像推进水位时清空，候选按它升序取（NULL 最前，仅剩加列前存量行）。这样每个空水位条目最终都会被尝试（队列位置是墙上时钟，依赖系统时间正常前进；持续回拨只会推迟重试，不影响正确性），取数/写库失败只跳过并记 warn、保留空水位待下轮重试；meta 悬空（issue 行已删）同样跳过。查询本身失败才走 `failSync`；`ctx` 取消时保留本轮已完成的部分。`issues.auto_sync_enabled=false` 的部署没有后台同步，增量与兜底都只在手动/触发同步时执行。

## 事务边界

集合镜像的原子单位是「单个 Issue 的评论 + 时间线两个集合 + 集合水位」：完整镜像在一个事务里复核失效版本、完成两个集合的全部 upsert + 缺失清理并推进水位，入口是 `writeFullCollections`（`AdvanceCollectionsWatermarkIfVersionTx` + `IssueCommentRepository.ReplaceSyncedTx` / `IssueTimelineRepository.ReplaceSyncedTx`）；部分写入在同一事务里写单个集合或空写，并让水位失效（入口 `writePartialCollections` → `InvalidateCollectionsTx`）。理由见「集合拉取水位」。兜底重拉（`healStaleCollections`）逐 Issue 复用 `writeFullCollections`，不新增事务边界，并发正确性与增量列表路径一致。issue 主表/meta 的其余字段（`upsertGitHubIssue`）不与子集合同一事务。事务内部 = 逐条 `UpsertTx` + `DeleteMissingTx`。

- **先拉全再写**：`fetchComments`/`fetchTimeline` 把两个集合的全部远端分页拉完并映射成 model 后才进事务。任一分页失败 → 本轮两个集合都保持同步前数据，一个字也不写（有意全有或全无：同步整体经 `failSync` 记 `failed`，下一轮两个集合一起重拉）。
- **事务内无网络**：事务闭包里只执行数据库操作；两个集合的拉取都发生在事务外。
- **失败整体回滚**：事务内任一 upsert 或清理失败 → 已执行变更一并撤销，返回 error（上层 `failSync` 记失败状态）。同步计数只反映已提交的行数。取数都在事务外完成也让每 Issue 的写事务从 2 个降到 1 个。

## 清理语义与来源保护

keep 集合由本轮拉取的行推导：评论按 `github_comment_id`，时间线按 `event_key`。远端空集合 = 清掉该 Issue 该类的全部镜像行。

- 评论清理只动 `source='github'`：`DeleteMissing`/`DeleteMissingTx` 带 source 过滤，`source='internal'` 的本地评论（`github_comment_id` 为负数合成 ID）永不进入清理候选集。
- 时间线事件全部是远端投影，无来源概念，清理按 event_key 集合差。
- PR 关联的 manual/synced 保护边界见 [issue-pull-request-links.md](issue-pull-request-links.md)：`DeleteMissingSynced` 只清 `link_origin=synced` 的行，由 `Upsert` 的 link_origin 只升不降约束保证。

## 冲突键

- 评论：`(issue_id, github_comment_id)`；时间线：`(issue_id, event_key)`。重复同步幂等，不产生重复行；既有行保留主键、刷新全部镜像字段。
- 注意：评论 upsert 的更新列包含 `author_user_id` 与 `idempotency_key`，而同步映射不带这两个字段——应用内发到 GitHub 的评论被同步覆盖时幂等键会被清空；防重发兜底靠评论正文里的 `fast-ship-hook:` marker（见 `service/issue_comments.go`）。
- 时间线 `event_key`：`gh:<event_id>`；无事件 ID 时用 `fallback:` + 事件类型/时间/actor 等字段拼接的兜底键。
