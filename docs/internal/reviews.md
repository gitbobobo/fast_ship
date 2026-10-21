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
每 PR 进程锁）按仓库归一——同一仓库任一时刻只有一个项目 enabled。但执行态
（运行、令牌、防抖、seen SHA、PR 总账）全部按 `project_id` 分区：项目换成
另一个项目接管同一仓库时各记一本账，互不吞事件、不混结果。接管语义见
「开启前置与同仓库唯一」。

**审查仓库 = 代码仓库**：项目可以单独配置代码仓库
（`projects.code_github_owner/code_github_repo`，musiver 场景：公开仓库放
Issue 与 Release、无代码；代码在另一仓库）。`project.CodeRepo()` 表达的是
「这个项目审查哪个仓库」的候选值：两个 code 字段都非空时用显式配置，否则
回落公开仓库；Issue 同步/发版/发货仍只走公开仓库，两者互不影响。

### 审查仓库的读取口径

同一个事实存了两份：项目行的 `CodeRepo()`（候选值）与设置行的
`repo_full_name`（生效值，小写）。两条写入路径保证它们恒等：项目改仓库时
`ProjectRepository.Update` 同事务停用审查并刷新快照（disabled 行也刷新），
保存审查设置时 `UpdateSettings` 用 `CodeRepo()` 写入快照并在事务内复核。
因此**不需要**在两者之间做「谁更可信」的兜底，需要的是按用途选列：

| 用途 | 读什么 | 允许出现的位置 |
| --- | --- | --- |
| 写入侧：生成快照、以及必须等于「即将写入的键」的预占预判 | `CodeRepo()` | `UpdateSettings`、`ProjectRepository.Update`、`reviewPrerequisites`、`reviewTakenBy`、`reviewOccupiers` |
| 运行侧：基于已有状态做判定 | 设置行 `repo_full_name`（不按 `enabled` 过滤，除非该判定本身要求已启用） | 触发反查、防抖复核、同步过滤/准入、rerun、记录与总览的「当前仓库」 |
| 展示：保留用户输入的原样大小写 | `CodeRepo()` | 总览的 owner/repo、`GetSettings.repo_full_name` |

`service` 包里的 `TestReviewChainReadsReviewRepoFromSettingsSnapshot` 用 AST
扫描把这条规则钉死：`review_*.go` 里出现未列入白名单的 `CodeRepo()` 读取即
失败。新增判定点时按上表选列，不要回读 live 字段，也不要给运行侧加 fallback
（没有设置行的项目不可能有账本/运行/防抖行）。

## 数据模型

七张表（`server/internal/model/review.go`，ID=UUID，时间一律 UTC）：

