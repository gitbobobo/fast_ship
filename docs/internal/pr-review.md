# GitHub PR 自动评审（INT-93）

在 INT-90 自动化之上：GitHub App webhook（pull_request opened/synchronize）触发
自动 PR 评审。一条规则可选 1–10 个模型，一次触发每个模型并行开一个 T3 线程，
结果通过机器人顶层评论 + GitHub Checks 回写到 PR。评论/check 全部以 GitHub App
安装令牌代发，agent 线程拿不到任何 GitHub 凭证。

**本次（INT-93）不创建真实 GitHub App、不部署**；自测走
`github_app.fake=true`（仅 `server.mode=debug`）+ `scripts/simulate-github-pr-webhook.sh`。

## 触发与范围

- 触发：`github_pr_opened`、`github_pr_synchronize`（`X-GitHub-Event: pull_request`
  且 action 匹配）。不处理 draft / fork（`head.repo.fork` 或 head ≠ base 仓库）；
  不处理 ready_for_review / reopened / closed。draft 打开后标可审的 PR，等到
  下一次非 draft、非 fork 的 synchronize。
- 仓库范围：GitHub App 安装到的仓库中，`projects.github_owner/github_repo`
  已关联（大小写不敏感反查）且该项目绑定了公开或代码仓库 Token 的才审查。
  未关联、或只填仓库名未绑 Token → 202 忽略，不取消、不开轮。webhook 事务内
  不调 GitHub，不现场验证该 Token 对该仓的权限。
- 规则匹配：`trigger_type` 必须与 action 一致（opened 规则不响应 synchronize，
  反之亦然）；`issue_project_id` 非空则必须匹配该项目，为空则该用户任一关联项目
  命中即可；同一 automation id 只开一轮。

## 防抖与排队（`pr_review.go`）

- **防抖**：规则可配 `debounce_seconds`（默认 60，0=立即，上限 86400）。
  窗口内同一 (规则, 仓库, PR) 的新推送只更新 `github_pr_debounces` 一行的
  head SHA 并重置 `fire_at=now+debounce`；到点由调度循环开轮，只对最新 SHA。
  debounce=0 时在 webhook 请求事务内直接开轮。
- **排队**：每用户 PR 触发同时最多 5 个在途运行
  （`prReviewMaxInflightPerUser=5`，常量；在途 = `''`/dispatched/running，
  queued 不占名额）。超出按 `triggered_at` 排队在 `queued` 状态，调度提升时
  `deadline_at` 重置为提升时刻 + timeout（超时从真正开始派发算）。
  开轮事务会就地提升；超时/结束/派发失败释放名额后，由调度循环
  `PromoteQueuedPRRuns` 再提升（结束回报与 failDispatch 会 kick）。
- **SHA 变化**：新 head SHA 使该 PR 下旧 SHA 的全部在途运行（排队+进行中，
  跨规则跨用户跨模型）一律 `cancelled`（`error_code='superseded'`），令牌消费、
  名额立即释放，并删除该 PR 下 `head_sha <> 新 SHA` 的防抖行（opened 窗口
  不能对过期提交开火）。取消前先判过期 webhook：当前 head 优先取未开火的
  防抖行，否则取评审账；已写入 `github_pr_seen_shas` 但不是当前 head 的 SHA
  一律 202 忽略——A 已开轮、B 还在 debounce 时，迟到的 `opened(A)` /
  `synchronize(A)` 也不能删掉 B 的 debounce。force-push 回已见过的 SHA 不再
  开轮。payload 的 `pull_request.updated_at` 早于或等于已受理 head 的，即使
  该 SHA 从未写入 seen/运行也拒绝——GitHub 先投递 `synchronize(B)` 再迟到
  同一秒的 `synchronize(A)` 时不能取消 B。webhook 在开事务前拿 per-PR 锁，与 agent
  代发和超时终态串行。曾 dispatched/running
  的由事务外同步调 `T3ThreadsService.StopThread`（`run.interrupt`，`holdQueue=true`）
  中断线程，每运行只尝试一次（`stop_attempted_at` 标记）；queued / 未认领不调。
  `launchThreadForRun` 仅在运行仍是 dispatched/`''` 时签发令牌；CreateThread
  返回后（含 RPC 已发出未确认）重读状态，已取消则消费令牌并立刻中断。

## 轮次与幂等

- `round_number`：同一 `repo+pr+head SHA` 共享一轮（多规则并行开轮不各自
  `NextRound`）；该 SHA 尚无运行时才取
  `1 + max(该 repo+pr 在 runs 与 github_pr_reviews 的轮次)`。零插入（唯一键
  吃掉重复 SHA+模型）不推进 `github_pr_reviews.round_number`。轮次号跨 SHA
  单调递增。
- `trigger_key = pr:<PathEscape(repo)>:<number>:<sha>:<PathEscape(instance_id)>:<PathEscape(model)>`
  —— 同一 SHA+模型被 `UNIQUE(automation_id, trigger_key)` 吃掉。
- webhook 以 `X-GitHub-Delivery` 幂等：`github_webhook_deliveries` PK 冲突 →
  202 且不再开轮；delivery 行保留 7 天由调度循环清理。
- 每个模型的运行 `config_snapshot.model_selection` 是**这一个模型**的单对象，
  现有 launch 路径不用改；`model_selections_json` 存规则的多模型数组。
- webhook 事务内 GitHub/T3 调用数为 0（测试断言）；提交后向调度循环发缓冲 1
  的 kick（循环仍是唯一执行者）。

## 评论与 check 同步（`pr_review_sync.go`）

