# 项目列表顺序（Issue 总数降序）

凡是出现「Fast Ship 项目列表」的界面，项目先后只有一种口径：**按 Issue 总数降序（open 与
closed 都计入），计数缺失按 0，同数保持服务端顺序**。服务端顺序是 `created_at DESC`
（`ProjectRepository.List`），因此并列的项目里新建的在前。

## 顺序的唯一来源

顺序在 `useProjects`（`web/src/lib/hooks/use-projects.ts`）里一次性决定：拉完全部分页后
对合并结果排序一次，`items` 出参已经是最终顺序。组件拿到什么顺序就展示什么顺序，不再各自
排序——这既避免同一份数据在不同界面里排出不同先后，也避免「分页前排序」把跨页的大项目
埋在后面。

`issue_count` 由 `GET /api/projects` 列表项携带（`ProjectService.List` 调
`ProjectRepository.GetIssueCounts` 统计，`omitempty`，详情接口不带）。切换器卡片第二行的
`issue_workflow_counts` 是**开启口径**（仅 `state=open` 按内部状态分组），只用于展示状态
分布，不参与排序。

## 用到顺序的界面

- 项目页（`/projects`）卡片，只按搜索词过滤。
- 项目切换器 `ProjectSwitcher`：问题、看板、日志、文档、版本、截图等页面共用；
  自动化表单的项目下拉也走同一份 `items`（顺序随之一致）。
- 首次进入页面时的默认项目（`getActiveProjectId` 回退到 `projects[0]`）：因此默认落在
  问题最多的项目上，与列表第一项一致；已保存的项目 ID 仍然优先，不会因重排改选。

以下两处不算「项目列表」，不套这条口径：

- 仪表盘的按项目统计图：它是按项目的开启问题数（`open_issue_count`）画柱，柱序与数值口径
  一体，改成总数排序反而让柱高失去意义。
- GitHub Token 的「引用项目」名册：展示的是 Token 被哪些项目用到的元数据，不是可浏览、
  可选的项目清单；为一个逗号拼接的名字串拉全量项目列表只为排序不划算。

## 新增界面时

需要项目列表的界面直接用 `useProjects()` 的 `items`，不要另加 `sort`。若确需不同顺序
（例如按名称字母序的选项），在调用处显式复制数组再排，并在注释里写明与这里口径不同的原因。