| 表 | 关键列 | 说明 |
| --- | --- | --- |
| `project_review_settings` | PK `project_id`；`user_id`；`enabled`；`trigger_opened/synchronize/ready_for_review/reopened`；`code_combinations_json`/`visual_combinations_json` | 项目级开关与组合。组合元素：`environment_id`、`instance_id`（harness/provider 实例）、`model`、`options`（canonical `[{id,value}]` 数组，思考强度等）、`t3_project_id`（保存时由 `project_t3_bindings` 快照，不信任客户端回传）。代码组合启用时 1–5 个必填；视觉组合 0–3 个，0 个即从不启动视觉审查。 |
| `review_runs` | `kind(code\|visual)`、`repo_full_name/pr_number/head_sha/round_number`、`installation_id`、`trigger_action`、`pr_title/pr_url/base_ref/head_ref`、组合列 `environment_id/t3_project_id/instance_id/model/model_options_json`、预分配 `thread_id/launch_command_id/initial_message_id`、`status(queued\|dispatched\|running\|completed\|failed\|timed_out\|cancelled)`、`dispatch_started_at/dispatched_at/started_at/finished_at/deadline_at`、`error_code/error_message`、`summary/result_json`、`review_id/posted_at`、`worktree_path/worktree_state/worktree_error`、`stop_attempted_at`、`archived_at` | 一次审查运行 = 一轮 × 一个组合。同一 (repo, pr, head_sha) 的代码与视觉运行共享 `round_number`（视觉是同一轮内的第二段）。`dispatched_at` 在启动 RPC 结果确认或 Agent 首次持令牌鉴权时写入，为空 = 线程是否建成未确认。`archived_at` 是终态后线程归档写回的时刻；归档有限重试超限后也写入（不再重试），失败原因见 `worktree_error` 与 `review_cleanup_failed` 通知。 |
| `review_run_tokens` | PK `run_id`；`token_hash`（UNIQUE）；`expires_at`；`consumed_at` | 每次运行一个 `fsr_` 令牌，只存完整令牌的 SHA-256。 |
| `review_pull_requests` | PK `(project_id, repo_full_name, pr_number)`；`user_id/installation_id`；`round_number/head_sha`；`code_comment_id/visual_comment_id`；`code_status/visual_status`；`visual_screens_fingerprint`；`sync_pending/sync_epoch/sync_error` | 一个 PR 的审查总账：评论落点、最近一次推送的 commit status 结论、视觉截图指纹、事务外同步标记（置 pending 时 epoch+1，清标记按开始时 epoch 条件写）。主键含 `project_id`：同仓库换项目接管后各一本账。 |
| `review_assets` | `(repo_full_name, pr_number, head_sha, position)`；`label`；`source_url`；`storage_path`；`mime_type`；`size_bytes` | 视觉审查截图：PR 描述里的图片链接经截图下载面（`NewReviewMediaService`）下载后本地落盘。内容是仓库归一的共享产物（同一 head 同一批截图），不按项目分区。 |
| `review_debounces` | PK `(project_id, repo_full_name, pr_number)`；`head_sha/action/installation_id/pr_title/pr_url/base_ref/head_ref`；`fire_at` | 防抖窗口。窗口内新推送只更新 head 并推后 `fire_at`；固定 60s，不提供配置。 |
| `review_seen_shas` | PK `(project_id, repo_full_name, pr_number, head_sha)`；`pr_updated_at`；`accepted_at` | 某项目已受理过的 head SHA：force-push 回旧 SHA 不再开轮。按项目分区——接管后新项目对同仓库同一 SHA 重新受理。 |

外键：`project_review_settings` → `projects`/`users` CASCADE；
`review_run_tokens.run_id` → `review_runs` CASCADE；`review_runs` →
`users`/`projects` CASCADE。T3 项目/线程/命令 ID 不建 FK（远端生命周期
不由本库管理）。

`project_review_settings.repo_full_name` 是项目**实际审查的代码仓库**快照
（`project.CodeRepo()` 小写，未单独配置代码仓库即公开仓库）：同仓库唯一
占用、webhook 反查、同步过滤都只认它。项目行新增
`code_github_owner/code_github_repo` 两列（空 = 与公开仓库相同），
`ProjectRepository.Update` 检测到 `CodeRepo()` 变更时同事务停用审查并刷新
该快照。

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
- `review_assets(repo_full_name, pr_number, head_sha, position)`。

## 触发与防抖（`review.go` 的 webhook 事务）

`POST /api/github/webhooks` 路径与验签不变（`github_webhook.go` +
`pkg/githubapp`：密钥未配置 503、签名不符 40107、`X-GitHub-Delivery`
重复投递幂等 202）。幂等登记之后同一事务内：

1. 只处理 `pull_request` 的 `opened` / `synchronize` / `ready_for_review` /
   `reopened`；其余事件与 action 忽略。
2. draft、fork（head repo ≠ base repo）忽略。
3. 反查项目：`project_review_settings.enabled AND repo_full_name = LOWER(?)`
   （只按设置行的仓库快照，一个部署内最多一个，见「同仓库唯一」）。没开启
   → 忽略，不取消、不开轮。公开仓库 ≠ 代码仓库时，只有代码仓库的事件能
   反查到项目；公开仓库的事件静默忽略。
4. action 未被该项目触发条件勾选 → 忽略（不取消、不改 seen）。
5. 过期 SHA 判定（全部按 `project_id + repo + pr`，其他项目的同仓库残留行
   不进入判定）：当前 head 优先取未开火的防抖行，否则取本项目
   `review_pull_requests.head_sha`；payload `pull_request.updated_at` ≤ 已受理
   head 的 `pr_updated_at` → 忽略；该 SHA 已在本项目 `review_seen_shas` 但不是
   当前 head → 忽略；`action=opened` 且当前 head 非空 → 忽略；该 SHA 本项目
   已有运行 → 忽略。force-push 回已见过的 SHA 不再开轮。
