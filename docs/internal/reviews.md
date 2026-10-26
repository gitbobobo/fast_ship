# PR 审查（INT-135）

按**项目**开启的 GitHub PR 自动审查：webhook 触发后分**代码审查**与**视觉审查**
两段执行——每个代码审查组合各在 T3 新建 worktree 线程，全部通过且 PR 有界面
改动时才衔接视觉审查（项目根目录只读线程）。审查结论以 GitHub App 身份写回：
两个 commit status context（`代码审查`/`视觉审查`）、两条顶层评论、按组合的
行内 review。Agent 不持任何 GitHub 凭证，PR 元数据、diff、历史、截图全部经
`/api/review-agent/*` 运行令牌端点由 Fast Ship 代取。

**边界**：INT-135 取代了原「通用自动化」（cron / Issue 事件 / GitHub PR 触发器
+ 自由提示词，INT-90/93/118），后者整体删除：旧七张表
（`automations`/`automation_triggers`/`automation_runs`/`automation_run_tokens`/
`github_pr_reviews`/`github_pr_debounces`/`github_pr_seen_shas`）由
`cmd/server/migrate_review.go` 在 AutoMigrate 前直接 DROP，不迁数据。本 PR
新增的七张审查表（`review_runs`/`review_run_tokens`/`review_pull_requests`/
`review_assets`/`review_debounces`/`review_seen_shas`/`project_review_settings`）
在开发期结构演进过一次（账本按项目分区），迁移只在检测到旧结构
（`review_pull_requests` 主键缺 `project_id`）时整表重建；新结构的库每次启动
原样保留，不清数据。
审查的流程类机制（触发、防抖、排队、工作区、历史、评论、状态、视觉衔接）由
系统写死保证，提示词只保留审查判断本身，不给用户自由发挥。不做评论命令、
不做 Checks API、不审 draft 与 fork PR。站内通知保留但只用于审查系统故障
（派发失败、超时、工作区回收失败），审查发现不发通知。单进程部署假设。

**账本按项目分区**：GitHub 产出（commit status、顶层评论、行内 review、
每 PR 进程锁）按 (集成, 仓库) 归一——同一集成的同一仓库任一时刻只有一个
项目 enabled。但执行态（运行、令牌、防抖、seen SHA、PR 总账）全部按
`project_id + integration_id` 分区：项目换成另一个项目接管同一仓库、或改绑
另一集成时各记一本账，互不吞事件、不混结果。接管语义见「开启前置与同仓库
唯一」；集成维度是 INT-167 加的（Forgejo 第二集成），存量语义见
docs/internal/forge-integrations.md。

**审查仓库 = 代码仓库**：项目可以单独配置代码仓库
（`projects.code_github_owner/code_github_repo`，musiver 场景：公开仓库放
Issue 与 Release、无代码；代码在另一仓库）。`project.CodeRepo()` 表达的是
「这个项目审查哪个仓库」的候选值：两个 code 字段都非空时用显式配置，否则
回落公开仓库；Issue 同步/发版/发货仍只走公开仓库，两者互不影响。集成身份
（`projects.code_integration_id`，空 = 内置 `github-default`）也算审查对象
的一部分：owner/repo 字面相同、换了实例就是换了仓库。

### 审查仓库的读取口径

同一个事实存了两份：项目行的 `CodeRepo()`（候选值）与设置行的
`repo_full_name`（生效值，小写）；INT-167 起伴随一份集成身份快照
`integration_id`（归一值：空绑定记 `github-default`，Forgejo 记
`forge_integrations.id`）。三条写入路径保证它们恒等：项目改仓库/改绑集成时
`ProjectRepository.Update` 同事务停用审查并刷新两份快照（disabled 行也刷
新），保存审查设置时 `UpdateSettings` 用 `CodeRepo()` 与项目绑定归一值写入
快照并在事务内复核。因此**不需要**在两者之间做「谁更可信」的兜底，需要的
是按用途选列：

| 用途 | 读什么 | 允许出现的位置 |
| --- | --- | --- |
| 写入侧：生成快照、以及必须等于「即将写入的键」的预占预判 | `CodeRepo()` + `code_integration_id` 归一值 | `UpdateSettings`、`ProjectRepository.Update`、`reviewPrerequisites`、`reviewTakenBy`、`reviewOccupiers` |
| 运行侧：基于已有状态做判定 | 设置行 `(integration_id, repo_full_name)`（不按 `enabled` 过滤，除非该判定本身要求已启用） | 触发反查、防抖复核、同步过滤/准入、rerun、记录与总览的「当前仓库」 |
| 展示：保留用户输入的原样大小写 | `CodeRepo()` | 总览的 owner/repo、`GetSettings.repo_full_name` |

`service` 包里的 `TestReviewChainReadsReviewRepoFromSettingsSnapshot` 用 AST
扫描把这条规则钉死：`review_*.go` 里出现未列入白名单的 `CodeRepo()` 读取即
失败。新增判定点时按上表选列，不要回读 live 字段，也不要给运行侧加 fallback
（没有设置行的项目不可能有账本/运行/防抖行）。

## 数据模型

七张表（`server/internal/model/review.go`，ID=UUID，时间一律 UTC）：

| 表 | 关键列 | 说明 |
| --- | --- | --- |
| `project_review_settings` | PK `project_id`；`user_id`；`integration_id/repo_full_name`（审查对象快照，INT-167）；`enabled`；`trigger_opened/synchronize/ready_for_review/reopened`；`code_combinations_json`/`visual_combinations_json` | 项目级开关与组合。组合元素：`environment_id`、`instance_id`（harness/provider 实例）、`model`、`options`（canonical `[{id,value}]` 数组，思考强度等）、`t3_project_id`（保存时由 `project_t3_bindings` 快照，不信任客户端回传）。代码组合启用时 1–5 个必填；视觉组合 0–3 个，0 个即从不启动视觉审查。`integration_id` 是归一值（空绑定 = `github-default`），保存设置与改绑项目时同点写入。 |
| `review_runs` | `kind(code\|visual)`、`repo_full_name/pr_number/head_sha/round_number/integration_id`、`installation_id`、`trigger_action`、`pr_title/pr_url/base_ref/head_ref`、组合列 `environment_id/t3_project_id/instance_id/model/model_options_json`、预分配 `thread_id/launch_command_id/initial_message_id`、`status(queued\|dispatched\|running\|completed\|failed\|timed_out\|cancelled)`、`dispatch_started_at/dispatched_at/started_at/finished_at/deadline_at`、`error_code/error_message`、`prep_retry_count`、`summary/result_json`、`review_id/posted_at`、`worktree_path/worktree_branch/worktree_state/worktree_reason/worktree_error`、`stop_attempted_at`、`archived_at` | 一次审查运行 = 一轮 × 一个组合。同一 (集成, repo, pr, head_sha) 的代码与视觉运行共享 `round_number`（视觉是同一轮内的第二段）。`integration_id` 是普通列不入键（INT-167）：按集成选 client、按集成隔离同名仓库的历史。`dispatched_at` 在启动 RPC 结果确认或 Agent 首次持令牌鉴权时写入，为空 = 线程是否建成未确认。`prep_retry_count` 是 T3 工作区准备失败后的自动重试次数（INT-147，须持久化：进程重启不能把已用掉的重试名额清零）。`worktree_branch` 是线程投影读到的本轮专属分支名（INT-170，「空才补」回写，与 `worktree_path` 同款——线程 404 后的两步回收靠行内快照；两列都是新增可空列，升级前已有的行是 NULL，「空才补」条件须兼容 `IS NULL`，否则旧库行静默写不进、快照丢失）。`worktree_state` 值域 `none/removed/failed/branch_pending`（INT-170：`branch_pending` = 工作树已删、本轮专属分支仍在，服务端无删分支 RPC，复查登记 ref 全集、分支消失才 `removed`）。`worktree_reason` 是回收问题分类码（INT-170：`archive_failed/thread_read/registry_error/registry_incomplete/remove_budget_pending/remove_unavailable/ownership_error/ownership_mismatch/worktree_remove/remove_uncertain/branch_pending/env_binding/branch_unknown`；`removed` 时清空；`ownership_error` = 归属核验所需的本地 DB 读连续失败，区别于 `registry_error` 的 T3 登记读失败；`remove_budget_pending` = 删除执行的行内预算耗尽待重试，`remove_unavailable` = 删除请求未送达远端待重试，两者与 `registry_incomplete` 的登记读取不完整分键），聚合通知按（环境, 仓库, 原因）分键查询受影响清单。`archived_at` 是终态后线程归档写回的时刻；归档有限重试超限后也写入（不再重试），失败原因见 `worktree_error` 与 `review_cleanup_failed` 聚合通知。 |
| `review_run_tokens` | PK `run_id`；`token_hash`（UNIQUE）；`expires_at`；`consumed_at` | 每次运行一个 `fsr_` 令牌，只存完整令牌的 SHA-256。 |
| `review_pull_requests` | PK `(project_id, repo_full_name, pr_number, integration_id)`；`user_id/installation_id`；`round_number/head_sha`；`code_comment_id/visual_comment_id`；`code_status/visual_status`；`visual_screens_fingerprint`（`c:<urlFP>:<contentFP>` / `u:<urlFP>`，见「视觉审查衔接」）；`sync_pending/sync_epoch/sync_error` | 一个 PR 的审查总账：评论落点、最近一次推送的 commit status 结论、视觉截图指纹、事务外同步标记（置 pending 时 epoch+1，清标记按开始时 epoch 条件写）。主键含 `project_id` 与 `integration_id`：同仓库换项目接管或改绑集成后各一本账。 |
| `review_assets` | 唯一键 `(integration_id, repo_full_name, pr_number, head_sha, source_url)`；`position`；`label`；`storage_path`；`mime_type`；`size_bytes` | 视觉审查截图：PR 描述里的图片链接经截图下载面（`NewReviewMediaService`）下载后本地落盘。内容按 (集成, 仓库) 归一共享（同一 head 同一批截图），不按项目分区；同名仓库在两个集成下是不同实例的图源，各留一行。 |
| `review_debounces` | PK `(project_id, repo_full_name, pr_number, integration_id)`；`head_sha/action/installation_id/pr_title/pr_url/base_ref/head_ref`；`fire_at` | 防抖窗口。窗口内新推送只更新 head 并推后 `fire_at`；固定 60s，不提供配置。 |
| `review_seen_shas` | PK `(project_id, repo_full_name, pr_number, head_sha, integration_id)`；`pr_updated_at`；`accepted_at` | 某项目已受理过的 head SHA：force-push 回旧 SHA 不再开轮。按 (项目, 集成) 分区——接管或改绑后新键对同仓库同一 SHA 重新受理。 |

所有审查键里的 `integration_id` 都是**归一值**（INT-167）：内置 GitHub 记
字面量 `github-default`（`model.ForgeIntegrationBuiltinGitHubID`），Forgejo
记 `forge_integrations.id`；写侧一律经归一（项目空绑定折成
`github-default`），库里不存空串。存量行由 `migrate_review_integration.go`
一次性回填 `github-default`——三张键表整表重建（SQLite 不能 ALTER 主键）、
三张加列表 ALTER + UPDATE，幂等可重复执行。

外键：`project_review_settings` → `projects`/`users` CASCADE；
`review_run_tokens.run_id` → `review_runs` CASCADE；`review_runs` →
`users`/`projects` CASCADE。T3 项目/线程/命令 ID 不建 FK（远端生命周期
不由本库管理）。

`project_review_settings` 的 `(integration_id, repo_full_name)` 是项目
**实际审查的代码仓库 + 集成**快照（`project.CodeRepo()` 小写；集成取绑定
归一值）：同仓库唯一占用、webhook 反查、同步过滤都只认这对键。项目行新增
`code_github_owner/code_github_repo` 两列（空 = 与公开仓库相同），
`ProjectRepository.Update` 检测到 `CodeRepo()` 或绑定变更时同事务停用审查
并刷新两份快照。

所有表的 `repo_full_name` 一律小写：webhook 侧取 payload 规范名、rerun 侧
取启用设置行的 `repo_full_name` 快照，写库前统一 `strings.ToLower`——GitHub
API 本身大小写不敏感，但 SQLite BINARY 比较会把两种大小写当两本账（防抖、
seen SHA、取消旧轮全部失效）。

关键索引（`cmd/server/main.go` 的 `createBootIndexes`）：

- `review_runs(project_id, repo_full_name, pr_number, round_number)` —— 审查
  记录按轮聚合；
- `review_runs(project_id, repo_full_name, pr_number, head_sha)` —— 按轮与
  SHA 查运行（读路径已带 project_id 前缀）；
- `review_runs(user_id, created_at DESC)` —— 用户维度队列；
- `review_runs(deadline_at) WHERE status IN ('dispatched','running')` —— 超时扫描；
- `review_runs` UNIQUE —— 同 head 同轮同组合的重复插入被吃掉（轮次入键，
  见「开轮」；组合去重见「设置」）；
- `review_pull_requests(project_id, updated_at DESC)` 与部分索引
  `(sync_pending) WHERE sync_pending = 1`；
- `review_debounces(fire_at)` —— 到期扫描；
- `project_review_settings(user_id)`；
- `project_review_settings(integration_id, repo_full_name) WHERE enabled = 1`
  UNIQUE —— 同仓库唯一占用的并发兜底（INT-167 起带 integration_id，同名
  仓库在两个集成下可各自被一个项目启用）；
- `review_assets(integration_id, repo_full_name, pr_number, head_sha,
  source_url)` UNIQUE（模型标签建出）—— 截图源地址去重，integration_id
  键首。

## 触发与防抖（`review_webhook.go` 的 webhook 事务）

