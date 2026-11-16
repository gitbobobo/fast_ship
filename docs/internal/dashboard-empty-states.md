# 仪表盘与图表空状态

`/dashboard` 与 bot 用量页共用 `web/src/components/dashboard/metric-chart.tsx`。这里的约定是 INT-151 定下的：空数据时不要把「没有数据」画成一张有刻度的图，也不要用看起来可点、实际不可点的元素。

## 数据形状

`GET /api/dashboard/overview`（`server/internal/service/dashboard.go`）按登录用户聚合：

- `open_issues_by_project`：该用户的**全部项目**（`repository/dashboard.go` 从 `projects` 出发 `LEFT JOIN issues`），没有开启问题的项目计数为 `0`，后端已按 `open_issue_count` 降序排好。所以**空数组 ⟺ 没有项目**，有项目时最低也会返回 `0` 行。
- `daily_resolved`：固定 30 条（含 0 值日期），按 `issue_internal_meta.completed_at` 分日聚合，**不再按项目拆分**（无 `projects` 字段）。近 30 天没有完成记录时 30 条全是 0；更早完成的问题不计入这个窗口。
- `action_item_groups`：「待我处理」聚合区，固定顺序返回，前端按序渲染。每组 `{kind, count, complete?, items[]}`，`items` 最多 5 条、各带站内 `href`。`complete=false` 表示该组数据不完整：`t3_waiting` 任一环境未 ready（无 T3 环境时该组整组缺席），或其它组查询失败降级（显示「暂不可用」而不是静默清零假扮「一切正常」）。各组口径：`review_failed` 只算近 14 天、`review_pull_requests` 当前轮次+当前 head 的 failed/timed_out 运行，按 PR 去重（同轮次多 run 不重复计数）；`ship_failed` 含 pending 且 `ship_status=failed`（发货失败）与 `ship_hooks_status ∈ {failed, incomplete}`（已发货钩子失败/未齐，会在下次发货重试并自行消失）；`recommendations` 排除 `deferred_at`；`quota_attention` 只认 `credential_invalid`/`provider_error`，与额度页「需处理」横幅同口径。
- `flow`：流转效率块（INT-235 阶段二），带可省略 `complete`（仅 false 返回）。`todo_count`/`in_progress_count` 按 workflow_status 分桶（NULL/空串/显式 todo 归待办，其余非 done 归进行中），分母判定与 `open_issues_by_project` 完全一致，**恒等式 todo+in_progress == Σopen_issue_count**；刻意独立成查询（概览那两个查询失败即 500，新指标必须能单独降级）。`daily_created` 与 `daily_resolved` 共用同一 UTC 窗口（固定 30 条），前端把两条序列按 date 对齐画堆叠面积图；**只统计 `source='internal'`**——GitHub 同步 issue 关闭时不写 completed_at，混算会把积压趋势系统性画成增长（裁决采纳）。`cycle_time_by_project` ≤5 条：窗口内 completed 且有 started_at 的样本、零时长行剔除、中位数 Go 内算、按 completed_count 降序+项目名升序取前 5，**恒等式 Σcompleted_count ≤ Σdaily_resolved**；连接方向 projects→issues→meta 主键，禁止从 `issue_internal_meta` 出发全表扫（所有新查询同此红线）。`backlog_age` 三档与待办分桶同口径，**恒等式三档之和 == todo+in_progress == Σopen_issue_count**；分档边界在 Go 算好 time 参数传 SQL（时间是文本存储，不用 julianday）。任一子查询失败整块 `complete=false`、计数清零但结构完整。
- `ai`：AI 运行面板（阶段三），子块独立降级（顶层无 complete 字段，panel P6 删）：
  - `t3`：每环境一行 `{running, waiting, queued, readable}`，与 `t3_waiting` 组**共用同一次 WaitingThreads 遍历**（绝不调第二次 PeekActive）；`queued` 取原始 shell `status==queued` 且运行相位在 running 的线程——该状态在 `t3ThreadStatus` 兜底分支被折进 running，现有 PeekActive 路径即可分辨，无需动跨端契约（裁决：做，不缺席）；`pendingRuntimeRequest` 优先落 waiting 的线程不算 queued。无 T3 环境时子块缺席——这是「无 T3 环境」的信号：前端据此渲染「未配置 T3 环境」空态 + /threads 入口，区块本身不消失（视觉 round1 V1）。`complete` 仅降级返回 false，成功时不携带（panel P9，与 reviews/bots/quota 子块同形）。前端降级**不整块短路**（panel P3）：可读环境的行照常展示，不可读行标「本轮不可读」；有环境行且降级时区块头加琥珀色「部分环境暂不可用」徽章；只有 environments 为空且降级才显示「暂不可用」。行文案「运行 N（含排队 K）· 等待 M」（K>0；queued 已含在 running 内，并排易被读成并集），K=0 维持「运行 N · 等待 M」（panel P13）。
  - `reviews`：PR 级四分桶（近 30 天、排除搁置，复用 `review_runs⨝review_pull_requests` 全键连接）：`code_status=success→passed / failure→blocked / error→errored`，pending 或仍有在途 run（`ReviewRunActiveStatuses`）→`in_flight`（在途优先）。**completed ≠ 通过**：run 终态 completed 但 `code_status=failure` 的 PR 归 blocked。`pass_rate=passed/(passed+blocked)`，**分母 0 → null（前端不显示，不代表 100%）**。窗口边缘情况：聚合从 `review_runs` 出发（`r.user_id` 与 `p.user_id` 双圈定），当前轮次尚无任何 run 行的 pending PR 不会入四桶（在途少计的已知代价；从 `review_pull_requests` 反向 LEFT JOIN 翻转留作独立建议，本轮不改）。
  - `bots`：经 `GetBotUsageOverview(userID,"7d","")` 窄接口取数（今天/脏日回退实时扫描），**绝不直读 bot_usage_daily**；`total_tokens=prompt+completion`（reasoning ⊆ completion 已含其中不重复计，panel P1；供应商未返回按 0）由共享累加器 `botUsageBotAcc` 合计。`cost=null` 是缺失不是 0（前端显示「未计价」）。时区是用量统计的生效时区；前端标题「机器人（近 7 天）」，有数据时区块底部脚注「按你的时区（tz）当地日聚合」，未配置/降级不显示任何窗口说明（视觉 round1 V6）。`next_schedule` 走 `bot_schedule_tasks ⨝ bots` 新查询（b.user_id 圈定、enabled、next_run_at 最早一条）：两表小且主键连接，**无额外索引可接受**（本注记即记录）。服务错误（含 ErrInvalidParams/ErrBotNotFound）一律吞掉降级；窄接口 nil 时子块缺席。`bot_count` 口径 = 已配置机器人数 + 窗口内有记录的已删除机器人（与展示数字的参与者集合一致，panel P5）。
  - `quota`：`QuotaService.List(userID)` 只读快照（不访问网络）。**后端不算剩余百分比**——前端用 `lib/quota.ts` 的 `accountRemaining`（100−max(used_percent)，默认池）推导，配色用 `quotaUsageTone`；快照可能落后，非 ok 状态与徽章一起展示。