6. 写 `review_seen_shas`（含 `project_id` 与 `pr_updated_at`）。
7. 新 head ≠ 当前 head：把该 PR 下**本项目**所有未终态运行置 `cancelled`、
   `error_code='superseded'`、`finished_at=now`，消费其令牌；删除该 PR 本项目的
   防抖行（旧 SHA 不得开火）；置本项目总账 `sync_pending=true`（其他项目的
   残留由各自停用/接管路径收敛，webhook 不动）。
8. 写/更新防抖行（同一 (project, repo, pr) 只一行），`fire_at=now+60s`。

事务内零 GitHub/T3 调用；提交后非阻塞 kick 调度循环。

## 调度循环（`review_scheduler.go`）

单进程一个循环（10s tick + 缓冲 1 的 kick），每 tick 顺序：

1. **到期防抖**：查 `fire_at<=now` 的防抖行，逐行短事务条件删除后复核——
   项目仍启用、`project_review_settings.repo_full_name` 快照仍与防抖行
   `repo_full_name` 一致（与 webhook 反查、同步准入同口径，不回读项目行
   live 仓库字段）、触发条件仍勾选——成立才开一轮；顺带清理
   超龄 delivery 幂等账（保留 7 天）。
2. **派发认领**：`dispatched` 且未确认建线程、**且从未签发过运行令牌**的
   运行，在认领后超过 5 分钟仍 `dispatched_at IS NULL` 时允许重新认领——
   `CreateThread` 以运行行预分配的 threadId/commandId 幂等，不会建出第二个线程。
   **已签发令牌的运行不再重认领**：令牌是随机值、库中只存 SHA-256，进程
   重启后无法恢复原值；令牌存在即证明 `launchRun` 至少走到过签发点，重签
   会覆盖旧哈希让已建成线程的令牌失效。这类运行保留 `dispatched` 等
   `deadline_at` 超时兜底，不自动重试。
3. **排队提升**：每用户最多 **5 个在途**（dispatched/running），其余
   `queued` 按 `created_at` 依序提升为 `dispatched`，`deadline_at` 重置为
   提升时刻 + 超时（超时从真正开始派发算，不占排队时间）。超时固定
   **代码审查 1h、视觉审查 30min**，常量，不提供配置。
3. **派发**：`dispatched` 且未派发的运行条件 UPDATE 认领
   （`dispatch_started_at`），事务外执行 launch。
4. **超时**：`dispatched/running` 过 `deadline_at` → `timed_out`
   （`error_code='timeout'`），消费令牌、置 `sync_pending`、写
   `review_run_timeout` 通知。
5. **GitHub 同步**：见「GitHub 产出」。只处理项目设置仍 `enabled` 的总账
   行——被接管/停用项目的 `sync_pending` 残留不再发 GitHub 产出。
6. **工作区回收**：见「工作区回收」。
7. 取消运行的线程中断：`cancelled` 且已派发且 `stop_attempted_at` 为空 →
   先条件占标记，再 `StopThread`（`run.interrupt`，每运行只尝试一次）。

所有网络调用在事务外；DB 落库用条件 UPDATE（影响 0 行 = 状态已被并发改写）。

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
- 提示词系统写死（`review_prompts.go` 常量）：代码审查要求自查证（git diff
  + 读文件）、以找 bug 为主、P0/P1/P2/Note 分级、历史处置规则、判断
  `ui_changed` 与 `ui_changed_since_last_visual`；视觉审查按层级/间距/排版/
  颜色/交互/文案/可访问性七维度、blocker/worth_fixing/minor 三级。两份都
  明确「只读审查、不发评论、不改 PR 状态，结论只能经 `/result` 提交」。

## 运行令牌与 Agent 端点（`/api/review-agent/*`）

派发时签发 `fsr_` + 32 字节 base64url，存 SHA-256，
`expires_at = deadline_at + 5min`。路由统一 `runTokenAuth`
（`middleware.RequireRunToken`）：只允许 `fsr_`，`fsk_` API Key 一律
**40304**，无令牌/无效/过期/已消费一律 **40106**。运行必须 `kind` 匹配
端点且 `status ∈ {dispatched, running}`；首次成功鉴权把 `dispatched→running`
落库（`started_at`），**并补 `dispatched_at`**——令牌只经启动提示词交给
线程，Agent 能出示即证明线程已建成（即使启动 RPC 结果未确认），归档阶段
以 `dispatched_at` 非空为前提。运行进终态后所有端点 40106。