两个公开入口共用同一条幂等壳与触发事务（`handlePlatformWebhook` →
`handleWebhookTx` → `handleReviewTriggerTx`）：

- `POST /api/github/webhooks`（INT-93 起）：`pkg/githubapp` 验签，密钥未配置
  503、签名不符 40107、`X-GitHub-Delivery` 重复投递幂等 202；集成身份固定
  传内置 `github-default`。
- `POST /api/forge/:id/webhooks`（INT-167 任务 3，`forge_webhook.go`）：按
  集成行解密验签密钥（集成不存在 404、未配置 503、密文损坏 50000），
  `X-Forgejo-Signature` 纯 hex HMAC-SHA256 常量时间比对（缺失/不等/带
  `sha256=` 前缀一律 40107），`X-Forgejo-Delivery` 幂等；集成身份是
  `forge_integrations.id`。任何写操作都在验签之后。delivery 幂等账与
  GitHub 共用 `github_webhook_deliveries`（id 都是平台 UUID，碰撞概率为零）。
  动作名入口归一：Forgejo 的 `synchronized` 折成 GitHub 拼法
  `synchronize`（GitHub 动作集合里没有前者，零影响）。

幂等登记之后同一事务内：

1. 只处理 `pull_request` 的 `opened` / `synchronize` / `ready_for_review` /
   `reopened`；其余事件与 action 忽略。closed（含合并）单独走取消分支。
2. draft、fork（head repo ≠ base repo）忽略。
3. 反查项目：`project_review_settings.enabled AND integration_id = ? AND
   repo_full_name = LOWER(?)`（按设置行的 (集成, 仓库) 快照；GitHub webhook
   传 `github-default`、Forgejo webhook 传集成 id，同一集成内最多一个，见
   「同仓库唯一」）。没开启 → 忽略，不取消、不开轮。公开仓库 ≠ 代码仓库时，
   只有代码仓库的事件能反查到项目；公开仓库的事件静默忽略。两个集成的
   同名仓库互不误触：设置行快照键不同，反查天然分区。
4. action 未被该项目触发条件勾选 → 忽略（不取消、不改 seen）。
5. 过期 SHA 判定（全部按 `project_id + repo + pr`，其他项目的同仓库残留行
   不进入判定）：当前 head 优先取未开火的防抖行，否则取本项目
   `review_pull_requests.head_sha`；payload `pull_request.updated_at` 严格早于
   已受理 head 的 `pr_updated_at` → 忽略；同刻同 head → 重复投递早退；
   **同刻异 head 是歧义**（秒级粒度下同秒连推与乱序投递/旧快照分不出）——
   **任一侧缺 `updated_at`/`pr_updated_at` 的异 head 同样按歧义处理**（旧
   数据行与被代理剥掉字段的投递都判不出新旧，放行会把总账回滚到旧
   head，而旧 head 在 seen 里记为「非当前」后同 SHA 重投被去重永吞）——
   事务回滚并交调用方现拉实时 PR 复核：live head 与 payload head 一致才以
   `tiedBase`（绑观察到的总账 head）重进受理；live 指向别处或 PR 已关 →
   丢弃并**补登幂等账**（确定态丢弃后 delivery 落账，平台手动 redeliver
   同 GUID 直接 202 去重，不重走实拉）；实拉/构造客户端失败按
   `deterministicReviewAccessErr` 归类——确定性失败（401/403/404、集成
   缺失、密文损坏）重投结果相同 → 同样判丢弃并补登幂等账，瞬时错误
   （5xx/网络/限流 403）才返回错误（handler 500 → 平台重投，GitHub 无
   轮询兜底不静默丢）；重进期间总账又前进 →
   tiedBase 失配、哨兵再触发按新快照重验（上限 4 次，仍不收敛则丢弃
   并补登幂等账——手动 redeliver 重走歧义循环没有意义）。总账不
   回滚到疑似旧 head。该 SHA 已在本项目
   `review_seen_shas` 但不是当前 head → 忽略；`action=opened` 且当前 head
   非空 → 忽略；该 SHA 本项目已有运行 → 忽略。force-push 回已见过的 SHA
   不再开轮。
6. 写 `review_seen_shas`（含 `project_id` 与 `pr_updated_at`）。
7. 新 head ≠ 当前 head：把该 PR 下**本项目**所有未终态运行置 `cancelled`、
   `error_code='superseded'`、`finished_at=now`，消费其令牌；删除该 PR 本项目的
   防抖行（旧 SHA 不得开火）；置本项目总账 `sync_pending=true`（其他项目的
   残留由各自停用/接管路径收敛，webhook 不动）。
8. 写/更新防抖行（同一 (project, repo, pr) 只一行），`fire_at=now+60s`。

事务内零 GitHub/T3 调用；提交后非阻塞 kick 调度循环。

投递本身也可能失败：GitHub 发出但没送达（公网隧道超时/502）时 GitHub App
不会自动重试，这次推送就永远不会进入上面的任何一步。独立低频循环里的对账
兜底这一层，见「投递失败对账」。

## 审查访问面与 Forgejo 补漏（INT-167）

审查链路对平台的全部访问收在 `forge.ReviewClient`（11 方法 + `Kind()`）：
GitHub 适配器包安装 id 透传 githubapi（请求序列与错误原样，行为零变化）；
Forgejo 适配器（`internal/forge/forgejo_review_client.go`）直连 v1 REST
（认证 `Authorization: token {PAT}`，详见 docs/internal/forge-integrations.md）。
`forge.ReviewClients.ClientFor` 三分支：空串/`github-default` → GitHub；其余
id → `ForgejoSource`（service 层 `ForgeClientSource`，按集成行现取 SSRF
client + 展开基址 + 解密 PAT，不缓存凭证）组装 Forgejo 适配器。调用方
（调度/同步/资产/设置）只认 resolver，媒体下载 bearer 经 `BearerSource`
接口按集成解析——GitHub 是安装令牌、Forgejo 是机器人 PAT（review_assets
只对 GitHub 实例的附件路径带凭证）。

**Forgejo 轮询补漏（`review_forgejo_poll.go`）**：webhook 丢投递的兜底。5
分钟 ticker（挂在 `Start()` 的循环组，启动不立即跑首轮）对每个「启用审查且
绑定 Forgejo」的项目（`integration_id <> 'github-default'`）联查项目行复核
「仍绑该集成 + 代码仓未变更」（防漂移；受理键仍取设置行快照，live 读只用于
跳过漂移行），拉 open PR 分页后逐个经 `reviewWebhookTrigger`（触发事实的
中性形态）走与 webhook 完全相同的 `handleReviewTriggerTx`。动作按「该 PR
从未受理过 → opened（受 TriggerOpened 约束），已受理过 → synchronize（受
TriggerSynchronize 约束）」归类，补漏不绕过勾选位；updatedAt 不传（过期
判定需要两端都有值，补漏只依赖 SHA 去重）。适配器侧过滤 fork head 与已删
fork；draft 在受理面照常忽略。webhook 先受理的 SHA 由 seen/运行计数去重，
不会开出第二轮；单项目/单 PR 失败只记日志，不中断整轮。

## 调度循环（`review_scheduler.go`）

单进程一个循环（10s tick + 缓冲 1 的 kick），每 tick 顺序：

1. **到期防抖**：查 `fire_at<=now` 的防抖行，**先在事务外读一次 PR 实况**
   （`GetPullRequest`，单次 10s 超时）：PR 已关闭/合并或远端 head 与防抖
   行 head 不一致就删行不开轮（这是拦下「重投的旧 payload」的主关卡，见
   「投递失败对账」）；读失败分两类——确定性失败（仓库/PR 404、凭证
   401/403、集成行缺失、PAT 密文解密失败）直接删行不推后（每轮重试结果
   相同，留着只会永久占批量名额并按周期刷错；后续新事件会重建防抖行），
   瞬时错误（网络/超时/5xx/限流 403/429）不开轮也不删行，把 `fire_at`
   按当前时刻推后一个防抖窗口——限流 403 经 body 的 rate limit/abuse
   detection 字样与权限 403 区分，限流高峰不丢事件。通过
   后再逐行短事务条件删除并复核——项目仍启用、
   `project_review_settings.repo_full_name` 快照仍
   与防抖行 `repo_full_name` 一致（与 webhook 反查、同步准入同口径，不回读
   项目行 live 仓库字段）、触发条件仍勾选——成立才开一轮；顺带清理超龄
   delivery 幂等账（保留 7 天）。
2. **排队提升**：每用户最多 **5 个在途**（dispatched/running），其余
   `queued` 按 `created_at` 依序提升为 `dispatched`，`deadline_at` 重置为
   提升时刻 + 超时（超时从真正开始派发算，不占排队时间）。超时固定
   **代码审查 1h、视觉审查 30min**，常量，不提供配置。
3. **派发（含认领）**：`dispatched` 且未确认建线程、**且从未签发过运行
   令牌**的运行，在认领后超过 5 分钟仍 `dispatched_at IS NULL` 时允许重新
   认领——`CreateThread` 以运行行预分配的 threadId/commandId 幂等，不会建
   出第二个线程。**已签发令牌的运行不再重认领**：令牌是随机值、库中只存
   SHA-256，进程重启后无法恢复原值；令牌存在即证明 `launchRun` 至少走到过
   签发点，重签会覆盖旧哈希让已建成线程的令牌失效。这类运行保留
   `dispatched` 等 `deadline_at` 超时兜底，不自动重试。认领后条件 UPDATE
   占住 `dispatch_started_at`，事务外执行 launch；launch 在**签发运行令牌
   之前**再读一次 PR，已关闭/合并就落 `cancelled`+`error_code=pr_closed`
   （消费令牌、置 `sync_pending`），不建线程——覆盖排队期间 PR 被关且
   closed 投递丢失的情况。读失败不落终态、不签令牌，留给认领超窗重试。
4. **工作区准备失败重试**（INT-147）：`kind=code`、`dispatched/running`、
   `dispatched_at` 非空的运行，每运行最多 60 秒查一次 T3 线程投影
   （进程内节流表）；投影显示「可重试的工作区准备失败」且失败条目已存在
   满 30 秒时，先把 `prep_retry_count` 条件加 1，再代发
   `prepared-run.retry`（`commandId=fs-review-prep-retry-<runId>-<n>`）。
   重试被受理后在状态守卫下把 `deadline_at` 重置为 now+超时，并把未消费
   令牌的 `expires_at` 一并延到新 deadline+5min（只延 deadline 会让 Agent
   在旧过期点开始收到 40106）；RPC 期间运行已被终态化时两者都不改写。
   RPC 失败不回退计数。计数已达 3 次则不再发第 4 次，直接落 `failed`、
   `error_code=workspace_preparation_failed`、`error_message` 写 T3 原文，
   并发 `review_dispatch_failed` 通知（消费令牌、置 `sync_pending`）。
   判定与阈值见「工作区准备失败重试」。
5. **超时**：`dispatched/running` 过 `deadline_at` → `timed_out`
   （`error_code='timeout'`），消费令牌、置 `sync_pending`、写
   `review_run_timeout` 通知。
6. **取消运行的线程中断**：`cancelled` 且已派发且 `stop_attempted_at` 为
   空 → 先条件占标记，再 `StopThread`（`run.interrupt`，每运行只尝试
   一次）。
7. **GitHub 同步**：见「GitHub 产出」。只处理项目设置仍 `enabled` 的总账
   行——被接管/停用项目的 `sync_pending` 残留不再发 GitHub 产出。
8. **归档**：见「工作区回收」。终态且线程可能已建成的运行逐个归档；确定
   没有建成线程的（`reviewRunNeverLaunched`：`dispatch_failed` 请求帧未
   发出、`pr_closed` 在建线程前就被拦下）跳过，不误报回收失败。归档前有
   **收尾宽限**（宽限语义见「工作区回收」）：终态落库后 agent 的 T3 run
   可能还在写收尾回复，立刻归档会切断 provider 事件流。
9. **工作区回收**：见「工作区回收」。
10. **旧线程墓碑清理**：通用自动化下线留下的在途线程墓碑逐条停线程、归
    档，成功即删行；失败有限重试后放弃（只留日志），避免墓碑无限累积。

顺序约束：重试排在超时之前（重试成功会重置 `deadline_at`，别让同一 tick
的旧 deadline 抢先落 `timed_out`）；归档在工作区清理之前（删除必须以归
档完成为前提）。

带串行外部读的阶段（到期防抖逐行读 PR、准备失败重试逐行读 T3 投影、归档
逐行读线程收尾状态与归档 RPC）每个有 **60 秒网络预算**
（`reviewNetworkStageBudget`，与同步阶段单 PR 2 分钟同量级）：超预算的剩余
行留在库里，下个 tick 再处理——上游变慢时单个 tick 仍大体有界，不会把
超时/取消中断/同步/回收拖到几十分钟后。

所有网络调用在事务外；DB 落库用条件 UPDATE（影响 0 行 = 状态已被并发改写）。

投递失败对账不在本循环里：它在独立 goroutine 低频跑（`redeliveryLoop`），
GitHub 变慢时对账会长时间占住网络调用，不能拖住防抖消费/派发/超时，
见「投递失败对账」。

### 投递失败对账（`review_redelivery.go`）

GitHub App 投递失败（公网隧道超时/502）不会自动重试，一次失败就是这次推送
永远不被审查；对账在**独立低频循环**里兜底（INT-146）。机制：

- **独立 goroutine**（`redeliveryLoop`，与调度循环共用同一个 cancel）：自带
  ticker 每 **5 分钟**一轮（`reviewRedeliverySweepEvery`），**启动即先跑一
  轮**（进程刚起来时补上停机期间漏掉的投递），单次兜底超时 90 秒——独立
  循环不阻塞调度，但仍要有界，避免 GitHub 半挂时无限拖。