- `release`：发布节奏块（阶段三），子查询任一失败整块降级。`pending_versions`（pending 按 created_at 升序截前 3，count 为全量）；`recent_releases`（**不在 SQL 侧排序/截断**：shipped_at 是文本存储，历史行有本地时区写法，字典序与真实时间序错位会把最新发版排丢——候选全取（仍不加时间窗口，表按版本量级极小），Go 侧按 RFC3339 及带偏移/空格分隔写法解析后按真实时间倒序取前 5，解析失败的行垫底不丢弃（panel P2）；ship.go 的发货时间已改写 `.UTC()`（残留清理，新写入恒为 UTC，存量行不作迁移））；`unmerged_pr_issue_count`：开启且至少关联一个非 merged PR（含 closed 未合并）的 issue 去重数、排除搁置，从 projects 出发连接。「距上次发版 N 天」徽章由前端从 `recent_releases[0].shipped_at` 推导，且带 `complete !== false` 门禁（降级载荷的列表是清零占位，panel P4）。

## 「待我处理」区

- 位于 Hero/错误条之下、KPI 之上（INT-235 阶段二起 KPI 上移到它前面：左列顺序为 KPI → 待我处理 → 流转效率 → 图表；W8 把流转效率上移到图表之前——它属原需求「左侧待处理与趋势」的趋势段，1440×900 首屏至少入镜卡头），同样受 `unavailable = isLoading || isError` 约束：**加载时渲染骨架屏，失败时整块不渲染**——不能显示缓存里的旧数字，也不能在失败时显示「一切正常」。
- Hero 摘要句「近 30 天解决 N 个问题，M 件事等你处理」：N=`daily_resolved` 30 天合计，与「近 30 天已解决」KPI 同源（W7，不再另立尾 7 天窗口）；「今天」是 UTC 日不能用于东八区早上，所以问候语只在无数据可说（unavailable）时兜底；M=待处理组 count 合计，任一组 `complete=false` 时显示 **M+**（真实值只会更多，不假装精确）。
- 布局 xl 断点两栏：左列 KPI+待我处理+流转效率卡+柱图+面积图（W8），右列 AI 面板卡+发布节奏卡；窄屏单列按此顺序。两列都是普通文档流（`items-start`），不强行等高。
- 只列出 `count > 0` 或 `complete === false` 的组；全部组为零且无 incomplete 时整卡折叠成一句「一切正常」。
- `complete === false` 时组标不看 count：`count > 0` 显示「N+ · 部分环境暂不可用」，`count = 0` 显示「暂不可用」。N+ 表示后端只给了部分环境的读数，真实值只会更多。
- 每条 item 是一个站内 `Link`（`item.href`），title 主行、detail 次行；不要自己拼 URL。