| 端点 | 说明 |
| --- | --- |
| `GET /pull-request` | PR 元数据（repo、PR 号、标题、正文、base/head ref、head SHA、URL）；身份全部取自运行行，不接受请求参数。 |
| `GET /pull-request/diff` | PR diff，>1MB 按字节截断并标 `truncated:true`。 |
| `GET /history` | 系统生成的历史上下文 Markdown（无历史时空串）。代码：上一轮（`round_number-1`）所有组合的结构化发现（含组合名）+ 上一轮结论之后的新行内/会话评论；首次审查给全部讨论（最近 50 条）。视觉：上一轮视觉 findings + unmeasurable。**只识别本系统自己发的结论**（按评论隐藏标记找回），不兼容旧 Review Bot / Code Bot 评论。 |
| `GET /screenshots` | 仅视觉运行。`{items:[{id,label,url}], changed}`：`url` 指向图片端点；`changed` = 本次 head 的截图 URL 指纹 ≠ 总账 `visual_screens_fingerprint`。 |
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
当前 HEAD：与运行 `head_sha` 不一致 → 本轮作废（`cancelled/superseded`、
消费令牌、置 `sync_pending`），返回 **40920**。成功后写 `result_json/summary`、
`completed`、`finished_at`，消费令牌、`posted_at` 置空（待同步推 GitHub）、
置 `sync_pending` 并 kick。

## GitHub 产出（`review_sync.go`）

每 PR 一把进程内锁（按 repo+pr 键控，同步完摘除条目，防 map 随历史 PR 数
增长——锁刻意保持仓库归一，同 PR 的 GitHub 写入串行），逐行处理
`review_pull_requests.sync_pending` 中项目设置仍 `enabled` 且账本
`repo_full_name` 与该项目 enabled 设置行快照一致的行（选中后还要过一次准入
复核：pending 未清、epoch 未推进、快照仍是本仓库；通过后每次 GitHub 写之前
再复核一次，避免同步期间改仓库把产出写到旧仓库）；全部走
App 安装令牌。失败只记 `sync_error` 下轮重试，不改运行终态；成功后按开始
同步时的 `sync_epoch` 条件清 pending（同步期间的新 pending 不会被误清）。
中段写回（`code_status`/`visual_status`/`visual_screens_fingerprint`）同样带
`sync_epoch` 条件：同步期间总账被新 webhook/rerun 推进时不命中，直接放弃
本轮落库——陈旧快照的结论不得写回。汇总运行、视觉沿用判定、评论写回等
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
| `视觉审查` | 视觉运行启动 | `pending`「视觉审查中」 |
| | 有 blocker / worth fixing | `failure`「发现 N 条问题」 |
| | 只有 minor 或无发现 | `success`「审查通过」 |

代码审查 `error` 时不启动视觉审查，等手动重新审查；未配置视觉组合时永不
设 `视觉审查` 状态。

### 顶层评论（各一条，隐藏标记找回）

- 代码审查：标记 `<!-- fast-ship-code-review -->`（`FindPullRequestComment`
  找回 → `Update`/`Create`）。标题「代码审查结论：」，按组合分节、按
  P0→P1→P2→Note 列出、带 `文件:行号`；没有 P0/P1/P2 时写「本轮未发现
  P0/P1/P2 问题。」；将启动视觉审查时在复审说明之前加「视觉审查尚未结束，
  将复核截图。」；截图复核遗留时加「截图复核意见尚未处理」；末尾固定复审
  说明（推送后自动复审）。
- 视觉审查：标记 `<!-- fast-ship-visual-review -->`，标题「视觉审查意见：」，
  按严重度排序，每条带截图标签、维度、改法；不发行内评论。

### 行内 review（每个代码组合一个）

每个 `completed` 且 `posted_at` 为空的代码运行：有 P0–P2 且行号落在 diff
右侧有效行内时 `SubmitReview(event=COMMENT, body=summary, comments=[...])`
（`side=RIGHT`）；行号不在 diff 范围内的条目并入顶层评论，没有有效行内
条目不提交 review。**Note 不发行内评论。**提交后写 `review_id` 与
`posted_at`。