- 拉近 **24 小时**（`reviewRedeliveryLookback`；GitHub 只保留约 3 天投递
  记录）的投递记录，只处理 `pull_request` 事件里**会开轮的 action**
  （opened/synchronize/ready_for_review/reopened，`reviewTriggerableAction`
  同源判定），`status_code` 非 2xx 即失败。`closed` **有意不重投**：丢失的
  closed 投递不会导致「推送没被审查」，而重投回去会无条件取消该 PR 当前
  有效的审查运行（webhook 的 closed 分支在任何时间戳判定之前执行）。
  重投重放的是**原始 payload**（PR 仍写着 open、`updated_at`/head 都是旧
  值），所以「该不该继续审」不能信事件体，只能看 `GetPullRequest` 的实时
  `State`——三个开关关卡（防抖到期开轮、建线程前、写 GitHub 产出前）
  都以它为准（见「调度循环」「派发」「GitHub 产出」）。
- 每个 guid 汇总成一行状态（`groupHookDeliveries` 纯函数）：任一行 2xx =
  已送达；列表里 `redelivery=true` 的行数只是**列表可见的近期重投次数**
  （兜底），**总次数以持久化的重投次数账为准**（`github_webhook_redeliveries`
  表，guid 主键）：投递列表只保留约 3 天，原始行滑出 24h 窗口后从列表推
  导的次数会变少——可能重复重投或漏发超限通知，所以每次成功重投都 upsert
  记账（attempts+1），账按与幂等账相同的 7 天保留期清理，丢了顶多退化回
  列表推导的近期次数。有效次数取 `max(账, 列表)`。
- 重投前两路去重，先查幂等账再判超限（本地账一次查全，含超限组——否则
  「GitHub 侧三次都超时、但某次实际已在本地落库」的 guid 会被误报故障）：
  本地 `github_webhook_deliveries` 幂等账已有该 guid（说明投递其实已送达，
  只是 GitHub 侧记录滞后）→ 跳过（不重投、不通知、不记账）；同 guid 任一行
  2xx（含成功的重投）→ 跳过。
- **关闭/合并的 PR 不重投、不报故障**：投递列表行（`HookDelivery`）没有
  repo/PR 信息，而重放的 payload 是旧快照（PR 当时写着 open），所以重投前
  先 `GetHookDelivery`（`GET /app/hook/deliveries/{id}`，同一 `HookDeliveries`
  访问面）取请求体，解析出 `repository.full_name`/`number`/`installation.id`，
  再 `GetPullRequest` 读**实时** State（同轮按 `repo#pr` 缓存，同一 PR 的多个
  投递只读一次）。PR 已关闭/合并 → 该 guid 记终态（重投次数账的
  `closed_at` 非空）并静默跳过：不重投、不发 `review_redelivery_failed`
  通知、以后每轮也不再查询它——否则隧道恢复后这些旧
  synchronize 会被重投回去白跑一轮，重投持续失败时还会误报「这次推送可能
  没有被审查」。payload 读不到 / 解析不出 PR / `GetPullRequest` 失败时
  **降级为不校验照常重投**：这道闸门只省白跑与误报，开轮与写产出的三道
  实时状态关卡（见「调度循环」「派发」「GitHub 产出」）已经拦得住误开轮，
  不能反过来把真投递漏掉。闸门查询（详情 + 实时读各占一个名额）有独立的
  **每轮 20 次次数预算与 30 秒时间预算**（`reviewRedeliveryProbeTimeout`）：
  用完后立即按未知降级，剩下的时间留给重投——不能拿可选校验烧完 90 秒
  总超时，否则重投请求会拿到已取消的 ctx，后面的真投递既不重投也不通知。
  闸门截止时间还不晚于「总截止 - 30 秒余量」（`reviewRedeliveryReserve`），
  列表页多、探测慢时重投仍有时间。
- 通过 `POST /app/hook/deliveries/{id}/attempts`（App JWT 端点，访问面
  `githubapp.HookDeliveries`，不进安装令牌的 `GitHubClient` 接口）让 GitHub
  重投：请求与原始投递相同，`X-GitHub-Delivery` 也相同，webhook 的幂等登记
  保证不重复处理。**只对成功的重投记账**（请求被拒时 GitHub 没产生投递
  记录，下一轮会再试）。**一轮最多发 20 个重投请求**
  （`reviewRedeliveryMaxPerSweep`，按「尝试」计、失败也占名额——只按成功
  计数的话接口持续 5xx 时上限形同虚设；剩余留到下一轮，不突发几百个
  POST）；遇 **403/429 立即停止本轮**（GitHub 明确要求限流后停止请求并
  退避，5 分钟后 ticker 自然再来），其他错误逐条跳过。
- 同一 guid 最多重投 **3 次**，超限不再重试，发 `review_redelivery_failed`
  站内通知（正文写实际次数）：投递记录是 App 维度、映射不到具体项目，发给
  所有启用审查的用户（`project_review_settings.enabled` 的 distinct user），
  dedup 键含 guid，重复对账只推进 `last_seen_at` 不刷屏。
- 没有启用审查的项目、App 未配置（或实现没有投递访问面，如静态令牌）时
  整步跳过。列表翻页、重投请求的复杂度收在 `pkg/githubapp` 适配器里，
  service 层只做分组与决策。
- 列表翻页走响应头 `Link` 的 rel="next"：**超过页数上限（10 页）仍有下一页
  时按错误处理**（`ErrDeliveriesTruncated`）——部分账本会漏掉投递，
  对账本轮放弃；**跟随 next 前校验同源**（scheme 与 host 必须与 API base
  一致），Link 来自响应头，不能把显式设置的 App JWT 带到别的地址，异源
  当作没有下一页。

### 开轮

- `round_number`：同一 (project, repo, pr, head_sha) 已有运行则复用该轮次
  （代码与视觉同轮号）；否则 `1 + max(round_number)`——轮次只在项目内分配，
  接管后新项目从 1 起算，不受旧项目已跑轮次影响。手动重新审查（
  `trigger_action=manual`）恒开新轮——同 head 也要 `max+1`，否则会被
  组合唯一索引吃掉。
- 同事务 upsert `review_pull_requests`（project/user/installation/round/head、
  `sync_pending=true`），为每个代码组合插一行 `review_runs`（`status=queued`）。
- 视觉运行不在开轮时创建：等代码审查全部终态后由「视觉审查衔接」决定。

## 派发（T3 线程与工作区）

- **代码审查**：在组合环境绑定的 T3 项目里新建 worktree——
  `workspace={type:worktree, base_ref=<head_ref>, start_from_origin:true}`。
  head 分支可能不是默认分支，提示词显式要求 `git checkout <head_sha>` 并
  核对 `git rev-parse HEAD` 等于该 SHA。
- **视觉审查**：`workspace={type:root}` 在 T3 项目根目录开线程，只读——
  只通过运行令牌端点读 PR 元数据、截图与历史，不改代码。
- 线程标题 `代码审查 · PR #N` / `视觉审查 · PR #N`，`runtime_mode=full-access`；
  `thread_id/launch_command_id/initial_message_id` 预分配在运行行上（重试
  幂等，T3 复用同 ID 线程）。模型选择 `{instance_id, model, options}` 直接
  映射 T3（options 按 id 排序保证稳定）。
- 派发前置：项目经 `project_t3_bindings` 绑定该环境的 `t3_project_id`，
  缺失 → `failed/dispatch_failed` + 通知；`CreateThread` 复用
  `t3_thread_launch.go` 的 `createThread` 与 launchSent 语义——启动 RPC
  请求帧未写出 → `failed/dispatch_failed` + `review_dispatch_failed` 通知；
  已发出但结果未确认 → 保持 `dispatched` 等超时，不自动重试。
- **启动请求不可变重放**：`thread_id/launch_command_id/initial_message_id`
  在运行行上预分配（开轮时刻写入），重试重放同一请求不建第二个线程；运行
  令牌只在运行从未签发过时才新签（令牌存在 = `launchRun` 到达过签发点）。
  `dispatch_started_at` 是认领时刻、`dispatched_at` 是建线程确认时刻。
- **建线程前查 PR 实况**：`launchRun` 在签发令牌之前读 PR，不是 open
  就落 `cancelled`/`error_code=pr_closed`（消费令牌、置 `sync_pending`），
  不建线程；读 PR 失败按确定性归类——404 先经仓库级 `GetRepository`
  复核消歧（GitHub/Forgejo 对无权限资源也回 404）：仓库可读而 PR 404 =
  PR 真已删同 `pr_closed` 终局，仓库同样确定性读不到（凭证吊销、仓库
  转私有、仓库已删）与其余确定性失败（凭证 401/403、集成行缺失、密文
  损坏）同落 `failed`/`error_code=access_denied` +
  `review_dispatch_failed` 通知；只有瞬时错误才不落终态不签令牌、等
  认领超窗重试。
- 提示词系统写死（`review_prompts.go` 常量）：代码审查要求自查证（git diff
  + 读文件）、以找 bug 为主、P0/P1/P2/Note 分级、历史处置规则、判断
  `ui_changed` 与 `ui_changed_since_last_visual`；视觉审查按层级/间距/排版/
  颜色/交互/文案/可访问性七维度、blocker/worth_fixing/minor 三级，先核对
  截图是否体现 PR 描述的改动，给出具体检查项（WCAG AA 对比度、点击区域、
  状态反馈、文案泄露技术串、假数据等），不凑数；旧图、裁切、缺关键状态
  与压缩看不清一并写进 `unmeasurable` 并说明怎么补拍。视觉的
  历史处置口径：`prior` 记对上一轮每条发现的处置（fixed / rejected /
  open，open 保持原级别并写清现状），`findings` 只写本轮新发现的问题——评论
  的「本轮新发现 / 仍未解决」两段正好对应这两份数据，不需要靠文本比对归
  类。两份都
  明确「只读审查、不发评论、不改 PR 状态，结论只能经 run 作用域的
  `/result` 提交」；凭证与临时文件要求见「运行令牌与 Agent 端点」。
  代码审查另约束产出形态：`summary` ≤2 句（结论 + 关键影响），finding
  按「问题 / 影响 / 建议」组织，`line` 无法定位到 HEAD 行时省略（顶层评论
  会注明未行内标注）。

### 工作区准备失败重试（`review_prep_retry.go`）

T3 建线程阶段可能撞上 Git 锁竞争（同仓库多个组合同时建 worktree 时
`fetchRemote` 报 `Another Git operation or a stale lock may be blocking the
fetch`）：T3 把 run 置 failed、写一条
`type=error`/`failure.code=workspace_preparation_failed` 的条目，并提供一个
Retry 按钮（`orchestration.dispatchCommand` 的 `prepared-run.retry`）。Fast
Ship 不代点就干等 1h deadline 变 `timed_out`，整轮还得手动重审。系统因此
自己代发，判定与阈值：

- **可重试的投影条件**（对齐 T3 `dispatchPreparedRunRetry` 的受理条件，看
  结构化字段、不看报错文案）：线程投影里的目标 run（按
  `userMessageId == 运行行 initial_message_id` 匹配，同一消息出现多条 run
  时取 ordinal 最大的一条——只有最新那条代表当前状态）`status=failed` 且
  带 `workspacePreparation`；`turnItems`/`visibleTurnItems` 里有该 run 的
  `type=error`、`status=failed`、`failure.code=workspace_preparation_failed`
  条目；`thread.archivedAt`/`deletedAt` 为空；同线程没有其他
  preparing/starting/running/waiting 的 run。失败原始文案与发生时刻从该
  条目取（`updatedAt`，缺失回退 `completedAt`）。
- **节奏**：每运行最多 60s 查一次投影（进程内节流表，重启丢表只是多查
  一次），失败条目存在满 30s 才重试（避开同一次锁竞争）。检查对象是
  `kind=code`、`dispatched/running`、`dispatched_at` 非空的运行，**不设
  批量上限**——节流命中不发生网络调用；带投影读的阶段有 60s 网络预算。
  扫描用**进程内轮转游标**：预算耗尽就停在哪条、下轮从它的下一条继续
  （环形），不回到队首——慢投影读会把预算吃光，固定从队首扫会让前几条
  反复占满预算、尾部直到超时都轮不到。
- **计数与上限**：先条件 UPDATE 把 `prep_retry_count` 加 1 再发 RPC；RPC
  失败不回退计数（下个窗口按新计数换 commandId 再试）。达 3 次不再发第
  4 次，落 `failed`+`error_code=workspace_preparation_failed`+
  `error_message=T3 原文`，发 `review_dispatch_failed` 通知。注意 Fast
  Ship 的判定是 T3 受理条件的**子集**（T3 还要求 workspace-preparation 的
  command_execution 条目等内部状态）：Fast Ship 判定可重试但 T3 不受理时
  RPC 会被拒，此时计数照常推进（不回退），达上限即转人工。
- **重试受理后**在一个事务里把 `deadline_at` 重置为 now+超时、并把未消费
  令牌的 `expires_at` 延到新 deadline+5min（T3 复用同一 run、把它恢复成
  preparing 并重放原用户消息，运行仍在原线程里跑；只延 deadline 不延令牌
  会让 Agent 在旧过期点开始收到 40106）。状态守卫（计数=本次加 1 后的值
  且仍在 dispatched/running）保证 RPC 期间运行已被取消/完成/超时终态化时
  两者都不改写。

## 运行令牌与 Agent 端点（`/api/review-agent/runs/:runId/*`）

