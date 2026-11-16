# 导航与项目路由（INT-237）

Web 端的 URL 分三层：全局页、项目页、详情页。项目上下文只认路径，页面内部
不再有「选择项目」的状态机；项目切换收敛在侧边栏一处。

## URL 层级

- 全局页：`/dashboard`、`/projects`、`/reviews`、`/notifications`、`/threads`、
  `/bots`、`/quota`、`/settings/*` 等。不属于任何项目，侧边栏链接直接可达。
- 项目页（六个 section）：`/projects/:id/{issues,board,versions,documents,screenshots,logs}`。
  页面标题是面包屑 `fast_ship / <项目名> / <section>`（问题/看板/版本/文档/截图/日志）。
- 详情/子页：`/projects/:id/issues/new`、`issues/:iid`、`issues/:iid/edit`、
  `versions/new`、`versions/:vid`、`logs/:runId`。同样挂在项目路径下，面包屑
  末段用各自语义（如运行详情）。

## 当前项目判定

- 项目级页面只认路径：`useRouteProject`（`web/src/lib/current-project.ts`）读
  `useParams` 的 `:id`，并用项目列表查询补出 `project / isLoading / notFound`。
  `notFound`（列表已就绪且查无此 ID）时整页渲染「项目不存在或无权访问」空态，
  数据 hooks 不挂载——既不请求、也不静默换项目；`isLoading` 不算无效，正常走骨架。
- 全局当前项目（侧边栏、旧路由重定向兜底）：`useCurrentProjectId`，裁决顺序
  见 `resolveCurrentProjectId`——路径 ID 优先（即使不在列表）；否则最近选择
  `lastSelectedProjectId`（仍需在列表）；否则第一个未搁置项目，全搁置回落列表
  首项；没有项目为 `null`。
- `AppLayout` 全局挂 `useSyncRouteProjectPreference`：凡在 `/projects/:id/*`
  下，把 `:id` 写入 `lastSelectedProjectId`。深链直接进 B 项目后，全局页的
  「项目内」入口与旧链接重定向都落在 B，而不是上次选过的项目。

## 切换跳转

- 唯一入口是侧边栏 `ProjectSwitcher`；页面内没有切换器，也不读写 `?project=`。
- 切换规则走 `projectSwitchPath(pathname, nextId)`（`web/src/lib/project-nav.ts`）：
  - 项目级列表页 → 目标项目同名 section（页内 query 不延续）；
  - 详情/new/edit/runId → 目标项目对应 section 的列表页；
  - 全局页或非项目路由 → 返回 `null`。
- 返回路径时 `navigate` 过去；返回 `null` 时只写 `lastSelectedProjectId`，
  pathname 不变，侧边栏「项目内」组的链接随之更新。

## 旧路由重定向

旧版单项目路由 `/issues`、`/board`、`/versions`、`/documents`、`/screenshots`、
`/logs`、`/logs/:runId` 全部是纯重定向组件
（`web/src/routes/legacy-redirects.tsx`，路由表见 `App.tsx`），不存在页面实现：

- 目标项目：query 里合法的 `project`（在列表中）优先，否则用
  `useCurrentProjectId` 的兜底裁决；没有任何项目时回 `/projects`。
- 项目列表未就绪时渲染空占位，不提前跳；查询失败时停留原地址渲染可重试占位，
  保住深链接的 query，不把用户弹到错误项目。
- query 迁移：`project` 从 query 移除（由路径承担），其余 query 原样保留，
  如 `/logs/run-9?project=p2&level=error` → `/projects/p2/logs/run-9?level=error`。
- 特例：`/projects/:id/versions/new` 由路由表直转
  `/projects/:id/versions?create=1`，由列表页弹出创建框。