## 视觉审查衔接

代码审查全部终态后，以下五条**同时满足**才开视觉运行（每个视觉组合一行，
`kind=visual`，同 `round_number`），并把 `视觉审查` 置 pending「视觉审查中」：

1. 所有代码运行 `completed`（无 failed/timed_out/cancelled）且无 P0/P1/P2；
2. 配置了视觉组合（0 个则永不启动，也不设 `视觉审查` 状态）；
3. **至少一张截图下载成功**（下载面见下；描述里的截图引用数只是前置）；
4. 至少一个代码运行 `ui_changed=true`；
5. 截图集合有变化（URL 指纹 ≠ `visual_screens_fingerprint`），或任一代码
   运行 `ui_changed_since_last_visual=true`。

「其余判据都成立」包含条件 5：只有本轮真的要开视觉（截图指纹变了，或上轮
视觉后又有 UI 改动）才实际去**探测**下载——避免每次同步都拉图，也避免把
截图未变的沿用/带过路径误写成「截图不可用」。探测按描述顺序逐张试，第一张
成功即停（成功图已落盘缓存，视觉运行时直接命中），不写 `review_assets`
——资产行仍由视觉运行时的 `ensureAssets` 落账，可用性只看当前描述里的 URL，
不按资产行计数（已不在描述里的陈旧资产不算可用截图）。探测有 30s 总时限
（含同 URL 下载锁上的等待——等待吃 ctx，不会被前一请求的 45s 客户端超时
拖穿）：它跑在共享同步循环里，图床挂死时不能把其它 PR 的同步一起拖满。

探测结果是**三态**，按逐张失败的原因分类（`ErrUnsupportedUpstreamMedia`
内容不是位图、`ErrInvalidMediaURL` 地址形态不支持 →「不是图」；其余一切
——网络、超时、预算耗尽——→「拉不到」）：任一成功 → `ready`，照常开轮；
全部探完且全部「不是图」→ `none`，把 `HasScreenshots` 改判为 false，与「描
述里没有截图」同收敛（`visualNone`，不写任何视觉状态——svg 徽章这类引用
不会每个同步事件刷一条 failure）；其余（任一「拉不到」、或总预算耗尽未探
完）→ `unavailable`，reason 取第一条「拉不到」类原因，没有再取第一条失败
原因。

「拉不到」时**不建视觉运行**，`视觉审查` 写一条明确的 failure（「截图无法
下载，未做视觉审查：<原因>」，原因为中文可读文案，网络层英文报错只进日志，
且 URL 与错误文本先脱敏——`*url.Error` 内嵌重定向后的完整地址，预签名
query 不落日志），且**不写指纹**。这里的重试发生在**下一次同步事件**（新
推送产生新 SHA、或手动重跑；reopened 对同一 SHA 不会再次进入同步——webhook
把已有运行的事件一律忽略），系统不为「截图不可用」单独排定时重试；每次事
件都写当前原因，不沿用上一轮的旧描述。上一轮视觉审查有 blocker / worth
fixing 时，这个分支的代码审查结论同样加「截图复核意见尚未处理」（与沿用
failure 分支口径一致；上一轮通过或没有上一轮时不加）。

条件 5 不满足（截图没变）且上一轮视觉审查有 blocker / worth fixing：**不新
开视觉轮**，把上一轮的 failure 结论带到新 HEAD 的 `视觉审查` status，并在
代码审查结论里加「截图复核意见尚未处理」。条件 5 不满足且上一轮视觉通过：
沿用 success 到新 HEAD，status 描述区分两种情况——截图引用仍在且集合未变
写「审查通过（沿用上一轮，截图未变化）」；描述删光了截图或全部不是图
（`HasScreenshots=false`）时那句文案不再成立，改写通用的「审查通过（沿用
上一轮）」。

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
  `![a][id]` + 定义行都由 goldmark 填充同一个 Destination，label 取 alt 文
  本；没有定义的引用不产节点，自然不收集。Markdown 目的地不解码 HTML 实体
  （CommonMark 行为，`&amp;` 原样保留），与 HTML 来源口径不同，接受该边界。
- HTML `<img src>`（ast.RawHTML 行内段 / ast.HTMLBlock 整行块）：literal 交
  给 `golang.org/x/net/html` 解析取 src，实体由解析器解码（`&amp;` → `&`，
  修正旧正则原样收进 URL 的问题）。