派发时签发 `fsr_` + 32 字节 base64url，存 SHA-256，
`expires_at = deadline_at + 5min`。路由统一 `runTokenAuth`
（`middleware.RequireRunToken`）：只允许 `fsr_`，`fsk_` API Key 一律
**40304**，无令牌/无效/过期/已消费一律 **40106**。运行必须 `kind` 匹配
端点且 `status ∈ {dispatched, running}`；首次成功鉴权把 `dispatched→running`
落库（`started_at`），**并补 `dispatched_at`**——令牌只经启动提示词交给
线程，Agent 能出示即证明线程已建成（即使启动 RPC 结果未确认），归档/回收
据此区分「可能已建成」与「确定没建」。运行进终态后，除 `POST /result` 的
完全相同重放（幂等成功）外所有端点一律 40106。

**运行归属强校验（INT-169）**：全部端点带 `:runId` 路径参数，
`RequireRunToken` 把它作为预期运行交给 `ResolveRunToken`——令牌/run 加载、
存活检查全部通过后、**任何状态推进之前**比对 URL runId 与令牌所属运行，
错配一律 **40307**：不写结果、不消费令牌、不推进运行状态（包括
dispatched→running）、不发布评论——对双方运行零副作用。拒绝日志记目标
run_id、令牌所属 run_id、`thread_id`、reason（不记令牌内容）。
`runId` 为空（`/runs//...` 双斜杠——Gin 允许路径参数匹配空段）同样按
40307 拒绝：空目标无法做归属校验，不能形成仅凭令牌的旁路入口。旧的仅凭令牌
端点（`/api/review-agent/pull-request`、`/history`、`/result` 等）已整体移除：
仅凭令牌无法确定「预期目标」，保留等于留一个绕过新校验的提交路径。
**在途线程处理**：升级时仍在跑、持旧提示词的线程无法构造 run 作用域
URL（其提示词不含 run_id），请求 404，运行按既有 deadline 机制
`timed_out` 收尾。

**远程临时文件约定**：提示词要求 Agent 在系统临时目录建
`fast-ship-review-<run_id>-<随机后缀>/`（POSIX `mktemp -d`，Windows
`%TEMP%`），凭证配置、结果 JSON、拉取的 PR/diff/history/截图全部放该
目录并用绝对路径；创建失败即停止、不回退共享路径，禁止 `/tmp/fsr-*`
固定路径与 HOME 固定文件（并行运行会互覆同名文件——INT-169 的串用
事故即由此发生）；重试沿用同一目录，成功后只删本轮目录。目录隔离依赖
Agent 执行，服务端的 run 强校验是兜底。

| 端点（前缀 `/api/review-agent/runs/:runId`） | 说明 |
| --- | --- |
| `GET /pull-request` | PR 元数据（repo、PR 号、标题、正文、base/head ref、head SHA、URL）；身份全部取自运行行，不接受请求参数。 |
| `GET /pull-request/diff` | PR diff，>1MB 按字节截断并标 `truncated:true`；diff 超本端读入上限（64MiB/64MB）时给已读前缀（可能为空）同样标 `truncated:true`，不让 Agent 每次拉取都 50200 卡到 deadline。 |
| `GET /history` | 系统生成的历史上下文 Markdown（无历史时空串）。代码：上一轮（`round_number-1`）所有组合的结构化发现（含组合名）+ 上一轮结论之后的新行内/会话评论；首次审查给全部讨论（最近 50 条）。视觉：上一轮视觉 findings + unmeasurable，再加该轮 `prior` 里仍未解决（`status=open`）的条目（独立小节「仍未解决（更早一轮）」）——模型把遗留只写进 `prior(open)` 时也不会从下一轮历史里消失。**只识别本系统自己发的结论**（按评论隐藏标记找回），不兼容旧 Review Bot / Code Bot 评论。 |
| `GET /screenshots` | 仅视觉运行。`{items:[{id,label,url}], changed}`：`url` 指向图片端点；`changed` = 本次 head 的截图 URL 集合 ≠ 上一轮视觉运行绑定 head 的截图 URL 集合（只作审查员的「是否要对比上一轮」提示；**是否开视觉轮由总账的内容指纹决定**，两者口径不同，别拿它推结论）。 |
| `GET /screenshots/{assetId}` | 仅视觉运行。图片字节 + Content-Type，按运行所属 PR/SHA 校验；未下载成功 404。 |
| `POST /result` | 提交结构化结论，见下。 |

`POST /result` 请求体按 kind 二选一（openapi `oneOf`）：

- 代码：`{summary, findings[{level:P0|P1|P2|Note, file, line?, body}],
  prior[{level?, file?, line?, body, status:fixed|rejected|open, reason?}],
  ui_changed, ui_changed_since_last_visual}`。`line` 是当前 HEAD 右侧代码
  行号，可省略；`prior.status=rejected` 必须有 `reason`。
- 视觉：`{summary, findings[{severity:blocker|worth_fixing|minor,
  screenshot, dimension, body, fix}], prior[同上],
  unmeasurable[{screenshot, reason}]}`。`dimension` 取 hierarchy / spacing /
  typography / color / interaction / copy / accessibility。

校验：请求体上限 2MB（超限 40004）、必填字段、枚举、`summary` 非空、
`line>=1`；非法 → 400（40004 口径）并提示修正重交，不改运行状态。提交前用 App 令牌 `GetPullRequest` 核对 PR
当前状态与 HEAD：PR 已关闭/合并，或 HEAD 与运行 `head_sha` 不一致 → 本轮
作废（`cancelled/superseded`、消费令牌、置 `sync_pending`），返回 **40920**。
**文件归属（INT-169）**：代码审查 `findings` 的 `file` 逐个经
`PathExistsAtRef` 核对在本轮 `head_sha` 树中存在——实现走非递归
`git/trees/{ref}:{dir}` 取父目录条目精确匹配末段名（contents API 的 2xx
会把目录/符号链接/子模块混进来，不严格；Forgejo 侧无 `ref:path` 语法，
走 contents 端点）；树未命中再用本轮 PR 变更文件清单兜底一次——PR
删除/重命名走的文件仍是合法审查对象（「不该删它」指向的正是已删文件），
只在树与清单都查不到才拒。清单来自 `pulls/N/files` 分页端点（rename 的
`filename`+`previous_filename` 两侧都收）——平台对超大 PR 的 diff 文本
可能无标记静默截断，否决类证据不用 diff 文本解析。清单本身不完整
（GitHub 3000 文件封顶）时缺席同样不作否决。不完整证据
不作缺席证明——两种形态：变更清单被平台封顶（`complete=false`）；
GitHub `git/trees` 截断（`truncated=true`，>10 万条目目录，归一为
`ErrTreeTruncated`）或目录清单超读入上限（>8MiB，同样归一
`ErrTreeTruncated`）。这些都按「存在性不可判定」放行并
Warn 记录，而非 40004/50200——重试只拿到同一份不完整证据，拒收会把
合法运行卡到 deadline。404 消歧：dir/路径级 404 回探根树/根目录，
根也 404 按调用失败（50200）而非「文件不存在」上报，避免把 ref/仓库级
故障误报成审查对象错误。同路径去重且去重后上限 64 条
（每个唯一路径一次平台调用，需要整体预算；该上限已写入 Agent 提示词
契约——改动面极广时 Agent 应合并同文件发现或裁剪次要条目）；前导 `/`、`..`/`.`/空段
等别名不做归一化、一律按不存在处理（校验过的形态原样入库，别名会造成
校验对象与存储值不一致）。树与 diff 都查不到 → **40004**「核对审查对象」且不产生任何状态副作用，
可修正重交；`prior` 不查（历史意见里已删除/重命名的路径合法）；只查
新发现、不限定 diff 内文件。校验位置在终态重放判定之后——已完成运行的
相同 canonical 重放不受新检查影响，仍按原幂等语义成功（并发落库的同文
提交在事务内复核后同样按幂等成功返回；校验失败时也先复核运行是否已被
并发提交完成，再决定报错口径）。
成功后写 `result_json/summary`、
`completed`、`finished_at`，消费令牌、`posted_at` 置空（待同步推 GitHub）、
置 `sync_pending` 并 kick。

## GitHub 产出（`review_sync.go`）

每 PR 一把进程内锁（按 repo+pr 键控，同步完摘除条目，防 map 随历史 PR 数
增长——锁刻意保持仓库归一，同 PR 的 GitHub 写入串行），逐行处理
`review_pull_requests.sync_pending` 中项目设置仍 `enabled` 且账本
`repo_full_name` 与该项目 enabled 设置行快照一致的行（选中后还要过一次准入
复核：pending 未清、epoch 未推进、快照仍是本仓库；通过后每次 GitHub 写之前
再复核一次，避免同步期间改仓库把产出写到旧仓库）；全部走
App 安装令牌。失败只记 `sync_error`、记 `sync_failed_epoch = sync_epoch` 并
按 `reviewSyncRetryBackoff`（5 分钟）退避重试，不改运行终态——持续性失败
（`SubmitReview` 固定 422、评论接口 403）不会每 10s
重放整条同步（否则读 diff、写评论、写 commit status 被放大 360 倍/小时，还会
写满同一 SHA 的 1000 条 status 上限）；diff 超出上限（4xx + `errors[].code=too_large`，实测 406）
是唯一不走退避的确定性失败，按「本轮无行内 review」降级收尾（见「顶层评论」
的「确定性失败（diff 超限）例外」）；任何置 pending 的入口都会推进
`sync_epoch`，新事件因此立即重新入队、不受退避影响。成功后按开始同步时的
`sync_epoch` 条件清 pending（同步期间的新 pending 不会被误清）。
中段写回（`code_status`/`visual_status`/`visual_screens_fingerprint`）同样带
`sync_epoch` 条件：同步期间总账被新 webhook/rerun 推进时不命中，直接放弃
本轮落库——陈旧快照的结论不得写回。

**已关闭/合并的 PR 不写任何产出**：`doSyncPR` 读到 PR 后先判 `State`，
不是 open 就在一个事务里清 pending（`sync_epoch+1`，带 epoch 守卫）、用
`supersedePRRunsTx` 取消在途运行，然后返回——不发 commit status、不建/
不改顶层评论、不写行内 review、不建视觉运行（合并后 head 不再变化，
靠 SHA 比较拦不住，必须先判关闭）。汇总运行、视觉沿用判定、评论写回等
所有账本读写都按 `project_id + repo + pr`。

### commit status（不用 Checks API）

固定两个 context，`target_url` 为 PR URL，`description` ≤140 字节（按
rune 截断加 `...`，不劈断多字节字符）；状态未变不重复调用。组合显示名
（`model[options] / instance`）截断到 60 字符。

| context | 状态 | 文案 |
| --- | --- | --- |
| `代码审查` | 任一组合未终态 | `pending`「审查中」 |
| | 任一 `failed/timed_out/cancelled` | `error`「审查未完成：<组合> 失败」（取第一个失败组合） |
| | 全部 completed 且有 P0/P1/P2 | `failure`「发现问题：P1 x 条，P2 x 条」（所有组合合计，只列非零级别，按 P0→P1→P2） |
| | 全部 completed 且无 P0/P1/P2 | `success`「审查通过」 |
| `视觉审查` | 代码审查未全部终态 | 不写（`visualWait`：结论还可能变，不提前落 status/评论/指纹） |
| | 视觉运行启动 | `pending`「视觉审查中」 |
| | 有 blocker / worth fixing，含 `prior` 里 `status=open` 且原级别为 blocker/worth_fixing 的遗留 | `failure`「发现 N 条问题」 |
| | 只有 minor 或无发现 | `success`「审查通过」 |
| | 本轮无事可做（截图内容未变、上轮视觉后无 UI 改动）且上一轮通过 | `success`「审查通过（沿用上一轮，截图未变化）」 |
| | 本轮无事可做且上一轮遗留 blocker / worth fixing | `failure`「截图复核意见尚未处理（上轮 N 条）」 |

代码审查 `error` 时不启动视觉审查，等手动重新审查；未配置视觉组合时永不
设 `视觉审查` 状态。代码运行未全部终态时**不写任何视觉产出**（等待分支见
「视觉审查衔接」）；描述里没有可服务截图、或截图变了/有新 UI 改动时都不沿
用上一轮通过——不写 status 也不冒充。

### 顶层评论（各一条，隐藏标记找回）

