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

- 内容区左侧覆盖式抽屉（INT-238）：宽窄屏同一套实现。sticky 容器（`sticky top-14 z-20 h-0 mx-auto max-w-7xl`，与正文容器同宽）紧跟 Header，不占文档流高度；抽屉内层 `left-4 md:left-6` 对齐正文容器内边距，即把手与面板贴正文卡片左缘（收起时把手离屏幕/侧栏边缘 16/24px）；面板宽屏 300px、<640px 视口近满宽（`calc(100%-2.5rem)`——用容器百分比不用 `dvw`，后者不扣 main 的非 overlay 滚动条会横向溢出；右侧留 24px 不露出截断控件），无遮罩，不盖全局侧边栏与顶栏；150ms transform 滑入滑出，`motion-reduce` 直切。
- 左缘把手是唯一开合控件（`aria-expanded`/`aria-controls="issue-source-list-panel"`，面板带 `role="region" aria-label="来源列表"`，头部带 `tabIndex=-1` 的 `issue-source-list-title`）：常态显示图标不依赖 hover——图标 `List`/`ListX`（**不要**用 PanelLeft*，右下角移动侧栏 FAB 也是 PanelLeft，同族会混淆入口）14px `h-3.5` 配 `text-neutral-600`（暗色 `neutral-300`），底 `neutral-300` 描边 `neutral-400`（暗色 `600/500`），`focus-visible` 给 `ring-2`；hover 变宽加深。把手顶部锚定（`top-8` 避开卡片左上圆角），**不要**垂直居中——居中会落在卡片纵向中部、切断卡片左描边，看起来像渲染残留。宽屏收起态面板多平移 10px（`md:calc(100%+0.625rem)`），把手落进容器内边距死区呈浮动小片（`rounded-md`），只与卡片描边重叠 2px；窄屏死区只有 16px 放不下把手，`-translate-x-full` 回贴卡片左缘（离屏缘 16px）。窄屏打开态把手 `max-sm:hidden` 不渲染——头部 X 是主要关闭控件，把手吊在右缘空隙像悬空残块；此时 `closeSourcePanel` 的焦点还原走 `requestAnimationFrame` 等把手回到 DOM。伪元素命中区：收起向左扩进死区（`after:-left-4 md:after:-left-6`），右侧 +4px 不越进正文可见区；打开时收窄到 `after:-left-1 after:-right-1`。**不要**把命中区向右扩过正文内边距——会吞掉正文左缘点击。无来源（`hasSourceContext` 为 false）不渲染把手与抽屉。
- 交互：`[` 或点把手开关（打开后焦点进面板标题，Tab 顺着进列表）；Esc 在输入控件聚焦时也收起抽屉（外来浮层打开时让位，且各浮层/内联编辑器消费 Esc 时 `preventDefault`，避免一次按键两处生效）；点抽屉外部（document `pointerdown`，面板与把手除外）收起；宽屏点选列表项保持打开，窄屏点选收起；J/K 翻页不变。四条主动收起路径（`[`/Esc/点把手/窄屏 X/窄屏点选）统一走 `closeSourcePanel` 把焦点还原到把手；点外部收起不还原（焦点落用户点的目标）。
- 来源上下文消失（同路由跳到无来源详情页、后退）时 effect 复位 `isSourcePanelOpen`：不 reset 会残留 true——FAB 被永久隐藏、回到有来源页抽屉莫名自动展开。FAB 渲染条件同时联动 `hasSourceContext`。
- 面板头部：「来源列表 + 来源描述·条数」副行 + 当前 issue `reference·标题` 行（抽屉会盖住正文标题区，头部补当前项上下文；颜色保持 `text-muted-foreground` 不再降透明度，避免小字低于 AA）；头部 `pl-9` 与列表项文字 x 对齐（`p-1.5`+`px-2`+选中条+未读点列+gap ≈ 36px）；窄屏（`sm:hidden`）另有 40px X 关闭按钮——近满宽抽屉的点外部只剩细条，X 是主要退出，把手与 Esc 作补充。
- 窄屏移动侧栏 FAB（`fixed bottom-6 right-6 z-40 lg:hidden`）在抽屉打开时不渲染：深色圆形 PanelLeft 图标浮在面板之上会被当成抽屉关闭钮。
- 偏好不持久化：每次进页默认收起（`issueSourceListExpanded` 已删除，旧 localStorage 键无消费者）。
- 抽屉与来源数据共用同一 `useIssueSourceList` 实例，**不要**挂两份控制器；`IssueSourceList` 组件只负责渲染与滚动定位。
- 全局快捷键避让：`defaultPrevented`、IME 组合态、Ctrl/Meta/Alt、`input/textarea/select/contenteditable`、其他 Dialog/AlertDialog/Sheet/DropdownMenu/Popover/Select 浮层打开（`document.querySelector('[data-slot=…]')` 探测）。抽屉自身是页面内普通面板（非 sheet-content），不在避让名单，打开时 `[`/J/K 仍归详情页。

## 看板侧编码（`web/src/routes/board/`）

- 各列筛选（`{label, source}`）提升到 `BoardPage` 持有（按 `ColumnId` 的 map，项目切换时重置），受控传给 `BoardColumn`。
- `BoardPage.getIssueColumnContext(issue)`：列 = `issue.internal_meta?.workflow_status` 对应列（`COLUMNS.statusValue`，`""`→`unset`）；列未知（如 issue 数据未加载）返回 null，此时卡片/详情链接**不带**来源参数。
- 卡片标题 Link（`board-issue-card.tsx`）与推荐面板「打开完整详情页」（`recommendation-detail-pane.tsx`）都用它编码 `issue_from=board` 参数；推荐面板取**实际展示问题**（含 peek 依赖）的上下文，而非原推荐项。

## 缓存识别注意点

`useMarkIssueRead` 只对 `isIssueListOrBoardQuery`（按 queryKey 形状识别）的缓存清未读数。新增无限列表查询的 key 是 `["projects", projectId, "issues", "infinite", state, q, label, source, workflow_status, sort]`——识别分支必须包含 `key[3] === "infinite"`，否则标记已读后来源列表的未读标记不会清除。看板查询走 `key[3] === "board"`，普通分页列表按 `key.length >= 11` 识别。
