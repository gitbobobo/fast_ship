# Issue 关联 PR

`issue_pull_requests` 表镜像 Issue 关联的远端 PR：一个 Issue 挂任意多条，每条缓存最近一次从 forge 拉到的状态（state/draft/author/head/base/merged_at/closed_at/synced_at）。

## 不变量

- **唯一键** `(issue_id, provider, repo_full_name, number)`。attach 是 upsert：重复 attach 幂等，不产生第二行，同时刷新同步字段。
- **`link_origin` 只升不降**。`manual`（用户/agent 显式 attach）是终态；`synced`（远端同步投影产生，如 timeline cross-referenced）可被手动 attach 升级为 manual，反向不允许。Upsert 的冲突更新用 CASE 表达式实现：`excluded.link_origin='manual'` 才写 manual，否则保留原值。违反这条会让同步清理把用户挂的 PR 当成投影残留删掉。
- **清理只碰 synced**。`DeleteMissingSynced`（按 origin 过滤）是同步投影的回收口；manual 行永不进入候选集。
- **PR 状态与 `workflow_status` 无耦合**。PR 全合并不等于需求完成，二者互不驱动。

## attach 契约

body 只传 `{"url": "<PR链接>"}`。服务端解析 owner/repo/number（支持 `http(s)`、可选 `www`、`/files` 等后缀、query/fragment），路径段以 `.` 开头/结尾或是 `.`/`..` 的直接拒绝——HTTP 客户端的路径归一化可能把请求改到别的仓库。解析通过后先调 GitHub 拉元数据，失败不落记录（50200）。

允许跨仓库：`repo_full_name` 以 PR 链接为准，可以是项目配置仓库之外的仓库。凭证按 `resolvePullRequestCredential` 三选一：项目已配独立 PR 访问 Token（`github_pr_token_encrypted`）则只用 PR Token；未配则沿用项目 Token；项目未配 GitHub 走匿名——PR 关联是唯一允许匿名访问的调用方。细节见 [github-credentials.md](github-credentials.md)。attach 与 sync 的失败文案会标明本次凭证来源；GitHub 对「PR 不存在」与「无权访问」都返回 404，权限类错误附「核查仓库访问范围与 Pull requests 读权限」的提示，不断言单一原因。

## sync 契约

`POST .../pull-requests/sync` 逐条刷新，返回 `{items, failures}`。**每条关联是独立失败域**：单行拉取或保存失败记入 `failures[]`（含关联行 id 与 `repo#number` 前缀的 error），旧数据保留，不中断其余行，整体恒 200。

写回用受 (id, issue_id) 约束的 UPDATE 而非 GORM `Save`——后者在 0 行命中时会退化成 INSERT，把 sync 期间被并发 detach 的行原样插回。0 行命中即记「关联已解除」失败项。

## 读侧投影

- 详情 `GET /issues/{iid}` 带全量 `pull_requests[]`（沿用 collab 的「仅详情填充、失败降级」约定）。
- 列表项带 `pull_request_summary {total, open, merged}`，单次 `GROUP BY` 聚合，无 N+1；closed = total - open - merged。