- 代码审查：标记 `<!-- fast-ship-code-review -->`（`FindPullRequestComment`
  找回 → `Update`/`Create`）。结构（`buildCodeComment`）：
  - 标题 `## 代码审查 · <短SHA> · 第 N 轮`，下一行一句话结论（通过 /
    P0–P2 计数 / 审查进行中 / N 个组合未完成）。
  - 总览表 `组合 | 状态 | P0 | P1 | P2 | Note`：组合列是 `模型 · 实例`，
    计数只对 completed 且结论可解析的组合统计，其余填 `–`。
  - 按组合分节：`### 模型 · 实例` + 一行 `<sub>k=v · k=v</sub>`（选项去
    JSON 引号，只截断选项本身、不截模型/实例），summary 用引用块。
  - P0→P1→P2 每条：`**级别** [file:line](HEAD blob 行锚点)` 换行正文；
    无法行内标注（无行号，或行号不在本次 diff 右侧有效行）的条目在位置后
    注明「未行内标注」。Note 全部折叠进 `<details>`。
  - 视觉待办/截图遗留提示在表格之后，末尾固定复审说明（推送后自动复审）。
  - 行内标注判定与行内 review 共用一次 `GetPullRequestDiff` + 右侧有效行
    集合（`fetchInlineLines`），不再重复拉取。**diff 读取失败只推迟行内
    review**：commit status、顶层评论（按「本轮没读 diff」口径，不加「未
    行内标注」注记）照常写，推迟的错误最后才上报——账本保持 `sync_pending`
    并写 `sync_error`，退避后重试；不能把失败压成「没有有效行号」，那会把
    行内意见静默丢掉。**无论有没有待发布的行内运行都上报**：本轮评论可能已
    按「没读 diff」口径重写，保持 `sync_pending` 才能在 diff 恢复后补上注记。
    注：同一 (head, round) 里「diff 读取失败」与「存在视觉运行」互斥——读
    diff 的前提是某个 completed 代码运行有带行号的 P0–P2，而这会让视觉决策
    停在 `visualNone`、不建视觉运行；视觉产出因此不会被 diff 失败连累。
    **确定性失败（diff 超限）例外**：GitHub 对超出上限的 diff 返回 4xx（实测 406，
    413/422 防御性兼容）且响应体 `errors[].code=too_large`（`githubapi.IsDiffTooLarge`），这是重试不
    可能成功的确定性失败，只认结构化 code、不凭状态码判定（406 是通用的
    Unacceptable，误判会把暂时性失败降级成终态）。此时本轮按「无行内 review」
    降级收尾：顶层评论照常写并注明「本轮 GitHub 拒绝读取 PR diff（超出上限），
    未发行内 review」，P0–P2 仍完整列在评论里（不静默丢）；有行号但本轮无法
    核对 diff 的发现不加「未行内标注」注记（无行号的发现本来就加，与是否读
    diff 无关）；行内运行保持 `posted_at` 为空，账本清 `sync_pending`/
    `sync_error`（本轮终态、不入退避）。同一 (head, round) 的下一次同步事件
    （同轮其他组合完成/失败、超时收尾）仍会再读 diff，读得到就补发行内 review
    与注记——属于自愈而非永久终态，所以不写 posted_at、不留任何「已降级」
    持久标记。手动重跑恒开新轮（`openRoundTx`），由新轮的运行重新审查并发布，
    不是补发旧轮的降级运行。**自愈窗口的边界**：同一 (head, round) 若之后再无
    同步事件（包括最后一轮收尾之后才超限、且 PR 不再有新事件），这次降级就是
    该 head 的终态；base 分支内容变化不会给 PR 触发同步事件，不能指望靠它补发
    行内 review。解析不出 `too_large` 的 4xx/5xx 仍按可重试推迟。
  - **顶层评论写入统一去重**（`upsertPRComment`，代码与视觉评论共用）：先按
    账本记的 `code_comment_id`/`visual_comment_id` 认自己的评论，id 不可用
    （写回失败/评论已重建）时退回与 `FindPullRequestComment` 同口径的「bot +
    marker 前缀」；正文已等于本轮正文就不 PATCH（重试路径每次都调这里，相同
    正文的写入既耗配额又留编辑记录），并顺手把找回的 id 补记回账本；正文变了
    照常更新。用状态做代理会漏掉「多个组合里又完成一个但状态没变」，只看
    「正文含 marker」会把用户复制粘贴的同一段正文当成自己的评论。
  - 视觉 `commit status` 的去重：沿用上一轮通过时按账本 `visual_status` 判
    （开轮已清空它，新轮首次照常写）；「截图不可用」「沿用上一轮 failure」
    两支在**同一快照的失败重试**（`sync_failed_epoch == sync_epoch`）且账本
    已是 failure 时跳过——状态写成功、随后写评论失败的重试不再每轮追加一条
    相同 context 的 status（同一 SHA + context 有 1000 条上限）。
- 视觉审查：标记 `<!-- fast-ship-visual-review -->`，标题
  `## 视觉审查 · <短SHA> · 第 N 轮`（与代码评论同一定位口径）。按组合分节
  （`### 模型 · 实例` + summary），先「本轮新发现」（`findings` + `unmeasurable`）再「仍未解决」（`prior` 里 `status=open` 的条目）；沿用上
  一轮时开头写「沿用上一轮视觉审查结论（第 N 轮 · head <短SHA>）：截图未变
  化，意见尚未处理」，被沿用的 findings 归入「仍未解决」，不冒充本轮新发现。
  `findings` 每条带截图标签、维度、改法；`prior(open)` 每条只有级别 + 正
  文（原条目没有截图/维度字段）。不发行内评论；截图全部无法下载的评论共用
  同一 marker 与标题口径。

### 行内 review（每个代码组合一个）

每个 `completed` 且 `posted_at` 为空的代码运行：有 P0–P2 且行号落在 diff
右侧有效行内时 `SubmitReview(event=COMMENT, body=summary, comments=[...])`
（`side=RIGHT`）；行号不在 diff 范围内的条目并入顶层评论，没有有效行内
条目不提交 review。**Note 不发行内评论。**提交后写 `review_id` 与
`posted_at`。

## 视觉审查衔接

视觉决策只在代码审查**全部终态**后做。代码运行还有排队/在途时决策是
`visualWait`（同步的等待分支）：本轮只发布代码侧产出，不写视觉 status、不
发视觉评论、不动截图指纹——代码结论还没落定，任何视觉判断（沿用/开轮/截
图不可用）都可能建立在即将被推翻的输入上。代码运行终态会置
`sync_pending`，下一次同步自然接上，等待不会卡死衔接。

代码审查全部终态后，以下五条**同时满足**才开视觉运行（每个视觉组合一行，
`kind=visual`，同 `round_number`），并把 `视觉审查` 置 pending「视觉审查中」：

1. 所有代码运行 `completed`（无 failed/timed_out/cancelled）且无 P0/P1/P2；
2. 配置了视觉组合（0 个则永不启动，也不设 `视觉审查` 状态）；
3. **至少一张截图下载成功**（下载面见下；描述里的截图引用数只是前置）；
4. 至少一个代码运行 `ui_changed=true`；
5. 截图集合有变化（URL 集合指纹不同**且内容指纹也不同**），或任一代码运
   行 `ui_changed_since_last_visual=true`。

判定顺序（`decideVisual`）：代码未全终态 → 等待；无视觉组合/无可服务截图
→ 不动任何产出；截图变了或上轮视觉后又有 UI 改动 → 代码没全净或 UI 没变
就不开，否则探测后开轮（探测拉不到则报「截图无法下载」）；以上都不成立
（截图内容未变且上轮视觉后无 UI 改动）→ 上一轮遗留 blocker/worth_fixing
就沿用 failure，上一轮通过就沿用 success。**截图变了或有新 UI 改动时绝不
沿用 success**——那会盖住真正需要复审的信号。

「其余判据都成立」包含条件 5：只有本轮真的要开视觉（截图指纹变了，或上轮
视觉后又有 UI 改动）才实际去**探测**下载——避免每次同步都拉图，也避免把
截图未变的沿用/带过路径误写成「截图不可用」。探测按描述顺序把**每一张**
都试一遍（不再第一张成功即停：内容指纹需要全部成功图的哈希；成功图已落盘
缓存，视觉运行时直接命中），不写 `review_assets`
——资产行仍由视觉运行时的 `ensureAssets` 落账，可用性只看当前描述里的 URL，
不按资产行计数（已不在描述里的陈旧资产不算可用截图）。探测有 30s 总时限
（含同 URL 下载锁上的等待——等待吃 ctx，不会被前一请求的 45s 客户端超时
拖穿）：它跑在共享同步循环里，图床挂死时不能把其它 PR 的同步一起拖满。

探测结果是**三态**，按逐张失败的原因分类（`ErrUnsupportedUpstreamMedia`
内容不是位图、`ErrInvalidMediaURL` 地址形态不支持 →「不是图」；其余一切
——网络、超时、预算耗尽——→「拉不到」）：任一成功 → `ready`，照常开轮；
全部探完且全部「不是图」→ `none`，把 `HasScreenshots` 改判为 false，与「描
述里没有截图」同收敛（`visualNone`，不写任何视觉状态——svg 徽章这类引用
不会每个同步事件刷一条 failure）；零成功且存在「拉不到」、或总预算耗尽未
探完 → `unavailable`，reason 取第一条「拉不到」类原因，没有再取第一条失败
原因。只有全部引用都下载成功时 `ready` 才带内容指纹；只成功一部分时内容未
知，调用方保守按「变化」处理，账本指纹只推进 URL 维度（见下）。

**截图指纹是两层的**：`visual_screens_fingerprint` 存
`c:<urlFP>:<contentFP>`（URL 集合指纹 + 内容指纹）、或 `u:<urlFP>`（已结
清「没有可服务截图」）、或历史上无前缀的裸 `urlFP`（按只有 URL 维度兼
容；旧版结清前缀 `none:` 仍可解析、不再新写）。比较时先看 URL 集合：相同
即判未变，不下载、不探测（便宜的常见路径）；URL 集合换了才探测，用下载文
件的字节 SHA-256 与账本里的内容指纹比对——作者重新上传截图、URL 全换但字
节相同不算变化（musiver PR #563 第 4→5 轮的红绿反复正源于按 URL 判变化）。
内容指纹是各图文件哈希（流式读取，单次读盘）排序后 join 再哈希的前 16 位，
与引用顺序无关；部分下载失败或 hash 预算耗尽时为空（未知）。

**账本指纹总是描述最近一次视觉轮的材料**：开轮 / 沿用分支只要本轮拿到新
知识就会推进——内容指纹已知写 `c:` 形态；内容未知但 URL 集合变了，至少写
裸 URL 指纹（不能把上一轮的内容指纹留在账本里，否则后来换回旧 URL 的
head 会被误判「未变化」，把别的截图集合上的通过结论沿用过去）；URL 集合
没变则不覆盖，保留已有的内容维度。已知取舍：**URL 集合相同就当作同一批
截图**，不再下载——附件类图床每次上传都换 URL，这条便宜路径成立；对固定
URL 换内容的第三方图床不检出，接受。

指纹哈希与探测共用同一份 30s 预算：每张换算前检查预算（不启动新的哈希），
耗尽后已下载的图仍可用于开轮，但内容指纹按未知处理（不写内容维度）。

指纹与视觉运行**在同一事务里落账**，且在确认**真的建出了运行**之后才写
（`createVisualRuns` 内、组合循环之后）：否则「运行已建、紧接着的 GitHub
pending 写失败」时账本会停在上一条材料上，下一次同步因为已有运行而走汇总分
支、不补指纹，之后换回旧 URL 集合的 head 就会被误判「未变化」。反向也一样：
决策时读到的组合可能在探测期间被清空（启用状态允许 0 个视觉组合，保存设置
不推进账本 epoch），此时只写指纹会让账本指向一次从未发生的审查。

判定为 `none`（或描述里没有截图）的集合会**结清**：指纹以 `u:` 前缀写入，
同一集合（指纹相同）之后任何同步事件（含 UI 又改动、手动重跑）直接判无截
图、不再重探；集合变化（指纹不同）自动恢复探测。`unavailable` 不写指纹：
拉不到是暂时性的，下次同步重试。

「拉不到」时**不建视觉运行**，`视觉审查` 写一条明确的 failure（「截图无法
下载，未做视觉审查：<原因>」，原因为中文可读文案，网络层英文报错只进日志，
且 URL 与错误文本先脱敏——`*url.Error` 内嵌重定向后的完整地址，预签名
query 不落日志），且**不写指纹**。这里的重试发生在**下一次同步事件**（新
推送产生新 SHA、或手动重跑；reopened 对同一 SHA 不会再次进入同步——webhook
把已有运行的事件一律忽略），系统不为「截图不可用」单独排定时重试；每次事
件都写当前原因，不沿用上一轮的旧描述。上一轮视觉审查有 blocker / worth
fixing 时，这个分支的代码审查结论同样加「截图复核意见尚未处理」（与沿用
failure 分支口径一致；上一轮通过或没有上一轮时不加）。

条件 5 不满足（截图内容没变）且上一轮视觉审查有 blocker / worth fixing：
**不新开视觉轮**，把上一轮的 failure 结论带到新 HEAD 的 `视觉审查` status，
并在代码审查结论里加「截图复核意见尚未处理」。条件 5 不满足且上一轮视觉通
过：沿用 success 到新 HEAD（「审查通过（沿用上一轮，截图未变化）」）。截图
内容变了或有新 UI 改动时不沿用；描述里没有可服务截图（`u:` 结清态）时也
不沿用——上一轮的结论不是这批材料上的结论，宁可不写 status，也不冒充。

**未解决的问题计入结论**：视觉 failure 的判定与条数（`visualFailureCount`）、
Web「需要处理」计数、审查记录投影（`review_records.go` 的 `findings_count`）
都取「本轮 `findings` 里的 blocker/worth_fixing」+「`prior` 里阻塞的
`status=open` 条目」。阻塞口径由 `priorOpenBlocks` 判定：`minor` / `P2` /
`Note` 不阻塞，其余（含 `nil` 与未知值）一律阻塞——模型显式标了「仍未解决」
却漏写级别时不能让结论变绿（新提交由 `validateVisualResult` 直接拒绝：open
必须带 blocker/worth_fixing/minor 级别）。评论的「仍未解决」段、下一轮视觉
历史与记录详情里的 `findings[]` 保留 `prior(open)` 的**全部**条目（含未分级 /
minor），比计数口径更宽——展示用宽口径，红绿用阻塞口径；记录详情里这些条目
带「上轮遗留：」前缀，避免被误读成本轮新发现。只写进 `prior(open)` 的遗留问
题因此不会从状态、评论、历史、记录里消失（musiver PR #563 的红绿反复正是它
被三处一起忽略导致的）。

