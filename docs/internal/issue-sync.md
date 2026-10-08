# Issue 同步（GitHub）

`POST /api/projects/{id}/issues/sync` 与后台增量任务共用 `IssueService.syncProject`：分页拉取远端 Issues（`since` 增量），逐条 upsert `issues` 与 `issue_github_meta`，然后按 Issue 同步评论与时间线两个镜像集合。同一项目用内存锁防并发，失败经 `failSync` 落 `issue_sync_states.last_error`。

## 事务边界

原子单位是「单个 Issue 的单类集合」：评论、时间线各自在一个短事务内完成全部 upsert + 缺失清理，互不共享事务；issue 主表/meta 也不与子集合同一事务。入口是 `IssueCommentRepository.ReplaceSynced` / `IssueTimelineRepository.ReplaceSynced`，内部 = 逐条 `UpsertTx` + `DeleteMissingTx`。

- **先拉全再写**：`syncComments`/`syncTimeline` 把全部远端分页拉完并映射成 model 后才进事务。任一分页失败 → 该集合保持同步前数据，一个字也不写。
- **事务内无网络**：事务闭包里只执行数据库操作；拉取发生在事务外。
- **失败整体回滚**：事务内任一 upsert 或清理失败 → 已执行变更一并撤销，返回 error（上层 `failSync` 记失败状态）。同步计数只反映已提交的行数。

## 清理语义与来源保护

keep 集合由本轮拉取的行推导：评论按 `github_comment_id`，时间线按 `event_key`。远端空集合 = 清掉该 Issue 该类的全部镜像行。

- 评论清理只动 `source='github'`：`DeleteMissing`/`DeleteMissingTx` 带 source 过滤，`source='internal'` 的本地评论（`github_comment_id` 为负数合成 ID）永不进入清理候选集。
- 时间线事件全部是远端投影，无来源概念，清理按 event_key 集合差。
- PR 关联的 manual/synced 保护边界见 [issue-pull-request-links.md](issue-pull-request-links.md)：`DeleteMissingSynced` 只清 `link_origin=synced` 的行，由 `Upsert` 的 link_origin 只升不降约束保证。

## 冲突键

- 评论：`(issue_id, github_comment_id)`；时间线：`(issue_id, event_key)`。重复同步幂等，不产生重复行；既有行保留主键、只刷同步字段。
- 时间线 `event_key`：`gh:<event_id>`；无事件 ID 时用 `fallback:` + 事件类型/时间/actor 等字段拼接的兜底键。
