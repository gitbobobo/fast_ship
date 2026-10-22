# 仪表盘与图表空状态

`/dashboard` 是唯一使用 `web/src/components/dashboard/metric-chart.tsx` 的页面。这里的约定是 INT-151 定下的：空数据时不要把「没有数据」画成一张有刻度的图，也不要用看起来可点、实际不可点的元素。

## 数据形状

`GET /api/dashboard/overview`（`server/internal/service/dashboard.go`）按登录用户聚合：

- `open_issues_by_project`：该用户的**全部项目**（`repository/dashboard.go` 从 `projects` 出发 `LEFT JOIN issues`），没有开启问题的项目计数为 `0`。所以**空数组 ⟺ 没有项目**，有项目时最低也会返回 `0` 行。
- `daily_resolved`：固定 30 条（含 0 值日期），按 `issue_internal_meta.completed_at` 分日、按项目聚合。近 30 天没有完成记录时 30 条全是 0；更早完成的问题不计入这个窗口。

## 空状态约定

- 图表判定为空时**不渲染坐标轴、网格、曲线与点**，只渲染一处居中的次要文字（`ChartEmptyState`）。空状态不叠在图上，也不伪造数据点（旧实现曾塞一行「暂无项目」，与服务端的 `0` 行叠成两套空状态）。
- 空状态可以带一个真实入口 `emptyAction`，由页面传入；入口用 `Button render={<Link/>}`（如「去创建项目」→ `/projects`），不要用带边框的虚线胶囊伪装成按钮。
- 柱状图只在 `points.length === 0` 时算空：有项目但开启问题都清零时保留项目行和 `0`，读者仍能看到项目本身。
- 面积图在 `points.length === 0` 或窗口内最大值 `0` 时算空。
- 卡片右上角的「日均 x.x」读数与趋势图标只在近 30 天有已解决数据时显示；空数据时整段隐藏（底部「日均解决」统计条仍显示 `0.0`），避免在空状态下暗示「在变差」。

## 纵轴刻度

阶梯取整：`tickStep = max(ceil(maxTotal / 4), 1)`、`chartMax = tickStep * 4`，刻度为 `0/1/2/3/4`、`0/2/4/6/8`、`0/250/500/750/1000` 等。直接用 `max(maxTotal, 1)` 会在最大值很小时产生 `1、1、1、0、0` 这种重复刻度。

## 测试

- `web/src/components/dashboard/metric-chart.test.tsx`：空状态、全零项目行、窗口全零、最大值 1 时的刻度唯一性。
- `web/src/routes/__tests__/dashboard-page.test.tsx`：空数据时每张卡片只有一个空状态、无伪造项目行、入口链接指向 `/projects`、不渲染趋势图。