失败原因按**错误本身**归类，不按错误外壳嗅探：本地缓存操作失败（建目录、
建临时文件、写盘、改名、读缓存）由下载面在产生点打上
`githubmedia.ErrLocalCache` sentinel，service 层据此给「截图缓存写入失败」；
网络错误（含 dial 的 syscall 形态）一律归「无法连接图片服务器」，不会被误
判成磁盘故障。

截图素材：系统按 GFM 规范解析 PR 描述收集截图 URL（goldmark AST：GFM +
Linkify；去重保序，label 取 alt 或「截图 N」）。收集走 AST 而不是正则——
「URL 到哪结束」是规范已定义的问题，逐字符正则重建它每轮审查都会漏一种包
法。认三个来源，按文档序（walk 顺序）合并：

- `![alt](url)`（ast.Image）：行内、尖括号目的地 `![](<u(1).png>)`、引用式
  `![a][id]` + 定义行都由 goldmark 填充同一个 Destination，label 递归取 alt
  里的全部文本节点（`![**登录页** v1](u)` 这类带行内格式的也拼得出来）；
  没有定义的引用不产节点，自然不收集。Markdown 目的地不解码 HTML 实体
  （CommonMark 行为，`&amp;` 原样保留），与 HTML 来源口径不同，接受该边界。
- HTML `<img src>`（ast.RawHTML 行内段 / ast.HTMLBlock 块）：块先按行拼接
  成完整 literal（`<img\n src=...>` 跨行写法的各个 fragment 都不是完整元素，
  逐行解析会漏收），再交给 `golang.org/x/net/html` 解析取 src，实体由解析器
  解码（`&amp;` → `&`，修正旧正则原样收进 URL 的问题）。
- 普通链接目的地（ast.Link 的 `[label](u)` 与 `<a href>`）：作者没写成图片
  语法但目的地常是附件/位图直链（沿用旧 bareAttachRe 的覆盖面），边界由
  解析器精确给出不裁尾，准入与裸链同口径——GitHub 附件形态 ∪ 位图扩展
  名，普通网页链接不误收。
- 裸链（ast.AutoLink，Linkify 产出）：URL 边界由 GFM 定义——ASCII 尾标点
  `?!.,:*_~` 与括号配对、结尾 `&xxx;` 实体由 Linkify 剥；URL 前面必须是空
  白/行首/`*`/`_`/`~`/`(` 才触发自动链接（cmark-gfm 同款，GitHub 渲染同款：
  紧贴直角引号的 `「https://…」` 在 GitHub 上就是纯文本，不收集）。
  `www.` 前缀裸链的 scheme 是 Linkify 按 GFM 补的 `http://`（作者没写协
  议、GitHub 渲染成可点链接），收集时升级 `https` 再进准入；显式写
  `http://` 的裸链不升级，准入照常拒收。
- 行内代码、围栏代码块、HTML 注释不产链接节点，自然不收集（旧正则会误收）。

三个来源的 URL 统一剥 `#fragment`（不是资源本身，下载面 normalize 会拒、
`IsGitHubMediaURL` 也因此判 false 导致 bearer 重试失效）；裸链再做尾标点兜
底裁剪——cutset 为 GFM 尾标点集 `?!.,:*_~'` ∪ Unicode `Pe`（右闭合）∪
`Pf`（右引号）∪ 中文句读 `。，、；：！？…`，并补一次「`)` 多于 `(`」的括
号配对（Linkify 已做过一轮，中文句读等非 GFM 字符不归它管）；只裁尾部不动
中段。显式声明的 URL（`![]()` / `<img src>`）一律不裁尾——写在括号/引号里
边界明确，裁尾会吃掉合法字符（如查询串结尾的 `!`）。ASCII `;` 不在裁剪集
内（Linkify 保留，与 GitHub 渲染一致），粘了分号的裸链不收集。

结构准入严格镜像下载面 normalize 的拒绝集：scheme 必须 `https` 且 host 非空
（`http`、`data:`、相对路径不收）、不能带 userinfo；**不按扩展名拒绝**——
显式声明的图交给探测的 Content-Type 裁决（`render.php?id=1` 这类无扩展名直
链可能是真图，扩展名误判会漏收）；「全部不是图」由探测三态收敛成
`visualNone`，不会每个同步事件刷「截图无法下载」failure。裸链没有图片语法
背书，在此之上额外按「GitHub 附件形态 ∪ 位图扩展名」准入，否则描述里每个
无扩展名的网页链接都会被当成截图。附件形态是**公共形态 ∪ 本部署实例形态**
（`githubmedia.IsGitHubAttachmentURLFor`）：公共形态
（`github.com/user-attachments/assets/…`、`user-images.githubusercontent.com/…`）
在任何部署下都收——GHE 的 PR 引用公共 GitHub 截图同样合理；本实例形态按
部署推导的 Web 地址判定（`githubmedia.GitHubWebHost`：`github_app.api_base_url`
为空或 `api.github.com` → `github.com`，GHE 的 `https://<host>[:port]/api/v3` →
同一 `host[:port]`，默认端口去掉），GHE 收
`https://<ghe-host>/user-attachments/assets/…`
（拖入/粘贴附件的标准直链，通常无扩展名）。附件路径要求解码后的规范形态，
点段/编码点段（`/../`、`%2e%2e`）一律不算。写死 `github.com` 会让 GHE 部署
的附件直链被静默丢掉，`HasScreenshots=false` 后视觉审查不启动。这个启发式
会误收正文里恰好以 `.png` 结尾的普通链接，接受。
由**独立的截图下载面**（`githubmedia.NewReviewMediaService`，与
`/api/github/media-proxy` 的 GitHub 白名单面是两个实例）下载落盘为
`review_assets`；同一 (repo, pr, head) 已有 asset 行复用，失败的行保留
中文原因在 `review_assets.error` 并在下次 ensure 重试。下载面允许**任意公网
https** 主机，内容只收位图：`image/*`（svg 即使标着 `image/svg+xml` 也不
收，非标准写法 `image/svg` 一并按前缀拒），
或 `application/octet-stream` 且 URL 是位图扩展名；上游没有 Content-Type 时
同样按位图扩展名回退——URL 是位图扩展名才收，否则拒；视频不收。防 SSRF 不靠
域名白名单，
而在拨号时校验解析出的 IP（`net.Dialer.Control`，因此这条下载面不走
`HTTPS_PROXY` 等环境代理——走代理时校验到的会是代理 IP 而非目标 IP）——
回环/内网/链路本地/CGNAT/NAT64·6to4·Teredo 内嵌的 IPv4 等地址直接拒连
（NAT64 `64:ff9b::/32` 内只有 well-known `/96`——bytes 4-11 全零——会解码
末 4 字节的内嵌 IPv4 再递归判定；其余前缀长度（RFC 6052 的 /40 /48 /56 /64
布局与 RFC 8215 的 local-use `/48`）布局不可安全解码，**整体拒绝**——内嵌
公网地址的也一并拒，宁可错拒不可放过），
重定向目标与 DNS 重绑定的每次建连同样被拦。唯一的例外是部署配置的可信
内网主机名单（`review_media.trusted_private_hosts`，INT-168）：拨号主机名
与名单完全一致（不区分大小写、不含端口、不支持通配符）时跳过这步 IP
校验——内网 DNS 把 UIV 这类自建服务直接解析到内网地址的部署靠它放行；
放行粒度是主机名不是 IP，同一内网地址上挂着的其他服务不连带放行，
名单外主机、以及名单内主机 302 跳出的建连照旧校验。App 安装令牌（bearer）只发往
**本部署的 GitHub 实例**、且只在匿名被 401/403/404 拒时重试一次（私有附件
对匿名请求正是回这两个码）：外部图床匿名失败即止，协议不支持、不是图片、
超限、内网地址这类确定性失败也不重试。凭证闸门按部署推导
（`IsGitHubMediaURLFor`）：公共 GitHub 部署沿用 `IsGitHubMediaURL` 的媒体域
白名单（`github.com` 上的附件路径同样要求解码后的规范形态，点段不能借前缀
把令牌带出附件目录）；GHE 部署只放行本实例 authority（含端口，`https://<host>:<port>/…`
的端口不同即不算同一实例；默认端口按 scheme 判定，https 的 `:80` 保留）上的
规范附件路径，公共 GitHub 域也不发——令牌只属本实例，发出去没有意义。媒体
适配器（`ProxyService`）自己发送前也用同一闸门再校一次
（`NewReviewMediaService` 构造时注入部署 authority）。

缓存按授权域分键：令牌拉取的条目键里带 `install:<id>` 授权域——私有附件
URL 即使被第三方知道，匿名或其他安装的请求也命中不了这条缓存（各是各的
条目，互不覆写）；bearer 本身不落盘。匿名条目照旧是纯 URL 键，公开附件
照常共享。分键上线前的存量条目来源不可辨（meta 无 Scope 字段、无法区分
公开/私有），服务构造时以 `.cache-scope-v2` 标记做一次性清场——公开内容
重拉即可，私有内容宁可重拉不可误给。指纹 = 排序后 URL 列表的
SHA-256 前 16 位。

## 工作区回收

运行进终态（完成、失败、超时、取消）后分两步：

- **归档**（`archiveRuns` 阶段）：线程「可能已建成」的终态运行
  （`dispatch_started_at` 非空，代码与视觉都归档；含 `dispatched_at` 为空
  的结果未确认运行——线程可能实际已建成，不停掉会永久游离）逐个
  `thread.archive`，成功写 `archived_at`；失败按进程内计数有限重试
  （3 个 tick），超限置 `archived_at` 防无限重试并发聚合通知
  （reason=`archive_failed`），代码运行同时置 `worktree_state=failed`
  （视觉运行状态不动，但也落 `worktree_reason` 供聚合清单统计）。
  确定没有建成线程的（`reviewRunNeverLaunched`：`dispatch_failed` 请求帧
  未发出、`pr_closed` 在建线程前就被拦下）跳过——这类运行无线程可归，
  误归档只会制造回收失败假告警。
  - **归档前收尾宽限**（INT-158，`reviewArchiveSettleWait` = 10 分钟）：结果
    落库后 agent 的 T3 run 往往还在写收尾回复，立刻归档会切断 provider 事件
    流，T3 把 run 误记为 failed，留下 settled=false 的僵尸线程。`finished_at`
    之后的宽限窗口内，归档先读线程投影判定运行绑定的 run（按
    `userMessageId` 匹配 `initial_message_id`，同一消息多条 run 取 ordinal
    最大，与准备失败判定同口径）是否已收尾：run 未落已知终态集——活动态
    （preparing/starting/running/waiting）、queued 或其它未识别状态——
    本 tick 不归档，等下轮再试（等待不占归档有限重试计数，宽限即上限；
    「不在活动集」不等于「已收尾」，只有 completed/failed/stopped/
    interrupted/cancelled/error 能证明回合已结束）；run 已终态 → 正常
    归档；线程已归档/已删除（墓碑投影可读）→ 目标态已达成，直接落
    `archived_at` 不走归档 RPC（墓碑线程的非幂等报错会被误记成归档失
    败）；投影里没有该消息的 run、`initial_message_id`
    为空（存量行，空串会误撞缺 `userMessageId` 的 run）→ 不能证明已收尾，
    同样等宽限兜底；线程 404 不立即采信（T3 重启/抖动，与
    `cleanupRunWorktree` 同口径）→ 连续到上限认定无可归对象，直接落
    `archived_at`（无线程的归档 RPC 只会误记归档失败、发假告警），中途读
    成功清零连续计数；读投影其他错误 →
    本 tick 跳过、下轮再试。`finished_at` 为空或已超宽限 → 跳过等待直接
    归档（防御 + 兜底，不会有永久不归档的运行）。归档 RPC 失败且行预算未
    耗尽时补读一次投影归因（dispatchCommand 对不存在的线程只回普通上游错
    误）：归因 404 与窗口内同一连续计数，到上限直接落账不误记归档失败；
    归因读回墓碑（已归档/已删除）同样直接落账；证实线程存在且未墓碑才按
    真实归档错误计重试。未确认启动的运行在宽限
    等待期间每个 tick 仍会再发一次 `StopThread`（幂等，帮孤儿回合尽快终止）。
    单行归档动作（停线程 + 收尾判定 + 归档 RPC）共享 60s 行预算
    （`reviewNetworkStageBudget`，各 T3 调用内部另有 `t3WriteBudget` 25s
    收紧）：前序步骤耗尽行预算时归档是被掐断而非被拒绝，按等待处理不占
    归档有限重试计数，下轮重试——宽限过后收尾判定跳过，归档独享行预算。
