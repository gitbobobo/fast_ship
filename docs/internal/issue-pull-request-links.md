# Issue 关联 PR

`issue_pull_requests` 表镜像 Issue 关联的远端 PR：一个 Issue 挂任意多条，每条缓存最近一次从 forge 拉到的状态（state/draft/author/head/base/merged_at/closed_at/synced_at）。

provider 双源（INT-181）：`github`（github.com PR，走项目凭证）与 `forgejo`（自托管 Forgejo/Gitea PR，走部署级 Forgejo 集成）。唯一键 `(issue_id, provider, host_key, repo_full_name, number)` 天然区分两源与多实例。

## 交付范围（方向审计后定稿）

> Forgejo 关联行以 `(issue, provider, host_key, owner/repo, number)` 为业务身份：不同 Forgejo 实例的同名 PR 作为独立关联并存，同主机重复关联幂等，并刷新到当前集成快照；集成在同主机重建后，重新关联即修复死链。`forge_integration_id` 只是凭据指针，不参与身份判定。集成改 base_url 时，已关联的行仍保留原 host_key 快照，sync 时主机不一致会报错要求重新关联，不会被静默改指。

两条有意设计的信任边界（写在此处供后续审查与运维判断）：

- **sync 沿用 attach 时授权的集成凭据**，不重查项目当前绑定——attach 时校验项目与主机一致，之后凭据随快照走；项目改绑后旧行继续用原集成拉数，直到集成改指（sync 拦下）或用户显式解除。
- **同主机内跨仓库**是支持的能力（跨仓 PR 关联），作用上限 = 项目所在 Forgejo 上机器人可见的仓库。

**回滚注意**：本 PR 启动时会 DROP 旧四元唯一索引 `idx_issue_pull_requests_issue_pr` 并建五元 v2（含 host_key）。回滚到本 PR 之前的版本前，需先手工重建旧索引（`CREATE UNIQUE INDEX idx_issue_pull_requests_issue_pr ON issue_pull_requests(issue_id, provider, repo_full_name, number)`，若已有跨实例同名并存行需先清理），否则旧代码 Upsert 的 ON CONFLICT 找不到匹配唯一索引，attach 全部失败。

## 不变量

- **唯一键** `(issue_id, provider, host_key, repo_full_name, number)`。attach 是 upsert：同主机重复 attach 幂等，不产生第二行，刷新同步字段；集成快照仅在新值非空时刷新（保护既有行不被空快照调用方抹掉）。不同实例的同名 PR 是不同键，并存。
- **`link_origin` 只升不降**。`manual`（用户/agent 显式 attach）是终态；`synced`（远端同步投影产生，如 timeline cross-referenced）可被手动 attach 升级为 manual，反向不允许。Upsert 的冲突更新用 CASE 表达式实现：`excluded.link_origin='manual'` 才写 manual，否则保留原值。违反这条会让同步清理把用户挂的 PR 当成投影残留删掉。
- **清理只碰 synced**。`DeleteMissingSynced`（按 origin 过滤）是同步投影的回收口；manual 行永不进入候选集。
- **PR 状态与 `workflow_status` 无耦合**。PR 全合并不等于需求完成，二者互不驱动。

## attach 契约

body 只传 `{"url": "<PR链接>"}`。服务端解析 owner/repo/number（支持 `http(s)`、可选 `www`、`/files` 等后缀、query/fragment），路径段以 `.` 开头/结尾或是 `.`/`..` 的直接拒绝——HTTP 客户端的路径归一化可能把请求改到别的仓库。解析通过后先拉远端元数据，失败不落记录（GitHub 50200 / Forgejo 50206）。

### 两种 URL 形状与凭证

- `https://github.com/<owner>/<repo>/pull/<number>`：GitHub 既有路径。凭证按 `ProjectCredentials.pullRequestCredential`：已配代码仓库 Token（`projects.code_github_token_id`）则只用它；未配则匿名读公共 PR，不借公开仓库 Token。两种用途互不兜底，细节见 [github-credentials.md](github-credentials.md)。PR 关联是唯一允许匿名访问的调用方。
- `http(s)://<host>/<owner>/<repo>/pulls/<number>`（非 github.com 主机）：Forgejo 路径（INT-181）。一道闸门：读项目绑定的代码集成行（`projects.code_integration_id` 指向的行）并比对 `host_key`——行不存在（或 kind 不符）→ 40007「不可用或已被删除」；行存在但主机不一致 → 40007「绑定的代码集成与 PR 所在主机不一致」。通过后该行直接交 `ForgejoClientFor` 构建 SSRF 安全 client + 解密机器人 PAT 拉取——只用 forge_integrations 的部署级凭据，绝不碰项目/用户级 token。集成读取错误三类（`classifyForgeIntegrationReadErr`）：行已删/kind 不符 →「不可用或已被删除」；已有业务文案（如 PAT 解密失败）→ 原样透出；其余（锁忙/IO 等瞬时故障）→「读取 Forgejo 集成失败，请稍后重试」。绑定缺失的 40007 按事实分三种文案：代码仓在内置 GitHub（`code_github_owner` 有值、集成 id 空）时点明「仅支持关联 GitHub PR」；完全未配代码仓时提示先配置；绑定集成在另一主机时提示不一致——都不指向「改绑代码仓」的死路。