## 空状态约定

- 图表判定为空时**不渲染坐标轴、网格、曲线与点**，只渲染一处居中的次要文字（`ChartEmptyState`）。空状态不叠在图上，也不伪造数据点。
- 空状态可以带一个真实入口 `emptyAction`，由页面传入；入口用 `Button render={<Link/>}`（如「去创建项目」→ `/projects`），不要用带边框的虚线胶囊伪装成按钮。
- 柱状图只在 `points.length === 0` 时算空：有项目但开启问题都清零时保留项目行和 `0`，读者仍能看到项目本身。值为 0 的行只画空轨道，不画填充段（短灰条会被误读成「有少量进度」）。
- 柱状图前端只显示 Top 10（`maxRows`），其余折叠为一行不可点的「其他 N 个项目共 M 条」（`collapsedLabel` 由页面生成）。数据行带 `href` 时整行渲染为链接——开放问题图跳 `/issues?project=<id>&state=open`；折叠行没有对应的真实入口，不可点。
- 行跳转口径：柱图行值是 `open_issue_count`（不含 `workflow=done` 的问题），而 `/issues?state=open` 列表的过滤口径可能略有出入，数字对不上是预期内的。
- 柱图副标题显示「N 个项目有开启问题」（`open_issue_count > 0` 的项目数），不再是泛泛的描述文案。
- 面积图在 `points.length === 0` 或窗口内最大值 `0` 时算空。图表转为空状态时不要渲染 tooltip：数据可能在悬浮过程中被刷新成全零，`hoveredIndex` 仍在，浮层会留在空状态上。
- 面积图只画总量：后端不再回传逐项目拆分，页面把当日总数作为单一系列（id `total`）传入 `MetricChartSeriesPoint`。series 堆叠渲染路径与 `MetricChartLegend` 仍被 bot 用量页（`variant="columns"`）使用，不要删。
- 概览请求失败（`isError`）时不要把「没有数据」当成「没有项目」：页面顶部显示可重试的错误条，图表位置只显示「数据加载失败」，不显示空状态文案与「去创建项目」入口，KPI 显示 `—`。
- 失败时可能仍有上一次成功的 `data`（TanStack Query 在 refetch 失败时保留缓存，`isLoading` 为 false、`isError` 为 true）。所有数字与趋势展示必须用同一个条件 `unavailable = isLoading || isError`：卡片的「总计 N」、右上角「日均 x.x」、KPI 数值与色调、「待我处理」区一起隐藏或显示 `—`，不能只盖住其中几处。
- KPI 三卡中仅「已配置项目」可点（`Link` → `/projects`），其余两张不带链接；不要给看起来可点的元素留假入口。
- 卡片右上角只显示「日均 x.x」读数，**不显示涨跌方向图标**；近 30 天没有已解决数据时整段隐藏，避免在空状态下暗示「在变差」。旧的底部统计条（最高日解决/日均解决/活跃项目/问题总量）已随 INT-235 删除，不要恢复。
- 图表卡标题「每日已解决」+ 副标题「近 30 天 · UTC 日」，与 KPI 卡标题「近 30 天已解决」区分（视觉 round1 V7）：两张卡同名会让 Hero 的「近 7 天解决 N 个」读起来像同一口径。
- KPI 卡 tone 色（amber/emerald）只上数值与图标；标签行固定中性色 `text-gray-700 dark:text-gray-300`，不要继承 tone 色再加 `opacity-70`——浅色底上对比度只有 ~2.7–3.0:1，不达标（视觉 round1 V4）。
- 不要恢复旧的「日均是否达到最大值一半」判断（1 条/30 天会被标成下降），也不要用「最近 7 天 vs 前 7 天」做方向判断：序列最后一格是尚未结束的 UTC 当天，最近窗口天然少一天，平稳吞吐也会被判成下降。真要做周期对比，得先用两个**已结束**的窗口，并补上对应文案。
- 日均大于 0 且小于 `0.1` 时显示 `<0.1`；日均等于 0（窗口内没有已解决记录）时仍显示 `0.0`。直接 `toFixed(1)` 会把 1 条/30 天渲染成 `0.0`，看起来像没有数据。
- 「近 30 天已解决」KPI 卡带 7 天 sparkline（`daily_resolved` 尾 7 天）：最后一格是尚未结束的 UTC 当天，**仅展示不判断方向**；全零不画（避免噪音）。其它 KPI 卡不带迷你趋势。svg 用比例 viewBox（宽 100）+ `preserveAspectRatio="none"` 横向撑满卡片内容宽度，线下铺同色淡面积（fill currentColor opacity 0.12）表达趋势带，描边 `non-scaling-stroke` 防拉伸变粗，不画点（非等比拉伸会把圆点压变形）（视觉 round1 V3）。
- 流转效率/AI/发布节奏三张新卡全部受 `unavailable` 约束：加载/失败时不渲染（不是显示旧值）；块级 `complete=false` 时显示「暂不可用」，且**零值徽章/图表/分布条一并不渲染**——降级返回里的 0 是清零占位，不是真实读数。
- 发布节奏卡三块（待发版/近期发版/未合并 PR）统一为 AI 卡的「图标 + 标签 + 值/空态」层级，容器 divide-y（视觉 round1 V2）；`pending_count > 0` 时显示 count 徽章 + 列表，0 时收成单行「待发版 + 右侧 muted『暂无』」（W9），不写「没有待发版」自问自答，也不写「待发版 0」（视觉 round1 V5）。
- 流转效率卡的「新建 vs 已解决」是堆叠面积图（两条序列按 date 对齐），纯展示无方向判断，不恢复任何涨跌图标。图上方用 `MetricChartLegend` 标注两条序列（与 bot 用量页共用组件，W1），图例顺序与副标题「新建与已解决」一致、色点与两条堆叠序列同色；图表转空状态（全零）或降级时图例一并不渲染，单序列的「每日已解决」图不用图例。积压年龄分布条三段（7 天内/7-30 天/30 天以上）宽度即占比，配色 emerald/amber/red 500 系，下方标签前的小圆点与条段逐档同色、无值档也保留作图例（W4）；总开启数为 0 时显示文案不画条。完成周期显示自适应单位（W3）：<1 小时「<1 小时」、不足一天「N 小时」（四舍五入到小时）、≥1 天「N 天」一位小数，样本计数带名词「N 个问题」。
- 流转效率卡右上角「待办 N · 进行中 M」是纯文本元信息（`text-[11px] text-muted-foreground`，W5），与「日均 x.x」同款，不用药丸底色。
- AI 面板与发布节奏卡的区块值/空态行统一 `pl-6`（24px = 图标 16px + gap 8px）与标签文字共用左基线（W2），`SectionUnavailable` 也带同款缩进。
- 行内空态入口的标准写法（W6）：`SectionEmptyLink`——text-primary + 尾部箭头「xxx →」+ hover 变色（如 T3 空态的「查看线程 →」），不要用灰字伪装成不可点的文字。
- AI 面板：审查通过率只在分母非零时显示（`pass_rate=null` 不渲染）；机器人 `cost=null` 显示「未计价」不渲染 `$0.00`；`bot_count=0` 显示「未配置机器人」；额度迷你条画已用方向、剩余百分比用 `accountRemaining` 推导，非 ok 状态配状态徽章（快照可能是旧数据）。T3 区块降级不整块短路：健康行仍在、不可读行标「本轮不可读」、区块头带琥珀色「部分环境暂不可用」徽章（panel P3）；发布节奏卡「距上次发版」徽章带 `complete !== false` 门禁（panel P4）。