- **删除**（`cleanupWorktrees` 阶段，INT-170 起两步模型）：先给确定没建线程
  的运行直接收尾（`reviewRunNeverLaunched` → `worktree_state=removed`，
  无线程/无工作区可清）；其余代码运行要等归档完成（`archived_at` 非空）才
  进入回收，查询条件含 `worktree_state IN ('none','branch_pending')`。
  **全量扫描 + 到期预判**（不再 `LIMIT 20` 截断查询）：循环顶部先做不占名额
  的到期预判——`branch_pending` 行查其复查退避、`none` 行的
  `registry_incomplete`/`remove_uncertain`/`remove_budget_pending`/
  `remove_unavailable` 查各自重试退避，未到点整行跳过
  （不读登记、不发网络请求）；否则 20 条常驻 pending 行会把后面的行永久饿死
  在批量窗口外。实际处理仍受双上限（先到先停）：每轮最多
  `reviewStageBatch`（20）条进入单行处理，且整阶段共享
  `reviewNetworkStageBudget`（60s，与其他网络阶段同款）。单行函数内部的到期
  检查保留，防直接调用的路径绕过预判。回收分两步独立判定：
  - **第一步·工作树**：真值来自线程投影（`GET /api/orchestration/threads/:id`
    的 `projection.thread.worktreePath`，已归档线程同样可读）；launch 确认时
    T3 往往还没建好 worktree，启动前后做差集推断必然歧义，该方案已废弃，
    `launchRun` 不再解析路径。投影读到后路径与分支「空才补」回写运行行
    （`worktree_path`/`worktree_branch`；两列都要兼容旧库 NULL——新增可空列
    升级前已有的行是 NULL，等值条件匹配不到空串会静默写不进，条件必须带
    `IS NULL`；分支写失败或未命中与 path 同口径直接返回等下轮，不持久化就
    不进入依赖这份快照的状态，进 `branch_pending` 前断言行内分支非空），
    线程之后 404 也不丢真值。
  - **第二步·分支**：远程 T3 **没有删除分支的 RPC**（`vcs.removeWorktree`
    只做 `git worktree remove`；内部 `deleteLocalBranch` 未对外暴露）——
    分支删除由**审查代理收尾**执行（提示词「收尾」节：先核验
    `git rev-parse refs/heads/<分支>` 仍等于 `head_sha`——必须用 `refs/heads/`
    完整引用名，git 对裸名字先解析 `refs/tags/`，同名 tag 指向预期提交而分支
    已被推进时裸名核验会误过、`branch -D` 会删掉被推进的分支；worktree list
    里该分支只被本轮工作树占用；分支名不等于 `head_ref`/`base_ref`/主分支，
    才 `cd <主检出> && git worktree remove --force … && git branch -D …`。
    `git branch --show-current` 为空（detached HEAD）时从 `git worktree list
    --porcelain` 本工作树条目取 `branch refs/heads/<名>`，仍取不到就跳过分
    支步、只删工作树）。服务端只核验登记 **本地** ref 名
    全集（`vcs.listRefs` 的 `refKind=all` 含远端跟踪 ref，收集时按 `isRemote`
    剔除——`HasRef` 要回答的是「本地分支仍在不在」，同名远端 ref 不算存活
    证据）：分支仍在 → `branch_pending` 复查；消失（ref 整条不在）→ `removed`。
  - 线程 404、读线程失败、线程 `worktreePath` 为空（T3 仍在准备）各自独立
    计数、有限重试（3 个 tick），成功一次即清零该原因的计数（按「连续失败」
    而非累计）：404 连续 3 次都读不到才采信，行内已有 path/branch 快照的改用
    快照继续走登记核验（线程删了工作区可能还在），行内无快照 → `removed`
    （磁盘工作区归 T3 生命周期管）；行内仅无路径但有分支快照（投影曾给出
    branch 而 path 空）同样走登记判定——分支可能仍存活，不能直接
    `removed` 静默残留；读线程失败到上限 →
    `failed`（reason=`thread_read`）+ 聚合通知；空路径到上限 → 行内分支也空
    则 `removed`（无工作区），分支非空则跳过工作树步直接判分支。
  - 行内旧 `worktree_path`/`worktree_branch` 与线程真值不一致 →
    `failed`（reason=`ownership_mismatch`）+ 通知，不删。分支只在**两边非空
    且不等**时判死：投影 `branch` 可空，重读丢字段拿到的空串不能推翻行内真
    值（`verifyWorktreeOwnership` 自己就容忍 branch 空）——投影为空时回落行
    内快照继续；路径字段的严格度不动（路径是删除对象本身）。
  - **登记读取**（`vcs.listRefs`，`limit=200`/页，按 `nextCursor` 翻到耗尽，
    不再固定 8 页截断——Musiver 仓库 ~800 引用就撞过旧上限误报）：整段翻页
    共享 `t3WriteBudget`（25s）总预算，每页发出前查 ctx；三重异常约束：预算
    耗尽（含页内调用被 ctx 掐断）→ 可判定的「登记读取不完整」错误；游标
    不推进/复现已见值 → 明确上游异常；512 页兜底上限（≈10 万引用）同样明确
    报错。**预算耗尽先待确认后收敛**：行保持 `none`、reason=`registry_incomplete`
    挂指数退避（见下），连续 ≥3 次起发聚合通知；连续 12 次（封顶 5min 退避约
    1h 周期）仍读不完整 → `failed`（reason=`registry_error`，长期读不完整视
    同登记读失败交人工）——待确认不是无限期状态；登记读成功即清登记侧
    原因并无条件复位 `registry-incomplete` 计数（复位先于原因判空——
    原因写库失败而读成功反复出现时，不复位会把「连续失败」退化成跨成功
    累计、叠满上限误收 failed；`remove_budget_pending`/`remove_unavailable`
    两个删除侧待确认原因不清，消解只看 RemoveWorktree 重试结果）。
    `vcs.removeWorktree` 前的环境解析/拨号把预算耗到点也挂待确认退避但分键
    `remove_budget_pending`（同一收敛上限，届时终态 reason=`worktree_remove`；
    与「登记读取不完整」分键避免聚合通知指向错误根因；连续计数也用独立
    `remove-budget` 键——登记读成功不复位它，消解由 RemoveWorktree 重试
    结果决定）；预算内就明确失败
    的（凭证被拒、协议不符、连接被拒）仍按上游错误走有限重试。
    上游明确报错 → reason=`registry_error` 有限重试 → `failed` + 聚合通知。
  - **归属核验与删除**（核验与删除用**同一个 cwd**，即 launch 快照的
    `workspace_root`；登记只读一次、核验与删除共用同一份快照）：行快照仍属
    本运行（repo/pr/head/thread/path 一致）、没有仍待回收的运行认领同一路径
    （只看 `worktree_state=none`；`branch_pending` 行的工作树已删不算认领）、
    该路径仍登记在 `vcs.listRefs` 且登记 ref 名含线程 `branch`（`branch` 空
    时只要求登记命中）。未登记 → 工作树步完成（代理已删或从未建成），不调
    删除；核验通过才 `vcs.removeWorktree{cwd, path, force:true}`（WS RPC，
    `git worktree remove --force`）。
  - **删除三分支**（INT-170，按错误发生的位置分）：**结果不可见**——WS 传输
    层失败（请求帧没写出、读超时、ctx 取消引起的连接关闭），远端可能已执行
    完，不直接 `failed`，先重读一次登记：路径已不在 → 工作树步完成；仍在 →
    保持 `none`、reason=`remove_uncertain` 挂退避，每次重发删除前都重过归属
    核验，连续 3 次 → `failed` + 聚合通知。**未送达**——请求发出前的准备失
    败（环境解析/凭证不可用/会话探测/WS 拨号，`errT3RemoveNotAttempted`），
    远端必然没执行、复查也读不出「没发生」的删除；与登记侧同根因（T3 停机/
    维护）同口径挂待确认——reason=`remove_unavailable` + 退避 + 独立
    `remove-attempt` 连续计数（登记读成功不复位，消解只看 RemoveWorktree
    重试结果），连续 12 次 → `failed`（reason=`worktree_remove`，长期不可达
    视同删除失败）+ 聚合通知。**明确拒绝**——
    Exit Failure 也先重读一次登记（报错与执行竞态），已不在即完成；仍在 →
    `failed`（reason=`worktree_remove`）+ 通知。
  - **分支步**：分支名缺失且行内有工作树路径时，先借登记恢复名字——登记
    中该路径恰有唯一 ref 名、且该 ref 未同时绑在别的工作区路径时采用
    （恢复名在通知文案注明「由登记推断」；同时绑在别路径视为歧义）。仍恢
    复不出且无法证明「没有专属分支残
    留」的两类场景都 `failed`（reason=`branch_unknown`，交人工按路径核
    查）：线程 404 只剩行快照（升级前 NULL 行）；或登记把该路径绑定到多
    个 ref 名、无法唯一映射（活投影 `branch` 空也可能只是丢字段，证据力
    与快照相同）。活着的投影明说无分支**且**登记路径无绑定 ref 才是权威
    「没分支」→ `removed`；路径与分支都拿不到同样 `removed`。分支仍在登
    记 ref 名全集 → `worktree_state=branch_pending` + 聚合通知；不在 →
    `removed`。
    `branch_pending` 行按退避复查（只查登记，不读投影），分支消失 →
    `removed` 并刷新聚合通知（受影响清单收缩）；环境绑定缺失解析不出
    `workspace_root` 时同样连续计数（`branch-env` 键），到 24 次上限收敛
    `failed`（reason=`env_binding`）——绑定永久缺失不留无信号滞留；登记预算耗尽与 none 态同
    口径收敛（复用 `registry-incomplete` 计数与 12 次上限 →
    `failed`/`registry_error`）；分支被绑到多个 worktree 时先收集全部绑
    定——行内有快照路径时同路径项优先采信恢复转 none，仅全部绑定都在异
    路径才 `failed`（reason=`ownership_mismatch`）交人工；行内无快照路径
    时只有唯一绑定可采信写回，多绑定即无法指认同样 `failed`；转 none 的
    恢复转移会保留 `branch-pending` 确认存活计数（登记反复出现/消失不会
    重置收敛进度）；确认存活累计 24
    次（封顶 1h 退避 ≈1 天）仍未消失同样收敛 `failed`（reason=
    `branch_pending`，提示人工清理）——待清理不是无限期状态；复查登记明
    确报错到上限同样降级 `failed`（`registry_error`，终态统一走
    `finishWorktreeCleanup`，旧 `branch_pending` 键随转移自动刷新）。
  - **指数退避**（INT-170）：`branch_pending` 复查、`registry_incomplete`、
    `remove_uncertain`、`remove_budget_pending` 与 `remove_unavailable`
    重试按 runID+reason 存进程内退避表，30s 起指数增长；
    封顶按原因给——重试类 5min，`branch_pending` 长期等外部动作（代理收尾或
    人工删分支）封顶 1h；未到点的行在阶段入口就被到期预判整个跳过（不重读大
    登记、不占处理名额）。进程重启丢失无所谓——下个 tick 即可重查。终态转移
    时随重试计数一并释放；恢复（`recoverFromPending`）时按原原因清退避条目，
    再入同一待确认态从起步间隔重爬梯。
- 删除项目时的 best-effort 回收与常规回收共用同一套归属核验：只有线程投影
  成功读到、且与行内快照一致时才继续，读不到/路径冲突/核验不过都只记日志
  跳过——项目行 CASCADE 后没有重试机会，宁缺毋滥。删除后分支仍登记在案只记
  日志（无删分支 RPC），不阻断项目删除。
- 视觉运行 `worktree_state` 恒 `none`，只归档线程、无工作区可删。

## 页面

- 侧边栏「审查」`/reviews`（`web/src/routes/reviews/index.tsx`）= PR 动态：
  顶部按项目筛选（默认全部，`?project=` 保留筛选），右上角「⚙ 配置
  （N 个项目已开启）」进入配置页。PR 按「你要做什么」分三组，**分组由服务端
  算好**（`GET /api/reviews/pull-requests`），前端只负责展示：
  - **进行中**：防抖中（「约 42 秒后开始」）、排队、审查中；行上写 PR 标题、
    仓库 #号、第几轮、短 SHA、已用时长与两段进度（「代码审查 2/3 完成 →
    视觉审查 等待代码审查」）。执行状态只看当前轮、当前 head 的运行，账本
    `code_status/visual_status` 的 pending 不会把已终态的轮次判成进行中。
  - **需要处理**：发现 P0/P1/P2（视觉 blocker/worth_fixing 单独写「视觉问题
    ×N」），或审查未完成（逐条写组合与原因：派发失败 / 超时 / 审查失败 /
    结论无法解析；账本 failure/error 在无更细证据时给保守兜底文案）。行上
    直接放「重新审查」。
  - **最近通过**：当前轮 runs 全部通过且完成时间在近 7 天内，默认折叠。
  存在进行中的 PR 时前端 10s 轮询，否则不轮询；两种空状态（没有项目开启 →
  引导去配置；已开启但无 PR → 「推送或打开 PR 后会出现在这里」）。
- 点 PR 打开右侧抽屉（`components/reviews/pr-activity-drawer.tsx`）替代原
  按项目的「审查记录」tab：顶部「在 GitHub 打开」；每轮写轮次、SHA、触发方式
  （opened/synchronize/ready_for_review/reopened/manual）、时间；每个组合一行
  结论（环境、模型、状态、summary、错误）并可跳 `/threads/:env/:threadId`；
  发现列表按级别渲染（代码 `文件:行号`，视觉 `截图 · 维度` + 建议），视觉的
  `unmeasurable` 单列「无法判断」；最新一轮展开，更早的轮折叠成一行灰字。
  进行中的 PR 在抽屉打开期间同样 10s 刷新。
- `/reviews/config`（`web/src/routes/reviews/config.tsx`）：配置页承接旧项目表
  并瘦身——已开启项目在上（项目名、实际审查的 owner/repo、代码/视觉组合数、
  最近结论、打开单项目设置）；未开启折叠为「其余 N 个项目未开启 · 展开」。
  同仓库被其他项目占用时显示占用者登录名；旧 `/automations` 路由直接删除，
  不做重定向。
- `/reviews/:projectId`（`web/src/routes/reviews/$projectId.tsx`）：只保留设置
  （不再有 `?tab=`）——前置条件全部满足时收成一行「✓ 前置条件已满足」，有
  缺项才展开逐条标红；开关、四个触发条件复选、代码审查组合 1–5 行（必填）、
  视觉审查组合 0–3 行。组合行（`components/reviews/combination-row.tsx`）= 环境
  `OptionSelect`（只列已通过 `project_t3_bindings` 绑定该项目的环境）+
  `ModelPicker` + 每个选项描述符一个 `EffortChip`；切换环境清空该行下游选择。
  `can_manage=false` 或 `taken_by` 非空时整页只读。

