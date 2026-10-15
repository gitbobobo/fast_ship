# 选择器约定：OptionSelect（INT-92）

Base UI 的 `Select` 在收起态由 `Select.Value` 渲染当前值：它先找子内容，没有
子内容再按 `Select.Root` 的 `items` 查标签，两者都没有就直接显示内部 value。
`yes`/`no`、`on`/`off` 这类内部值一旦透出到界面就是 bug。

## 约定

- 业务代码一律用 `src/components/ui/option-select.tsx` 的 `OptionSelect`：
  传 `options: {value, label, disabled?, itemLabel?}[]`，组件内部会把 options
  同时渲染下拉项并传给 `Select.Root` 的 `items`，收起态必然显示 `label`。
- 下拉项内容和收起态标签不一致时用 `itemLabel`（如「全部状态」在收起态显示
  为「状态」、分支名在下拉项带「(默认)」后缀）。
- 收起态要显示选项表查不到的内容（快照标题、占位回退）时用 `valueLabel` 覆盖。
- 需要分组、复合行内结构等新形态时，在 `src/components/ui/` 下加封装并在
  `eslint-rules/no-raw-select.js` 的 `ALLOWED_FILES` 登记，不要在业务文件里
  直接用原始 `Select`。

## 防线

- `fast-ship/no-raw-select`：业务文件 import `@/components/ui/select`（含
  相对路径等价物）或 `@base-ui/react/select` 直接报错。
- `fast-ship/select-value-label`：`<SelectValue />` 无子内容且所属
  `<Select>` 没传 `items` 时报错，兜住封装层及将来例外放行处的写法。
- `src/components/ui/option-select.test.tsx` 断言收起态渲染 label 而非
  value；页面级回归见 `src/routes/__tests__/automations-page.test.tsx`。