每个 PR 一把进程内锁，`github_pr_reviews.sync_pending` 置位后由调度循环在
事务外推送；失败只记 `sync_error` 下轮重试，不改运行终态。超时、派发失败、
agent finished、取消都会在原事务末尾置位。`ClearPendingSync` /
`SetSyncError` 按开始同步时的 `sync_epoch` 条件写（置 pending 时 +1），
同步期间的新 pending 不会被清掉。

- **评论**：每 PR 一条机器人顶层评论，带隐藏标记 `<!-- fast-ship-pr-review -->`。
  已保存 `comment_id` 则直接 PATCH；404 或未保存再按标记找回，否则创建。
  只展示当前 Head SHA 的运行。表格列：模型、Harness（instance_id）、状态、
  Head SHA、轮次。
- **check**：每个模型一个 check run，名 `Fast Ship review (<model>)`，
  `external_id`=run id；`check_run_id` 用 `WHERE check_run_id IS NULL` 写回。
  Checks API 返回 403 时退回整轮一个 `Fast Ship review` 合并 check
  （全部运行共享同一 id，状态取聚合）。合并 check 同样 403（App 没有 Checks
  写权限）时跳过 check，仍推/更新机器人评论。

| 运行状态 | 评论格 | check |
| --- | --- | --- |
| queued | 排队中 | queued |
| '' / dispatched / running | 进行中 | in_progress |
| completed | 完成 | completed/success |
| failed / timed_out | 失败 | completed/failure |
| cancelled | 已取消 | completed/cancelled |

## Agent 代发端点（运行令牌，`automation_agent_pr.go`）

owner/repo/PR 号/SHA/check id 全部取自运行行，请求不接受这些输入；运行必须是
PR 触发且 `dispatched|running`，结束/取消后令牌已消费，访问一律 40106；
`fsk_` API Key 一律 40304。

- `GET /api/automation-agent/pull-request` —— PR 元数据
- `GET /api/automation-agent/pull-request/diff` —— diff，>1MB 截断并标 `truncated`
- `GET /api/automation-agent/pull-request/comments` —— review 评论列表（GitHub 分页，每页 100，最多 10 页）
- `POST /api/automation-agent/pull-request/review` —— COMMENT/APPROVE/REQUEST_CHANGES，
  commit_id 固定本运行 head SHA。与 webhook 取消、调度超时共用 per-PR 锁
  （先锁再开事务），锁内重读运行行，已取消/结束/超时则 40920 且不提交（避免
  superseded / timed_out SHA 留下 blocking REQUEST_CHANGES）
- `POST /api/automation-agent/pull-request/check` —— 只更新本运行 check 的
  title/summary/text；结论由运行终态决定。同样在与 webhook 取消共用的
  per-PR 锁内重读运行行，已取消/结束则 40920 且不 PATCH；若 PATCH 之后才变成
  终态，置 `sync_pending` 让同步覆盖 `in_progress`。

提示词额外替换 `{{repo}}` `{{pr_number}}` `{{pr_title}}` `{{pr_url}}` `{{head_sha}}`
并附五个端点说明；工作区仍用规则上的 root/worktree，不为 PR 单独 clone。

## 配置与安全

- `github_app.app_id`（`FAST_SHIP_GITHUB_APP_APP_ID`）
- `github_app.private_key_path`（`FAST_SHIP_GITHUB_APP_PRIVATE_KEY_PATH`）——
  PEM 文件路径；私钥不进库、不进环境变量正文、不写日志
- `github_app.webhook_secret`（`FAST_SHIP_GITHUB_APP_WEBHOOK_SECRET`）——
  为空时 `POST /api/github/webhooks` 一律 503，不接受未签名请求
- `github_app.fake`（`FAST_SHIP_GITHUB_APP_FAKE`）—— 仅 `server.mode=debug`
  允许 true；注入内存假实现，入站 webhook 会 `SeedFakeFromWebhook`（simulate
  脚本不必另调 SetPR），`GET /api/debug/github-review-log`（JWT）可看调用记录

适配器在 `server/internal/pkg/githubapp/`：`webhook.go`（HMAC-SHA256 常量时间
比较，`sha1=` 前缀拒绝）、`app.go`（App JWT RS256 约 9 分钟 → 安装令牌，按
installation_id 进程内缓存，401 作废重试一次）、`client.go`（PR 元数据/diff、
issue 评论、review 评论、check run、review 提交）、`fake.go`。不借用
`pkg/github` 的 PAT 客户端，不读 `user_github_tokens`。

## 索引

- `idx_automation_runs_active_once` 收窄为仅 cron/issue 触发（**必须 DROP 再
  CREATE**，`CREATE IF NOT EXISTS` 不会替换已有定义）——PR 触发允许同一规则
  多行未结束（一轮多模型 + 排队）；
- `idx_automation_runs_pr_head(automation_runs.repo_full_name, pr_number, head_sha)`；
- `idx_projects_github_repo(projects.github_owner, github_repo)`；
- `github_pr_seen_shas(repo_full_name, pr_number, head_sha)` PK——已受理 SHA。

## 已知限制

- 单进程：per-PR 锁与防抖消费都是进程内/单 DB 条件更新，多副本部署不适用。
- cron/issue 规则的「同一自动化至多一个活动运行」语义不变；PR 规则不受
  40919 删除守卫影响（删除守卫 `HasActiveRunTx` 保持原语义，排队中的运行
  finished_at 为空仍会拦删除）。
- 合并 check 与按模型 check 可能混存（首个模型 403 前已建的不回收）。
- 令牌重放窗口内（deadline+5min）结束回报可重放，PR 代发端点不行（requireLive）。