首页与配置页的项目元数据都来自 `GET /api/reviews`；PR 动态列表不返回旧仓库
（项目换过审查仓库后，旧仓库账本只在 `/review-records` 接口可见）。

## HTTP API（JWT）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/reviews` | 审查总览（配置页与首页的项目元数据）：每项目一行（仓库 = 实际审查的代码仓库、开关、组合数、最近 code/visual status、`last_review_at`）；同仓库被占用时 `taken_by` 带占用者登录名且 `can_manage=false`。 |
| GET | `/api/reviews/pull-requests` | 跨项目 PR 动态：`?project_id=&limit=&offset=`（limit 默认 50、上限 200）。以 `review_pull_requests` 为主，带当前轮各组合运行状态、防抖行、发现计数与未完成原因；**分组由服务端算好**（in_progress / needs_action / recently_passed），返回 `items[]`、`counts`、`total`。只返回已 enabled 且审核仓库 = 设置行快照的项目；当前 head 没有运行且没有防抖的账本不返回。 |
| GET | `/api/reviews/pull-requests/{prNumber}` | PR 详情（抽屉）：`?project_id=` 必填。复用 review-records 结构并补每轮 `trigger_action`、每个运行的 `findings[]`（代码含 Note；视觉 severity/dimension/fix/screenshot）与视觉 `unmeasurable[]`。项目不属于当前用户 40401；账本不存在 40434。停用项目仍可看历史。 |
| GET | `/api/projects/{id}/review-settings` | 设置 + `repo_full_name`（实际审查的代码仓库，原样大小写）+ `prerequisites[]`（缺项 `missing=true` + `detail`）+ `taken_by`。prerequisite 的 `github_app_installation` 每次实时调一次 GitHub。 |
| PUT | `/api/projects/{id}/review-settings` | 全量保存。组合结构/归属/绑定/数量/重复问题 40001（只读会话的环境同样拒绝）；启用时前置条件缺项 41202（逐条写进 message）；同仓库占用 40922（带占用者）。`enabled=false` 跳过前置校验、允许零组合。 |
| GET | `/api/projects/{id}/review-records` | 审查记录：`items[]` 每个 PR 一行，内嵌 `rounds[]`（轮次、head、触发方式、时间）与每轮 `code_runs[]`/`visual_runs[]`（含 `findings[]`/`unmeasurable[]`）。`?limit=` 默认 20、上限 100。首页改版后网页不再有入口，接口保留。 |
| POST | `/api/projects/{id}/reviews/rerun` | 重新审查：body `{pr_number}`。项目必须 enabled（否则 40923）。rerun 没有 webhook installation 快照：先 `ResolveInstallation` 解析真实安装 id（解析不支持/失败直接报错，不拿 0 继续），再用安装令牌 `GetPullRequest` 实时取当前 head，走与 webhook 相同的「取消旧 SHA + 开轮」事务路径（`trigger_action=manual`，恒开新轮），安装 id 落进运行与总账。PR 已关闭/合并时直接报错（40001「PR 已关闭」），不开轮。返回 `{accepted, head_sha, round_number}`。 |

## 开启前置与同仓库唯一

启用时五项前置条件必须全部满足（`review_settings.go` 的
`reviewPrerequisites`；`GET` 每次实时计算，缺哪项逐条列出）。仓库相关判定
一律按实际审查的代码仓库（`project.CodeRepo()`，未单独配置即公开仓库）：

1. 部署 GitHub App 已配置（`deployment_settings` 的 App 身份；
   `github_app.fake` + `server.mode=debug` 视为已配置）。
2. App 已安装到该仓库：用安装令牌调 `GetRepository`，失败即未安装。
3. `server.public_url` 已配置（Agent 端点基址进提示词，不从 Host 推断）。
4. 项目绑定了公开或代码仓库 Token（`public_github_token_id` /
   `code_github_token_id`）。
5. 每个组合的环境属于当前用户，且经 `project_t3_bindings` 绑定了该项目
   （绑定的 `t3_project_id` 快照进组合）。

「项目已关联仓库」（`repoLinked`，不满足时 `github_app_installation` 标缺）
同样按代码仓库判定；未配置代码仓库的项目行为与原先完全一致。

组合本身在 service 边界校验（一律 40001）：`environment_id/instance_id/model`
必填；代码 1–5 个、视觉 0–3 个；按 `environment_id+instance_id+model+
options canonical JSON` 判重（同一搭配不同思考强度算不同组合）。

**环境能力**：组合里的环境必须是可写会话（具备 `orchestration:operate`）；
只读会话在保存时直接 400，前置条件也会把它标成缺项——否则每次派发都只会落
`dispatch_failed`。

**同仓库唯一**：整个部署内同一**集成**下的同一**代码仓库**
（`(integration_id, repo_full_name)` 快照，仓库名大小写不敏感）只允许一个
项目 `enabled=true`；其他用户的同仓库项目在 `GET`/`PUT` 里得到 `taken_by`
（占用者登录名），开启返回 40922。INT-167 起唯一键带集成维度：同名仓库在
GitHub 与 Forgejo 两个集成下可各自被一个项目启用，互不算占用。占用判定只
读 enabled 设置行的快照列（联 `users` 取登录名），不回联项目的 live 仓库
字段。写入路径双保险：保存事务内先查占用者，部分唯一索引
`idx_project_review_settings_repo` 兜并发竞态。

**保存并发复核**：`PUT` 写入事务内会重读项目的审查仓库（`CodeRepo()`，
含 code 两列），与事务外校验用的项目快照不一致 → 400「项目仓库已变更，请
刷新后重试」（并发改仓库时把 enabled 落到旧快照上会写错唯一占用键与
账本分区键）。项目侧改**代码仓库**走 `ProjectRepository.Update`——同事务
停用审查并刷新 `repo_full_name` 快照。

**接管**：某项目成功 `enabled=true` 保存时，同一事务内对同 (集成, 仓库) 的
其他项目做收尾（唯一占用保证此时没有其他 enabled 项目，残留来自曾开启后
停用、或换过仓库/改绑集成的项目；其他集成下的同名仓库不受影响）：

- 其未终态运行（queued/dispatched/running）全部置 `cancelled`、
  `error_code='taken_over'`、`finished_at=now`，运行令牌一并消费；线程
  中断由调度循环照常补发（`stop_attempted_at` 一次）。
- 其 `review_debounces` 行删除（旧 SHA 不再开火）。
- 其 `review_pull_requests.sync_pending` 清掉——旧项目的待同步残留不再
  发 GitHub 产出；配合 `syncPendingPRs` 的 enabled 过滤是双保险。
- 其 `review_seen_shas` 保留：已受理账是项目自己的历史记录，新项目重新
  受理同 SHA 不需要也不应该沿用旧项目的已见集合。

## 通知

`notifications` 表保留（弱引用 + `(user_id, dedup_key)` 唯一），kind 收窄
为四类审查系统故障：`review_dispatch_failed`、`review_run_timeout`、
`review_cleanup_failed`、`review_redelivery_failed`
（`model/notification.go`）。审查发现不发通知。
原 `automation_*` 四类与 `agent` kind、`automation_busy` 随通用自动化删除。

`review_cleanup_failed` 自 INT-170 起按（环境, 仓库, 原因）**聚合**：
ProblemKey = `environment_id|repo_full_name|worktree_reason`，标题按原因给
可读名（`branch_pending`→「PR 审查分支待清理」、`registry_incomplete`/
`remove_uncertain`/`remove_budget_pending`/`remove_unavailable`→「PR 审查
回收待确认」、其余→「PR 审查工作区回收失败」），
正文先写一句原因说明、再逐行列当前受影响运行（查
`worktree_reason=原因 且 worktree_state IN ('none','branch_pending','failed')`，
状态标签按原因给：四个待确认原因→「待确认复查」、
恒 `none` 的视觉运行（`archive_failed`）→「已放弃」、其余 `none`→「未完成」、
`branch_pending`→「分支待清理」、`failed`→「失败」；清单按 limit+1 探测——
超过 20 条才写「…（清单截断）」，恰好 20 条不误报）。
每次发布重算全量清单：dedup 键相同、指纹（正文）变化才更新并清已读，清单没变
只续 `last_seen_at`；问题态恢复（运行落 `removed` 或原因清除）重发一次该键，
正文收缩、全部恢复时写「受影响运行均已回收完成」。发布前先按
`notificationHash(环境, 空, kind, problemKey)` 查该 dedup 键是否已存在：键不
存在且受影响清单为空 → 跳过不建（恢复刷新针对从未发布过的键时，不凭空造一
条「均已回收完成」宣告没被告之过的问题已解决）；键不存在但清单非空 → 正常
建；键存在 → 照常发布更新。终态写行（含旧原因键刷新）统一走
`finishWorktreeCleanup` 唯一入口。逐 run 通知只剩
`review_dispatch_failed` 与 `review_run_timeout`。

## 已知限制

- **单进程**：调度循环、per-PR 锁、防抖消费都在同一进程内；多副本部署会
  重复派发与重复写状态（唯一索引只保证运行行唯一）。
- **fork PR 不审**：head repo ≠ base repo 一律忽略，fork 仓库的 PR 永远
  不会进入审查（Forgejo 轮询补漏同样只在适配器枚举处收同仓库分支的 PR）。
- **Forgejo 运行的兜底链接仍按 GitHub**：记录/时间线的 `pr_url` 优先用运行
  快照（Forgejo 运行开轮时快照的是 Forgejo URL）；快照缺失时的兜底构造仍
  按 GitHub 身份——待记录层按行上 `integration_id` 逐行解析后收口（见
  docs/internal/forge-integrations.md）。
- **私有仓库截图下载仍可能失败**：截图由独立的截图下载面拉取——匿名优先，
  匿名被 401/403/404 拒且有安装 id 时带 App 安装令牌重试一次（App 令牌只对
  GitHub 附件有意义，外部图床始终匿名）；仍拉不到的截图不会进入审查素材，
  视觉审查只审
  下载成功的部分（图片端点对缺失资产 404）。下载失败的资产行保留中文
  `error` 原因，下一次 ensure 会重试；描述里的截图**全部**下载失败时不建
  视觉运行（见「视觉审查衔接」），且系统不为「截图不可用」单独排定时重试
  ——只有下一次同步事件（新推送产生新 SHA、或手动重跑）才会再次探测。
- **截图数量不设上限**：取材范围就是描述里的全部图片引用，不做截断；探测
  有 30s 总时限且第一张成功即停，逐张串行下载不会拖满同步循环。
- **截图下载面不走环境代理**：拨号校验目标 IP 与 `HTTPS_PROXY` 互斥
  （走代理时校验到的是代理 IP），这条面选择直连；GitHub 媒体代理面仍走
  环境代理。
- **本地分支残留**：`git worktree remove` 不删本地分支，远程 T3 0.0.45 也
  没有删分支的 RPC（内部 `deleteLocalBranch` 未对外暴露，`vcs.removeWorktree`
  只删工作树；不要把内部方法当可调用 RPC）。INT-170 起分支回收的主路径是
  **审查代理收尾**（代码审查提示词「收尾」节：核验分支未被推进——必须用
  `git rev-parse refs/heads/<分支>` 完整引用名，防同名 tag 先于 `refs/heads/`
  被解析而遮蔽、未被其他工作树占用、不是 PR 源分支/主干分支后才删）；代理
  没删成的落
  `worktree_state=branch_pending` 复查（退避封顶 1h）+ 聚合通知，人工或代理
  删掉后下轮复查自动收敛。`t3-worktree-gc` 脚本仍是存量残留的兜底。
- **令牌重放窗口**：`expires_at = deadline_at + 5min`，窗口内无效令牌重放
  仍被接受为「已消费」语义外的探测面；终态后一律 40106。
- **历史只认本系统结论**：`/history` 按隐藏标记找回，旧 Review Bot /
  Code Bot 时代的评论不会进入上下文；升级前的历史轮次视为没有历史。
- **已派发未确认的运行**：`dispatched` 且 `dispatched_at` 为空表示启动 RPC
  结果未确认——账本如实等超时判 `timed_out`，不回捞「线程其实起来了」。
  重认领窗口只覆盖**签发令牌之前**的崩溃缝隙（认领→issueRunToken 之间）；
  令牌一旦签发即不再重认领（随机原值不可恢复，重签会让已建成线程的令牌
  失效），这类运行靠 `deadline_at` 超时兜底；已建线程若存活，首次鉴权
  会补 `dispatched_at` 并照常进入 running/归档路径。
- **改代码仓库 URL 或集成绑定会自动停用审查**：项目更新**代码仓库**地址
  （未单独配置代码仓库时，改公开仓库等同改代码仓库）或改绑代码集成时，
  同一个事务里把 `project_review_settings` 置 `enabled=false` 并把
  `(integration_id, repo_full_name)` 快照刷成新审查对象——避免旧键继续占着
  唯一索引、新键与别的已启用项目重复。代码仓库已显式配置时只改公开仓库不
  影响审查。用户需要在新仓库上重新开启（前置条件都按新仓库重算）。
- **重投的旧事件走上游既有触发判定**：投递失败对账把旧投递原样重投，触发
  判定仍是 webhook 入口的既有逻辑（seen SHA / `pull_request.updated_at`
  水位 / epoch），重放的 payload 里 PR 仍写着 open。但「投递失败后 PR 已
  被关闭」不再靠超时兜底：三道关卡都以 `GetPullRequest` 的实时 State 为准
  ——防抖到期开轮前（见「调度循环」）、派发建线程前（见「派发」）、GitHub
  产出写入前（见「GitHub 产出」）——已关闭 PR 不会开轮、建线程或写产出。
