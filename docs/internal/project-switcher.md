# 项目切换组件约定：ProjectSwitcher（INT-113）

board / issues / logs / screenshots / documents / versions 等页头的
「切换项目」统一走 `web/src/components/projects/project-switcher.tsx` 的
`ProjectSwitcher`，不要在页面里另写项目下拉。弹出面板对齐 T3 Code 新建
线程的项目选择器：顶部搜索框、竖向单列项目列表、行尾 `⌘N` 直选 chip、
底部键盘提示行。键盘与 Portal 约定同 `model-picker.md`（INT-112）；T3 的
「No project」项与返回箭头在项目域没有对应概念，不照搬。

## 组件

- 签名：`{ projects, value, onValueChange, placeholder?, disabled?,
  className?, ...data-* }`。触发器是「首字母徽标 + 项目名 + 下箭头」按钮，
  aria-label `切换项目：<当前项目名|placeholder>`，`className`/`data-*`
  透传到触发器。
- 面板经 `Popover.Portal` 挂到 document.body，aria-label `切换项目`；
  宽度 `w-80`，高度上限约 `max-h-80`（并受可视区高度约束），超出纵向滚动。
- 面板内容集中在内部组件 `ProjectSwitcherPanel`：每次打开重新挂载，搜索词
  与高亮自动重置，不需要手动清理。

## 列表行（`role="option"`，容器 `role="listbox"` aria-label「项目列表」）

- 左：确定性首字母徽标——多词名取前两个词首字母、单词名取前两个字符；
  背景色按项目 id 散列到固定色板（`BADGE_STYLES`），不引入新依赖。
- 中：第一行项目名（`font-medium` 截断），第二行副标题——描述优先、否则
  `owner/repo`，都没有则省略（INT-78 口径不变）。
- 右：四个状态计数点（未设置/待处理/开发中/已完成，只统计 `state=open`，
  语义同 `WORKFLOW_COUNT_SEGMENTS`；`issue_workflow_counts` 缺省时不渲染
  该组），加前 9 条可见行的 `⌘N`/`Ctrl+N` kbd chip。平台判断只看
  `navigator.platform`/`navigator.userAgent`，happy-dom 下同样生效。
- 当前项目行靠 `aria-selected` + 背景高亮表示；行内没有 GitHub 图标与选中
  对勾（INT-77 的简化保留）。

## 行为

- 排序只按开启状态 Issue 数降序，缺省按 0，同计数靠稳定排序保持传入顺序；
  过滤在拷贝上进行，不原地修改 `projects`。
- 搜索框 aria-label `搜索项目`，placeholder「搜索项目名称、描述或仓库」；
  匹配名称、描述、`owner/repo`，大小写不敏感；打开时自动聚焦；无结果
  显示「无匹配项目」。
- 键盘监听挂 window 捕获阶段，忽略输入法组合态（`isComposing`/`keyCode
  229`）与 `repeat`：`↑`/`↓` 环绕移动高亮并把高亮行滚入可视区，`Enter`
  选中高亮行，`Esc` 关闭并 `stopPropagation`（不连带关闭外层 Dialog），
  `⌘N`/`Ctrl+N`（N=1..9）直选第 N 条可见行。
- 点选/键选当前项目只关闭面板不回调；选其它项目回调一次并关闭。

## 测试

- `web/src/components/projects/project-switcher.test.tsx`。面板经 Portal
  挂载，用 `getByRole("dialog", { name: "切换项目" })` 定位；行
  `role="option"`、列表 `role="listbox"`；键盘高亮断言看
  `data-highlighted="true"`；数字键直选用
  `fireEvent.keyDown(window, { key: "1", metaKey: true })`（或 `ctrlKey`）。
