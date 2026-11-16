# 仪表盘与图表空状态

`/dashboard` 与 bot 用量页共用 `web/src/components/dashboard/metric-chart.tsx`。这里的约定是 INT-151 定下的：空数据时不要把「没有数据」画成一张有刻度的图，也不要用看起来可点、实际不可点的元素。

## 数据形状

`GET /api/dashboard/overview`（`server/internal/service/dashboard.go`）按登录用户聚合：

- `open_issues_by_project`：该用户的**全部项目**（`repository/dashboard.go` 从 `projects` 出发 `LEFT JOIN issues`），没有开启问题的项目计数为 `0`，后端已按 `open_issue_count` 降序排好。所以**空数组 ⟺ 没有项目**，有项目时最低也会返回 `0` 行。
- `daily_resolved`：固定 30 条（含 0 值日期），按 `issue_internal_meta.completed_at` 分日聚合，**不再按项目拆分**（无 `projects` 字段）。近 30 天没有完成记录时 30 条全是 0；更早完成的问题不计入这个窗口。
- `action_item_groups`：「待我处理」聚合区，固定顺序返回，前端按序渲染。每组 `{kind, count, complete?, items[]}`，`items` 最多 5 条、各带站内 `href`。`complete=false` 表示该组数据不完整：`t3_waiting` 任一环境未 ready（无 T3 环境时该组整组缺席），或其它组查询失败降级（显示「暂不可用」而不是静默清零假扮「一切正常」）。各组口径：`review_failed` 只算近 14 天、`review_pull_requests` 当前轮次+当前 head 的 failed/timed_out 运行，按 PR 去重（同轮次多 run 不重复计数）；`ship_failed` 含 pending 且 `ship_status=failed`（发货失败）与 `ship_hooks_status ∈ {failed, incomplete}`（已发货钩子失败/未齐，会在下次发货重试并自行消失）；`recommendations` 排除 `deferred_at`；`quota_attention` 只认 `credential_invalid`/`provider_error`，与额度页「需处理」横幅同口径。

## 「待我处理」区

- 位于 Hero/错误条之下、KPI 之上，同样受 `unavailable = isLoading || isError` 约束：**加载时渲染骨架屏，失败时整块不渲染**——不能显示缓存里的旧数字，也不能在失败时显示「一切正常」。
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
- 不要恢复旧的「日均是否达到最大值一半」判断（1 条/30 天会被标成下降），也不要用「最近 7 天 vs 前 7 天」做方向判断：序列最后一格是尚未结束的 UTC 当天，最近窗口天然少一天，平稳吞吐也会被判成下降。真要做周期对比，得先用两个**已结束**的窗口，并补上对应文案。
- 日均大于 0 且小于 `0.1` 时显示 `<0.1`；日均等于 0（窗口内没有已解决记录）时仍显示 `0.0`。直接 `toFixed(1)` 会把 1 条/30 天渲染成 `0.0`，看起来像没有数据。

## 纵轴刻度

取整阶梯：最大值不超过 2 时只分两档（`tickIntervals = 2`），其余分四档；`tickStep = max(ceil(maxTotal / tickIntervals), 1)`、`chartMax = tickStep * tickIntervals`。于是最大值为 1/2 时刻度是 `0/1/2`（峰值至少占图高一半），最大值 5/7 时是 `0/2/4/6/8`，最大值 1000 时是 `0/250/500/750/1000`。直接用 `max(maxTotal, 1)` 会在最大值很小时产生 `1、1、1、0、0` 这种重复刻度，也会让峰值只占四分之一高。刻度文字用 `text-[11px]` 与 `fill-muted-foreground`。

横轴日期从**最后一个点**按固定步长往前取（`stride = max(ceil(last / 6), 1)`），相邻标签间隔恒定，最后一个日期永远在最后一个点正下方；首点因此可能没有标签（30 个点时是索引 4/9/14/19/24/29），不要为了补首点而在中间插一个不均匀的间隔，也不要按 `round(last * i / (count - 1))` 均分（30 个点会得到 5、5、5、4、5、5 的不等间隔）。这是按点数的近似，不保证极窄容器（约 300px 以下）里 6 个 `MM-DD` 都不重叠；真要在窄屏用，再根据实测宽度减少刻度数。

图表的坐标系用**实测宽度**（`ResizeObserver` 取容器宽度，`viewBox` 与该宽度一致，1:1 像素）。不要改回固定 `720x220` 的 viewBox：那会把整个图（包括坐标轴文字）等比缩小，纵轴数字实际只剩 ~8.7px，横轴日期与数据点也会错位。横轴日期直接画在 SVG 内、和数据点共用同一套 x 坐标，所以「最后一个日期」永远落在最后一个点正下方。

## 测试

- `web/src/components/dashboard/metric-chart.test.tsx`：空状态、全零项目行（含不画填充段）、带 `href` 的行渲染为链接、`maxRows`+`collapsedLabel` 折叠行不可点、窗口全零、最大值 1/2 与 5 时的刻度阶梯。
- `web/src/routes/__tests__/dashboard-page.test.tsx`：「待我处理」区渲染组名/count/items 链接、「一切正常」空态、`complete === false` 降级（「N+ · 部分环境暂不可用」/「暂不可用」）、错误态不渲染该区；柱图 Top 10 折叠与行 href 指向 `/issues?project=<id>&state=open`；「已配置项目」KPI 链接 `/projects`；空数据时每张图表卡片只有一个空状态、无伪造项目行、入口链接指向 `/projects`、不渲染趋势图；日均 `<0.1` 的显示且不再有方向图标；首次失败与「缓存后 refetch 失败」两种错误态都隐藏旧数字（`unavailable` 条件）。
- 网格：`MetricChart` 的 tooltip 在图表转为空状态时必须消失（悬浮中数据可能被刷新成全零）。