## 纵轴刻度

取整阶梯：最大值不超过 2 时只分两档（`tickIntervals = 2`），其余分四档；`tickStep = max(ceil(maxTotal / tickIntervals), 1)`、`chartMax = tickStep * tickIntervals`。于是最大值为 1/2 时刻度是 `0/1/2`（峰值至少占图高一半），最大值 5/7 时是 `0/2/4/6/8`，最大值 1000 时是 `0/250/500/750/1000`。直接用 `max(maxTotal, 1)` 会在最大值很小时产生 `1、1、1、0、0` 这种重复刻度，也会让峰值只占四分之一高。刻度文字用 `text-[11px]` 与 `fill-muted-foreground`。

横轴日期从**最后一个点**按固定步长往前取（`stride = max(ceil(last / 6), 1)`），相邻标签间隔恒定，最后一个日期永远在最后一个点正下方；首点因此可能没有标签（30 个点时是索引 4/9/14/19/24/29），不要为了补首点而在中间插一个不均匀的间隔，也不要按 `round(last * i / (count - 1))` 均分（30 个点会得到 5、5、5、4、5、5 的不等间隔）。这是按点数的近似，不保证极窄容器（约 300px 以下）里 6 个 `MM-DD` 都不重叠；真要在窄屏用，再根据实测宽度减少刻度数。