- 裸链（ast.AutoLink，Linkify 产出）：URL 边界由 GFM 定义——ASCII 尾标点
  `?!.,:*_~` 与括号配对、结尾 `&xxx;` 实体由 Linkify 剥；URL 前面必须是空
  白/行首/`*`/`_`/`~`/`(` 才触发自动链接（cmark-gfm 同款，GitHub 渲染同款：
  紧贴直角引号的 `「https://…」` 在 GitHub 上就是纯文本，不收集）。
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
背书，在此之上额外按「GitHub 附件形态
（`github.com/user-attachments/assets/…` 与 `user-images.githubusercontent.com/…`，
拖入/粘贴附件的标准直链）∪ 位图扩展名」准入，否则描述里每个无扩展名的网页
链接都会被当成截图。这个启发式会误收正文里恰好以 `.png` 结尾的普通链接，接
受。
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
重定向目标与 DNS 重绑定的每次建连同样被拦。App 安装令牌（bearer）只发往
GitHub 媒体域、且只在匿名被 401/403/404 拒时重试一次（私有附件对匿名请求
正是回这两个码）：外部图床匿名失败即止，协议不支持、不是图片、超限、内网
地址这类确定性失败也不重试。指纹 = 排序后 URL 列表的
SHA-256 前 16 位。

## 工作区回收

运行进终态（完成、失败、超时、取消）后分两步：

- **归档**（`archiveRuns` 阶段）：所有 `dispatched_at` 非空的终态运行
  （代码与视觉都归档）逐个 `thread.archive`，成功写 `archived_at`；失败
  按进程内计数有限重试（3 个 tick），超限置 `archived_at` 防无限重试
  并发 `review_cleanup_failed` 通知，代码运行同时置
  `worktree_state=failed`。
- **删除**（`cleanupWorktrees` 阶段）：只处理已归档完成的代码运行，且
  删除前核验——行快照仍属本运行（repo/pr/head/thread/path 一致）、
  `worktree_path` 非空、且没有其他运行认领同一路径。任一不符一律
  `failed` + `worktree_error` + `review_cleanup_failed` 通知，
  **不删任何路径**。核验通过后
  `vcs.removeWorktree{cwd: 项目 workspace_root, path: worktree_path,
  force:true}`（T3 WS RPC，`git worktree remove --force`）。成功
  `removed`。不重试、不影响审查结论。
- worktree 路径只在 launch 确认后的有界窗口内解析：launch 前记录已知
  路径集合，launch 后 3 次 ×1.5s 内取 `vcs.listRefs` 差集，新增的唯一
  非空 `worktreePath` 即本次工作区并写库。**回收阶段不再差集猜路径**
  ——窗口内解析不到就保持空，回收时按「无法证明归属」failed + 通知，
  绝不误删。
- 视觉运行 `worktree_state` 恒 `none`，只归档线程、无工作区可删。

## 页面

- 侧边栏「审查」`/reviews`（`web/src/routes/reviews/index.tsx`）：当前用户
  所有已关联仓库（公开或代码仓库任一）的项目列表——项目名、仓库（实际审查
  的代码仓库，未单独配置即公开仓库）、开关、代码/视觉组合数、
  最近一次代码/视觉审查结论与时间；同仓库已被其他项目开启的行只读并显示
  占用者登录名。旧 `/automations` 路由直接删除，不做重定向。
- `/reviews/:projectId`（`web/src/routes/reviews/$projectId.tsx`）：单路由
  内两个分页（`?tab=settings|records`）——
  - **设置**：前置条件逐条 ✓/✗ 展示、开关、四个触发条件复选、代码审查组合
    1–5 行（必填）、视觉审查组合 0–3 行。组合行
    （`components/reviews/combination-row.tsx`）= 环境 `OptionSelect`
    （只列已通过 `project_t3_bindings` 绑定该项目的环境）+ `ModelPicker`
    + 每个选项描述符一个 `EffortChip`；切换环境清空该行下游选择。
    `can_manage=false` 或 `taken_by` 非空时整页只读。
  - **审查记录**：PR → 轮次 → `code_runs`/`visual_runs`（组合、环境、模型、
    状态、结论摘要、错误），每行可跳 `/threads/:env/:threadId`；每个 PR 行
    上「重新审查」按钮。

