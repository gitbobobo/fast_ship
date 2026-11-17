# 问题详情页来源上下文（INT-231）

详情页的「来源列表」替代旧的上/下一条按钮：从看板卡片或问题列表进入详情时，URL 携带来源参数，详情页据此渲染该来源的列表（标题旁按钮 + `[` 快捷键，宽屏左栏 / 窄屏左侧抽屉），J/K 按列表顺序翻页。本文档记录 URL 约定、解析语义、导航行为与实现要点。

## URL 约定

来源上下文只放 URL，不用 sessionStorage/内存快照——刷新、复制链接后列表不变。全部参数以 `issue_` 前缀挂在详情页路径上：

| 参数 | 含义 | 写入方 |
|---|---|---|
| `issue_from=board` | 来源是看板卡片 | `buildBoardSourceSearchParams` |
| `issue_workflow_status` | 所在看板列：`todo` / `in_progress` / `done` / `unset`（「未设置」列编码为 `unset`） | 同上 |
| `issue_label` / `issue_source` | 该列当前筛选（有且非 all 时才写入） | 同上 |
| `issue_state` / `issue_q` / `issue_sort` / `issue_page` | 问题列表页来源（沿用旧约定） | `buildIssueDetailSearchParams`（`web/src/lib/issue-list-context.ts`） |
| `issue_workflow_status` | 列表页的内部状态筛选（值可为 `unset` 等） | 同上 |

### 三态解析（`readIssueSourceContext`）

- `issue_from=board` **且** `issue_workflow_status` 为合法列值 → `{kind:"board"}`；固定 `state=open`，只用列/标签/来源，忽略混入的 `q`/`sort`。
- `issue_from=board` 但列值非法/缺失 → `none`；未知 `issue_from` 值 → `none`。
- 无 `issue_from` 但存在任一 `issue_*` 参数 → `{kind:"list"}`（`all` 归一为空，page 下限 1，sort 缺省 `updated_desc`）。
- 无任何来源参数 → `{kind:"none"}`，不显示来源列表入口，J/K 无效。

`kind:"none"` 之外两种都算「有来源」。另外当前问题加载后发现 `project_id` 与来源项目不一致时（脏链接），入口同样隐藏、查询禁用。

## 来源列表行为（`useIssueSourceList`，详情页 components 目录）

- 数据走 `useInfiniteIssueList`（`web/src/lib/hooks/use-issues.ts`，`page_size=20`）。
- 当前问题未出现在已加载页时**按顺序补拉**直到出现或没有更多页——不能把「还没加载到」当「已移出筛选」插到首尾。
- 当前问题曾出现后被查询移出时，按最后已知位置插入保留项（`stale`），UI 弱化并标记「已不符合来源条件」，直到离开详情页。
- J/K：J 在末项且有下一页时先补拉再自动前进（`pendingAdvance` 期间防重复跳转）；来源或项目变化清空保留项与补拉状态；控制器随详情路由卸载。
- 点击与 J/K 均 **replace** 导航：保留来源参数，清掉上一问题的 `comments_page`/`timeline_page`/hash；list 来源还会把 `issue_page` 更新为目标所在页（第 1 页不编码）。首次进入详情仍是 push，因此浏览器一次返回即回到进入前的看板/列表。
- 列表加载失败保留「重试」，不冒充终点。

## UI 接入（`$iid.tsx`）

- 内容区左侧覆盖式抽屉（INT-238）：宽窄屏同一套实现。sticky 容器（`sticky top-14 z-20 h-0 mx-auto max-w-7xl`，与正文容器同宽）紧跟 Header，不占文档流高度；面板宽屏 300px、<640px 视口近满宽（`calc(100dvw-24px)`，右侧不留露出截断控件的窄条）`absolute left-0` 覆盖正文左缘，无遮罩，不盖全局侧边栏与顶栏；150ms transform 滑入滑出，`motion-reduce` 直切。
- 左缘把手是唯一开合控件（`aria-expanded`/`aria-controls="issue-source-list-panel"`，面板带 `role="region" aria-label="来源列表"`）：伪元素向右扩展命中区到约 44px 供触屏点按；常态显示淡色图标（窄屏 `max-lg:opacity-80`），hover 加宽加深。无来源（`hasSourceContext` 为 false）不渲染把手与抽屉。
- 交互：`[` 或点把手开关；Esc 在输入控件聚焦时也收起抽屉（外来浮层打开时让位，且各浮层/内联编辑器消费 Esc 时 `preventDefault`，避免一次按键两处生效）；点抽屉外部（document `pointerdown`，面板与把手除外）收起；宽屏点选列表项保持打开，窄屏点选收起；J/K 翻页不变。收起（`[`/Esc/点把手）后焦点还原到把手。
- 偏好不持久化：每次进页默认收起（`issueSourceListExpanded` 已删除，旧 localStorage 键无消费者）。
- 抽屉与来源数据共用同一 `useIssueSourceList` 实例，**不要**挂两份控制器；`IssueSourceList` 组件只负责渲染与滚动定位。
- 全局快捷键避让：`defaultPrevented`、IME 组合态、Ctrl/Meta/Alt、`input/textarea/select/contenteditable`、其他 Dialog/AlertDialog/Sheet/DropdownMenu/Popover/Select 浮层打开（`document.querySelector('[data-slot=…]')` 探测）。抽屉自身是页面内普通面板（非 sheet-content），不在避让名单，打开时 `[`/J/K 仍归详情页。

## 看板侧编码（`web/src/routes/board/`）

- 各列筛选（`{label, source}`）提升到 `BoardPage` 持有（按 `ColumnId` 的 map，项目切换时重置），受控传给 `BoardColumn`。
- `BoardPage.getIssueColumnContext(issue)`：列 = `issue.internal_meta?.workflow_status` 对应列（`COLUMNS.statusValue`，`""`→`unset`）；列未知（如 issue 数据未加载）返回 null，此时卡片/详情链接**不带**来源参数。
- 卡片标题 Link（`board-issue-card.tsx`）与推荐面板「打开完整详情页」（`recommendation-detail-pane.tsx`）都用它编码 `issue_from=board` 参数；推荐面板取**实际展示问题**（含 peek 依赖）的上下文，而非原推荐项。

## 缓存识别注意点

`useMarkIssueRead` 只对 `isIssueListOrBoardQuery`（按 queryKey 形状识别）的缓存清未读数。新增无限列表查询的 key 是 `["projects", projectId, "issues", "infinite", state, q, label, source, workflow_status, sort]`——识别分支必须包含 `key[3] === "infinite"`，否则标记已读后来源列表的未读标记不会清除。看板查询走 `key[3] === "board"`，普通分页列表按 `key.length >= 11` 识别。