图表的坐标系用**实测宽度**（`ResizeObserver` 取容器宽度，`viewBox` 与该宽度一致，1:1 像素）。不要改回固定 `720x220` 的 viewBox：那会把整个图（包括坐标轴文字）等比缩小，纵轴数字实际只剩 ~8.7px，横轴日期与数据点也会错位。横轴日期直接画在 SVG 内、和数据点共用同一套 x 坐标，所以「最后一个日期」永远落在最后一个点正下方。面积/折线变体的末点 x 额外内收 6px（`AREA_END_INSET`，W10），与绘图区右边框脱开；columns 变体按槽位居中、本就内收，坐标计算零变化（bot 用量页不受影响）。

## 测试

- `web/src/components/dashboard/metric-chart.test.tsx`：空状态、全零项目行（含不画填充段）、带 `href` 的行渲染为链接、`maxRows`+`collapsedLabel` 折叠行不可点、窗口全零、最大值 1/2 与 5 时的刻度阶梯。
- `web/src/routes/__tests__/dashboard-page.test.tsx`：「待我处理」区渲染组名/count/items 链接、「一切正常」空态、`complete === false` 降级（「N+ · 部分环境暂不可用」/「暂不可用」）、错误态不渲染该区；柱图 Top 10 折叠与行 href 指向 `/issues?project=<id>&state=open`；「已配置项目」KPI 链接 `/projects`；空数据时每张图表卡片只有一个空状态、无伪造项目行、入口链接指向 `/projects`、不渲染趋势图；日均 `<0.1` 的显示且不再有方向图标；首次失败与「缓存后 refetch 失败」两种错误态都隐藏旧数字（`unavailable` 条件）。INT-235 阶段二/三页面编排：Hero 摘要句（N/M/M+ 与 unavailable 回退）、三张新卡加载/失败时不渲染、降级块经页面透传「暂不可用」。卡片本体断言在组件级测试（panel P8 拆分）：- `web/src/components/dashboard/flow-card.test.tsx`（徽章/周期列表/积压条/降级零值不渲染；W1 双序列图例出现与空态隐藏、W3 周期单位小时/天两档与边界、W4 三档配色点条内与标签对应、W5 纯文本「待办 N · 进行中 M」）`ai-panel-card.test.tsx`（每环境行含「运行 N（含排队 K）」/不可读/部分可读健康行仍在/区块降级徽章/暂不可用边界、四分桶+通过率 75%、pass_rate null 不显示、机器人 token/未计价/未配置、额度条 60%+凭据失效徽章）、`release-card.test.tsx`（待发版链接/时间线/未合并计数/距上次发版门禁）。
- 后端恒等式与降级：`server/internal/service/dashboard_test.go`（todo+in_progress==Σopen、三档年龄==Σopen、Σcycle≤Σresolved、中位数边界、各块降级不 500、t3 单遍历共用）；`server/internal/repository/dashboard_test.go`（分桶判定、internal 过滤、样本窗口、next_schedule 语义）。
- 网格：`MetricChart` 的 tooltip 在图表转为空状态时必须消失（悬浮中数据可能被刷新成全零）。