## HTTP API（JWT）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/reviews` | 审查总览：每项目一行（仓库 = 实际审查的代码仓库、开关、组合数、最近 code/visual status、`last_review_at`）；同仓库被占用时 `taken_by` 带占用者登录名且 `can_manage=false`。 |
| GET | `/api/projects/{id}/review-settings` | 设置 + `repo_full_name`（实际审查的代码仓库，原样大小写）+ `prerequisites[]`（缺项 `missing=true` + `detail`）+ `taken_by`。prerequisite 的 `github_app_installation` 每次实时调一次 GitHub。 |
| PUT | `/api/projects/{id}/review-settings` | 全量保存。组合结构/归属/绑定/数量/重复问题 40001（只读会话的环境同样拒绝）；启用时前置条件缺项 41202（逐条写进 message）；同仓库占用 40922（带占用者）。`enabled=false` 跳过前置校验、允许零组合。 |
| GET | `/api/projects/{id}/review-records` | 审查记录：`items[]` 每个 PR 一行，内嵌 `rounds[]`（轮次、head、时间）与每轮 `code_runs[]`/`visual_runs[]`。`?limit=` 默认 20、上限 100。 |
| POST | `/api/projects/{id}/reviews/rerun` | 重新审查：body `{pr_number}`。项目必须 enabled（否则 40923）。rerun 没有 webhook installation 快照：先 `ResolveInstallation` 解析真实安装 id（解析不支持/失败直接报错，不拿 0 继续），再用安装令牌 `GetPullRequest` 实时取当前 head，走与 webhook 相同的「取消旧 SHA + 开轮」事务路径（`trigger_action=manual`，恒开新轮），安装 id 落进运行与总账。返回 `{accepted, head_sha, round_number}`。 |

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

**同仓库唯一**：整个部署内同一**代码仓库**（`repo_full_name` 快照，大小写
不敏感）只允许一个项目 `enabled=true`；其他用户的同仓库项目在 `GET`/`PUT`
里得到 `taken_by`（占用者登录名），开启返回 40922。占用判定只读 enabled
设置行的快照列（联 `users` 取登录名），不回联项目的 live 仓库字段。写入
路径双保险：保存事务内先查占用者，部分唯一索引
`idx_project_review_settings_repo` 兜并发竞态。

**保存并发复核**：`PUT` 写入事务内会重读项目的审查仓库（`CodeRepo()`，
含 code 两列），与事务外校验用的项目快照不一致 → 400「项目仓库已变更，请
刷新后重试」（并发改仓库时把 enabled 落到旧快照上会写错唯一占用键与
账本分区键）。项目侧改**代码仓库**走 `ProjectRepository.Update`——同事务
停用审查并刷新 `repo_full_name` 快照。

**接管**：某项目成功 `enabled=true` 保存时，同一事务内对同仓库其他项目
做收尾（唯一占用保证此时没有其他 enabled 项目，残留来自曾开启后停用或
换仓库的项目）：

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
为三类审查系统故障：`review_dispatch_failed`、`review_run_timeout`、
`review_cleanup_failed`（`model/notification.go`）。审查发现不发通知。
原 `automation_*` 四类与 `agent` kind、`automation_busy` 随通用自动化删除。

## 已知限制

- **单进程**：调度循环、per-PR 锁、防抖消费都在同一进程内；多副本部署会
  重复派发与重复写状态（唯一索引只保证运行行唯一）。
- **fork PR 不审**：head repo ≠ base repo 一律忽略，fork 仓库的 PR 永远
  不会进入审查。
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
- **本地分支残留**：`git worktree remove` 不删本地分支，T3 也没有删分支的
  RPC——每次代码审查残留一个本地分支引用，由 `t3-worktree-gc` 脚本兜底清理。
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
- **改代码仓库 URL 会自动停用审查**：项目更新**代码仓库**地址（未单独配置
  代码仓库时，改公开仓库等同改代码仓库）时，同一个事务里把
  `project_review_settings` 置 `enabled=false` 并把 `repo_full_name` 快照改成
  新审查仓库——避免旧仓库继续占着唯一索引、新仓库与别的已启用项目重复。
  代码仓库已显式配置时只改公开仓库不影响审查。用户需要在
  新仓库上重新开启（前置条件都按新仓库重算）。
