# 项目列表顺序（Issue 总数降序）

凡是出现「Fast Ship 项目列表」的界面，项目先后只有一种口径：**按 Issue 总数降序（open 与
closed 都计入），计数缺失按 0，同数保持服务端顺序**。服务端顺序是 `created_at DESC`
（`ProjectRepository.List`），因此并列的项目里新建的在前。

## 顺序的唯一来源

顺序在 `useProjects`（`web/src/lib/hooks/use-projects.ts`）里一次性决定：按首页
`total` 算清剩余页数并发拉完全部分页，对合并结果排序一次，`items` 出参已经是最终顺序。
组件拿到什么顺序就展示什么顺序，不再各自排序——这既避免同一份数据在不同界面里排出
不同先后，也避免「分页前排序」把跨页的大项目埋在后面。

`issue_count` 由 `GET /api/projects` 列表项携带（`ProjectService.List` 调
`ProjectRepository.GetIssueCounts` 统计，`omitempty`，详情接口不带）。切换器卡片第二行的
`issue_workflow_counts` 是**开启口径**（仅 `state=open` 按内部状态分组），只用于展示状态
分布，不参与排序。

## 用到顺序的界面

- 项目页（`/projects`）卡片，只按搜索词过滤。
- 项目切换器 `ProjectSwitcher`：问题、看板、日志、文档、版本、截图等页面共用。
- 首次进入页面时的默认项目（`getActiveProjectId`，见 `web/src/lib/projects.ts`）：已选
  项与 URL 指定项在列表中即沿用（含搁置项目），否则取第一个未搁置项目，全部搁置才
  回落到 `projects[0]`。因此默认落在问题最多的活跃项目上；已保存的项目 ID 仍然优先，
  不会因重排改选。

以下两处不算「项目列表」，不套这条口径：

- 仪表盘的按项目统计图：它是按项目的开启问题数（`open_issue_count`）画柱，柱序与数值口径
  一体，改成总数排序反而让柱高失去意义。
- GitHub Token 的「引用项目」名册：展示的是 Token 被哪些项目用到的元数据，不是可浏览、
  可选的项目清单；为一个逗号拼接的名字串拉全量项目列表只为排序不划算。

## 搁置项目（INT-165）

`GET /api/projects` 始终返回含搁置项目的全量列表，`is_shelved` 标记是否搁置中；
「消失」只发生在前端展示层：

- `/projects` 页：搁置卡片收到底部默认折叠的「已搁置 (n)」分组，排序口径不变；
  卡片徽标显示「搁置至 YYYY-MM-DD」或「已搁置」（文案统一由
  `shelvedStatusLabel` 提供）。
- `ProjectSwitcher`：默认列表不列搁置项目；输入搜索词后可搜到并带搁置标记；
  当前选中项本身是搁置项目时触发器上也带标记。
- 仪表盘：后端在统计里排除搁置项目，前端只提示一行「另有 n 个项目已搁置」。
- 直接访问搁置项目的 issue 详情等链接照常，不做拦截。

## 新增界面时

需要项目列表的界面直接用 `useProjects()` 的 `items`，不要另加 `sort`。若确需不同顺序
（例如按名称字母序的选项），在调用处显式复制数组再排，并在注释里写明与这里口径不同的原因。
