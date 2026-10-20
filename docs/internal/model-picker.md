# 模型选择器约定：ModelPicker / ModelMultiPicker（INT-112）

所有「模型切换」入口统一走 `web/src/components/threads/model-picker.tsx`
的两个组件，不要再手写模型下拉（OptionSelect、DropdownMenu 平铺模型列表、
内联 checkbox 列表都算）。外观对齐 T3 Code 的模型选择器：触发器是
provider 图标 + 模型名 + 下箭头的 chip；弹出面板左侧是 provider 竖排图标
rail（第一项「收藏」星标），右侧搜索框 + 模型列表。

## 组件

- `ModelPicker`（单选）：`{ providers, resolved, onPick, disabled?, ariaLabel?,
  triggerLabel?, leadingOption?, className? }`。`resolved` 用
  `resolveModel(providers, t3Project, pick)` 计算；点行回调
  `onPick({instanceId, model, efforts: {}})` 并关闭面板。
- `ModelMultiPicker`（多选）：`{ providers, selected: ModelPick[], onToggle,
  disabled?, ariaLabel?, className? }`。点行回调 `onToggle`、面板不关闭；
  不显示也不响应数字键直选（避免歧义）。
- 共同的触发器约定：aria-label 默认 `"模型"`；单选默认文本
  `resolved.model?.name ?? "默认模型"`，多选空选显示「选择模型」、否则前
  两个模型名 + `+N`。
- `leadingOption` 把一行固定在列表最上方（「项目默认」这类非模型选项），
  不受搜索/rail 过滤影响，点击后关闭。`selected: true` 时触发器默认显示
  其 `label`（调用方仍可用 `triggerLabel` 覆盖，如「已失效」标注）。
- 面板复用 `web/src/components/ui/popover.tsx` 的 `Popover`（Base UI
  `@base-ui/react/popover` 封装）；触发器样式复用 `thread-chips.tsx`
  导出的 `CHIP` 常量；provider 图标一律 `ProviderIcon`。
- 模型行副标题默认是 provider 名；上游模型带 `subProvider`（pi 的
  `commandcode`/`opendesign` 等）时补成 `provider · subProvider`，与 T3 Code
  一致（同 provider 下同名模型靠它区分）。子 provider 名以 provider 名开头时
  （`OpenCode` / `OpenCode Zen`）只显示子 provider，纯空白视同没有；搜索命中
  模型名与这条副标题全文。
- `ProviderChoice` 可带 `iconId`（INT-133）：图标解析键与实例 id 不一致时用它
  （机器人表单把 AI 提供商的 `catalog_id` 当图标键，`instanceId` 仍是
  `ai_providers.id`）；缺省回落 `instanceId`。
- ACP registry 接入的 harness 按实例后缀取官方图标：`acpRegistry_devin` →
  devin、`acpRegistry_factory_droid` → `factory-droid`（注册表 agent id 把
  `-` 换成 `_`）。图标全量内联在 `acp-registry-icons.ts`（生成文件，快照自
  ACP registry，当前 42 个 agent），不依赖外部 CDN；只有注册表里没有的
  agent 才回落通用 `acp` glyph。解析入口是 `resolveProviderIcon`。

## 收藏

- 行尾星标切换收藏（`stopPropagation`，不触发行选中）；收藏 rail 项
  （`aria-label="收藏"`）只列收藏过的模型。打开面板时若存在**当前可用的**
  收藏，默认落在收藏 rail（对齐 T3），否则落在当前 provider（无当前
  provider 时用第一个 provider）。
- 存储：localStorage `fast-ship-model-favorites`，zustand persist
  （`web/src/lib/store/model-favorites-store.ts`）。key 格式
  `modelFavoriteKey(instanceId, slug)` = `` `${instanceId}::${slug}` ``——
  用 `::` 分隔，因为 instanceId 可能含 `_`/`-`。

## 键盘

- `↑`/`↓` 在当前可见行间移动高亮（环绕），`Enter` 选中高亮行，`Esc`
  关闭面板（不影响外层 Dialog）。`leadingOption` 也纳入高亮与 `↑`/`↓`/`Enter`
  导航（固定在第一位）。
- 不提供数字快捷键：面板不显示也不响应 `⌘N`/`Ctrl+N`。
- 搜索框内的 IME 组合事件（`event.isComposing` / `keyCode === 229`）不触发选择。

## 调用点

- 新建线程表单（`new-thread-form.tsx`）、从 issue 启动线程
  （`launch-issue-thread-dialog.tsx`）、从截图标注启动线程
  （`launch-annotation-thread-dialog.tsx`）：单选 `ModelPicker`。
- 审查组合行（`components/reviews/combination-row.tsx`，INT-135）：单选
  `ModelPicker`，provider rail 即 harness 实例；每个选项描述符再配一个
  `EffortChip` 组成一行。`triggerLabel` 覆盖「先选环境」/「加载中…」/
  「加载失败，请重试」/「xx（已失效）」；选值经 `resolveModel` +
  `buildModelSelection` 落成 canonical `options` 数组。
  `ModelMultiPicker` 目前没有调用点（多选需求随通用自动化一起移除）。
- 机器人表单（`bots/bot-form-dialog.tsx`，INT-133）：把已配置的 AI 提供商
  映射成 `ProviderChoice`（`provider-choices.ts`），模型清单来自内置
  models.dev 目录；目录里没有模型清单的提供商（`custom`）不进 picker，由
  表单另给手填模型输入框。选中回调的 `instanceId` 就是 `provider_id`。

## 测试注意

- 面板内容经 Portal 挂到 document.body：行 `role="option"`、列表
  `role="listbox"`、rail 按钮 aria-label 为 provider 名；收藏按钮 aria-label
  为 `收藏 <模型名>`/`取消收藏 <模型名>`。
