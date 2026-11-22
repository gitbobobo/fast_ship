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

- 主区左缘覆盖式抽屉（INT-238）：宽窄屏同一套实现。sticky 容器（`sticky top-14 z-20 h-0 w-full`）铺满 main 宽度、紧跟 Header，不占文档流高度；抽屉内层 `left-0` 贴主区左缘——即紧贴全局侧边栏右缘（`aside md:w-56`/`w-14`，main 无横向内边距），窄屏无常驻侧栏则贴屏幕左缘。**不要**回到 `mx-auto max-w-7xl` + `left-4/6` 对齐正文卡片的旧几何——用户明确要贴侧栏一侧、不与正文卡片边对齐。面板宽屏 300px、<640px 视口近满宽（`calc(100%-1.5rem)`，贴左缘后右侧留 ~24px 不露出截断控件），无全屏遮罩（窄屏露出条上有轻遮罩，见下），不盖全局侧边栏与顶栏；150ms transform 滑入滑出，`motion-reduce` 直切。
- 抽屉 JSX 提取为组件级 `sourceDrawer` 变量，加载中/问题不存在/加载完成三个 return 分支都渲染在同一位置（`[Header, 抽屉|null, 正文/骨架]`，INT-242）：早退分支曾只渲染 `[Header, 骨架 div]`，React 把骨架顶层 div 复用为完成分支的抽屉外层容器，className 从无平移跳到 `-translate-x-full` + `transition-transform`，进页播一次 0%→-100% 收起过渡。抽屉与来源数据不依赖 issue 加载（`hasSourceContext` 在 `issue === undefined` 时即真），加载态即可开抽屉翻列表；**不要**用加 key 止动画的方式绕开。回归测试在 `issue-pages.test.tsx`（「renders the drawer while the issue is loading…」）：加载→完成切换断言面板与外层平移容器保持同一 DOM 节点且平移类无跳变。
- 左缘把手是唯一开合控件（`aria-expanded`/`aria-controls="issue-source-list-panel"`，面板带 `role="region" aria-label="来源列表"`，头部带 `tabIndex=-1` 的 `issue-source-list-title`）：常态显示图标不依赖 hover——图标 `List`/`ListX`（**不要**用 PanelLeft*，右下角移动侧栏 FAB 也是 PanelLeft，同族会混淆入口）14px `h-3.5` 配 `text-neutral-600`（暗色 `neutral-300`），底 `neutral-300` 描边 `neutral-400`（暗色 `600/500`），`focus-visible` 给 `ring-2`；hover 变宽加深。收起态图标上方叠一个 `text-[9px]` 条数数字（`sourceCount`，即已加载数，加载/失败不渲染）——纯图标竖条看不出是「来源列表」入口、也不知道里面有几条；aria-label 同步带 `，已加载 N 个`。把手顶部锚定（`top-8`），**不要**垂直居中——居中像渲染残留。收起时 `-translate-x-full`（宽窄一致，不再有多平移 10px 的死区浮动小片），把手落在主区左缘贴侧边栏/屏幕边；把手 `rounded-r-md` + `border-l-0`（弹层方边、把手保留圆角——需求方定调：弹层不圆角、把手不圆太丑）。窄屏可见宽 `max-sm:w-7`（28px）——窄屏它是唯一入口，16px 难于点中且贴近侧滑手势区更需要足够目标。伪元素命中区：收起只向右扩（`after:left-0 after:-right-4`）——**不要**向左扩，会盖住侧边栏右缘吞掉导航点击；打开时收窄到 `after:-left-1 after:-right-1`。窄屏打开态把手 `max-sm:hidden` 不渲染——头部 X 是主要关闭控件；此时 `closeSourcePanel` 的焦点还原走 `requestAnimationFrame` 等把手回到 DOM。无来源（`hasSourceContext` 为 false）不渲染把手与抽屉。
- 交互：`[` 或点把手开关（打开后焦点进面板标题，Tab 顺着进列表）；Esc 在输入控件聚焦时也收起抽屉（外来浮层打开时让位，且各浮层/内联编辑器消费 Esc 时 `preventDefault`，避免一次按键两处生效）；点抽屉外部（document `pointerdown`，面板与把手除外）收起；宽屏点选列表项保持打开，窄屏点选收起；J/K 翻页不变。四条主动收起路径（`[`/Esc/点把手/窄屏 X/窄屏点选）统一走 `closeSourcePanel` 把焦点还原到把手；点外部收起不还原（焦点落用户点的目标）。
- 来源上下文消失（同路由跳到无来源详情页、后退）时 effect 复位 `isSourcePanelOpen`：不 reset 会残留 true——FAB 被永久隐藏、回到有来源页抽屉莫名自动展开。FAB 渲染条件同时联动 `hasSourceContext`。
- 面板头部固定两行（INT-242 精简）：「来源列表」标题 + 「来源描述·计数」副行，不再有当前 issue `reference·标题` 行（当前项靠列表高亮 + 自动滚动定位，抽屉 JSX 也因此与 issue 加载状态解耦）；来源描述由 `describeIssueSourceContext` 生成，带类型前缀（board → `看板 · …`，list → `列表 · …`，INT-242）；计数为「已加载 / 总数」（如 `20 / 57`）：已加载 = 非 stale 条数，总数 = 接口首页 `total`（`useIssueSourceList.total`，取 `data.pages[0].total`），加载中/失败仍用「—」占位——「0」会暗示空列表是成功结果、误导用户跳过重试；头部 `pl-9` 与列表项文字 x 对齐（`p-1.5`+`px-2`+选中条+未读点列+gap ≈ 36px）；窄屏（`sm:hidden`）另有 40px X 关闭按钮——近满宽抽屉的点外部只剩细条，X 是主要退出，把手与 Esc 作补充。
- 列表行（`IssueSourceList`）为紧凑两行（INT-242，一屏可见条数明显多于旧三行布局）：第 1 行 = 等宽弱化编号 + 未读点 + 标题单行截断（`title` 属性悬停看全文）；第 2 行 = 弱化小字元信息用 `·` 分隔——内部状态（小圆点+文字，用色同 `IssueWorkflowStatusBadge` 三色；`showInternalStatus=false` 时省略，页面在看板列来源传 false，整列相同不重复）；OPEN/CLOSED 由来源上下文稳定决定（INT-242 审查收敛：不按已加载页动态判定，避免补拉后整列补显状态文字回流）：仅列表来源未筛选状态（可混态）时逐行恒显示，首帧即定、翻页/补拉不变；看板列（固定 open）与带状态筛选的列表来源不显示；圆点着色不用胶囊；标签最多 2 个余「+N」（github 用 `github.labels`，internal 用 `internal_meta.labels`）；PR 聚合（`pull_request_summary.total>0` 时「PR N」，有 merged 加「· 合 M」）；相对更新时间（`formatRelativeTime`）；GitHub 负责人小头像重叠排列最多 3 个余「+N」（仅 `source === "github"` 且有 `github.assignees`）。当前项高亮保留左侧竖条（`border-primary`）+ 淡背景（`bg-accent/50`），不再加粗。
- 列表三态要有明确区分：加载中 spinner + 「加载中…」；空数据中性「暂无问题」；加载失败红色 `CircleAlert` + 红字「来源列表加载失败」+ 重试按钮，不与空态混为中性色。stale 保留项整行弱化、不展示快照元信息（状态/时间可能已过期），第二行只保留「已不符合来源条件」标记，行 `title` 提示保留语义。
- 面板无圆角、只 `border-r` 单边描边（暗色 `neutral-600`）——需求方要求方边、边框克制，**不要**加 `rounded-r-*` 或双层 ring 描边（曾用 `0_0_0_1px` ring + border 被评「太刻意」）。窄屏打开时面板右侧 ~24px 留白上盖一条 `bg-black/10`（暗色 `bg-black/40`）轻遮罩（`sm:hidden`，随抽屉平移、打开才渲染）——底层卡片描边/圆角断线露出像渲染残留；遮罩不在面板内，点击仍走「点外部收起」，别改成全宽抽屉。把手 hover 图标要持续加深（`neutral-900`/暗色 `white`），不能 hover 反而变淡。
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
