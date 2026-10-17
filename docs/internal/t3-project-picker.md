# T3 项目选择器约定：T3ProjectPicker（INT-113）

所有「选 T3 Code 项目」的入口统一走
`web/src/components/threads/t3-project-picker.tsx` 的 `T3ProjectPicker`，
不要再手写项目下拉（OptionSelect、DropdownMenu 平铺项目列表都算）。
外观与交互对齐 T3 Code 新建线程时的项目选择器：触发器是「项目首字母
徽标 + 项目名 + 下箭头」的 chip；弹出面板顶部搜索框，下方竖向单列
项目列表，底部一行键盘提示。

注意区分：`web/src/components/projects/project-switcher.tsx` 是 Fast Ship
项目域的全局项目切换，与 T3 项目（composer-options 返回的
`ComposerProject`）是两个概念，本组件不替代它。

## 组件

- Props：`{ projects: ComposerProject[]; value: string;
  onValueChange: (value: string) => void; disabled?; ariaLabel?;
  triggerLabel?; placeholder?; className? }`。`ariaLabel` 默认 `"项目"`，
  `placeholder` 默认「选择项目」。
- 触发器文本优先 `triggerLabel`，否则当前项目 `title`，否则
  `placeholder`。当前 `value` 不在 `projects` 里（换环境/未加载/快照
  过期）时组件不猜，由调用方用 `triggerLabel` 传 `xx（已失效）` 这类
  标注；此时徽标退化为 Folder 图标。
- 面板经 Portal（`@base-ui/react/popover`，main 上尚无 `ui/popover.tsx`
  封装，组件内直接使用原语）挂到 document.body，宽 w-80、高上限
  max-h-80，列表纵向滚动。
- 行内容：左侧彩色首字母徽标——多词名取前两个词首字母、单词名取前
  两字符，颜色按 `project.id` 稳定散列到固定调色板（不引入新依赖）；
  中间第一行项目名、第二行副标题（有 `workspace_root` 时
  `Local · <workspace_root>`，超过 30 字符的路径省略中间段、保留
  开头与项目目录，如 `/Users/…/projects/fast_ship`；更短的路径原样
  显示）。当前项目行 `aria-selected` 并带背景高亮。
- 搜索：打开自动聚焦；只按 `project.title` 大小写不敏感过滤；无结果
  显示「无匹配项目」。关闭面板即卸载，搜索词与高亮随之重置。
- 选中语义：点击当前项目只关闭面板不回调；点击其它项目回调一次
  `onValueChange` 并关闭。

## 键盘

监听挂 window 捕获阶段，忽略 `event.repeat` 与 IME 组合态
（`isComposing` / `keyCode === 229`）：

- `↑`/`↓` 在可见行间环绕移动高亮并把高亮行滚入可视区，`Enter`
  选中高亮行，`Esc` 关闭面板并 `stopPropagation`（不连带关闭外层
  Dialog）。
- 不做数字快捷键直选：`meta`/`ctrl` 组合键直接放行给浏览器，带
  修饰键的按键不参与列表导航。

## 与 INT-112 ModelPicker 的关系

交互约定与模型选择器（INT-112，PR #91 的 `ModelPicker`）保持一致：
chip 触发器、Portal 面板、搜索自动聚焦、↑↓/Enter/Esc 键盘协议、
`triggerLabel` 覆盖与 `ariaLabel` 约定均相同；本组件无数字快捷键
（`ModelPicker` 有，本项目选择器按需求不做）。两处有意不从
`thread-chips.tsx` 导入 `CHIP` 常量（避免与未合并分支互相依赖），
picker 内自带同一份 chip 样式字符串；对齐时若样式要改需同步两边。
项目列表比模型面板简单：没有 provider rail、收藏与多选。

## 调用点

- 新建线程表单（`new-thread-form.tsx`）：`ariaLabel="项目"`，
  `disabled` 沿用原项目 chip 的条件；无项目时 `triggerLabel` 传
  「加载中…」/「无可用项目」。
- 自动化编辑弹窗（`automations/editor-dialog.tsx`）：替换 T3 项目的
  OptionSelect，`ariaLabel="T3 项目"`、`className="w-full max-w-none"`
  占满一行；`onValueChange` 沿用原有联动清空（`model_selection`、
  `model_selections`、`workspace` 置空）；`triggerLabel` 覆盖
  「加载中…」/「加载失败，请重试」/`xx（已失效）`。

## 测试注意

- 面板内容经 Portal 挂到 document.body：行 `role="option"`、列表
  `role="listbox"`、搜索框 aria-label「搜索项目」。
- 路径省略逻辑导出了 `shortenPath(path, maxLength = 30)` 供单测；
  组件里以 `Local · <shortenPath(workspace_root)>` 渲染并带
  `title` 全路径。
- 带修饰键的数字键现在不应触发选择，测试断言 `onValueChange` 未被
  调用且面板保持打开即可。
- 徽标颜色来自项目 id 散列，测试不要断言具体颜色，断言首字母即可。