github.com/www.github.com 域内形状不符（如 `/pulls/{n}`）按无效（40001）处理，不误判成 Forgejo。允许跨仓库：`repo_full_name` 以 PR 链接/平台返回为准，可以是项目配置仓库之外的仓库。

### 集成快照

forgejo 行落 `forge_integration_id`（可空列，AutoMigrate 追加；github 行恒空串）：`repo_full_name` 只有 owner/repo 没有 host，sync 必须按快照 id 重建该行的 Forgejo 客户端。重复 attach 时 upsert 会把快照刷新为本次解析结果（换绑集成后重新绑定项目再 attach 即跟随）；**空快照不覆盖**——upsert 冲突路径对快照用 `excluded <> ''` 才写，未来空快照的调用方（同步投影等）不会把 manual 行的快照抹掉。状态映射：Forgejo `closed &&（平台标记 merged=true 或 merged_at 非空，历史/异常快照可能只有其一）→ merged`，其余 closed → closed，否则 open（与 GitHub 的 closed+merged_at 口径对齐；merged 行的 merged_at 可能为空，OpenAPI 已按此措辞）；`repo_full_name` 归一到平台返回的 canonical 名。

attach 与 sync 的失败文案不回显适配器错误原文（集成 API 绝对地址、响应体只进服务端日志）：Forgejo 状态码错误给「Forgejo API 状态码 N」+提示，传输/解析失败归一为「连接或响应异常」。GitHub 与 Forgejo 对「PR 不存在」与「无权访问」都返回 404，提示同时保留两种可能（GitHub 附「核查仓库访问范围与 Pull requests 读权限」）；限流另给提示，不断言单一原因。

**主机身份与改指防护**：行的 host_key 是 attach 时刻的主机快照，不随写入漂移。集成管理员改 base_url（改指别的主机）后，sync 按 host_key 比对发现不一致，按单行失败拦下（「该 Forgejo 集成已改指其他主机，请解除后重新关联」），旧行不被静默改指。

## sync 契约

`POST .../pull-requests/sync` 刷新全部关联，返回 `{items, failures}`。**每条关联是独立失败域**：单行拉取或保存失败记入 `failures[]`（含关联行 id 与 `repo#number` 前缀的 error），旧数据保留，不中断其余行，整体恒 200。sync 按 provider 分流：github 行按 repo 缓存客户端；forgejo 行按 `forge_integration_id` 缓存客户端（同集成一轮 sync 只构建一次，构造失败同样按 id 缓存逐行记失败）；快照缺失（空 id，提示解除重联）与未知 provider 也只算单行失败。

执行分三段（INT-175），各阶段有独立预算：访问面解析（凭证/集成读取，带缓存）串行，30s；远端拉取并发上限 4，跟随请求 context（浏览器断开即停发后续请求），30s 是整批兜底而非逐条预算；写回与响应组装串行且按关联行原序，独立 40s 预算（对齐 busy_timeout 量级——MaxOpenConns(1) 下要排唯一写连接）并脱离请求取消（`context.WithoutCancel`）：已拉到的数据照常落库，一条慢拉取撞上拉取 deadline 不会让整批零落库；写回预算到点记「写回超时」。items/failures 的顺序与创建顺序一致，与并发度无关。写回带 `synced_at` 时间守卫：并发两次 sync 时慢批次不会用旧数据覆盖已写入的更新数据（0 行命中按「已有更新数据」读回现行进 items，不记失败，记服务端日志）。attach 的 Upsert 冲突更新里快照字段逐列带同一守卫（`CASE WHEN excluded.synced_at >= 库内`），且 attach 的 `synced_at` 取拉取完成时刻——attach 与并发 sync 交错时旧快照同样不能覆盖更新数据；守卫只约束快照字段，`link_origin` 的 manual 升级与 `forge_integration_id` 换绑永不被旧快照拦下。服务端总时长上界 = 30 + 30 + 40 = 100s，web 端调用超时 120s。

写回用受 (id, issue_id, synced_at) 约束的 UPDATE 而非 GORM `Save`——后者在 0 行命中时会退化成 INSERT，把 sync 期间被并发 detach 的行原样插回。0 行命中有两种含义，用一次 SELECT 区分：行不存在（`ErrRecordNotFound`）→ 记「关联已解除」失败项；行存在但 `synced_at` 更新（`ErrSyncedAtStale`，并发 sync 已写入更新数据）→ 不算失败，读回库内现行行进 items——读回与写回共用 writeCtx，读回被预算截断记「写回超时」失败项，其余读回错误记「读回同步结果失败」。

## 读侧投影

- 详情 `GET /issues/{iid}` 带全量 `pull_requests[]`（沿用 collab 的「仅详情填充、失败降级」约定）。
- 列表项带 `pull_request_summary {total, open, merged}`，单次 `GROUP BY` 聚合，无 N+1；closed = total - open - merged。
