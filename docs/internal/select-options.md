# 选择器约定：OptionSelect（INT-92）

Base UI 的 `Select` 在收起态由 `Select.Value` 渲染当前值：它先找子内容，没有
子内容再按 `Select.Root` 的 `items` 查标签，两者都没有就直接显示内部 value。
`yes`/`no`、`on`/`off` 这类内部值一旦透出到界面就是 bug。

## 约定

- 业务代码一律用 `src/components/ui/option-select.tsx` 的 `OptionSelect`：
  传 `options: {value, label, disabled?, itemLabel?}[]`，组件内部会把 options
  同时渲染下拉项并传给 `Select.Root` 的 `items`，收起态按 value 显示 `label`。
- 下拉项内容和收起态标签不一致时用 `itemLabel`（如「全部状态」在收起态显示
  为「状态」、分支名在下拉项带「(默认)」后缀）。
- 「不限/任意/全部」这类全放行选项不要用 `""` 做 value——Base UI 把 `""`
  视为未选中，收起态会显示成 placeholder 样式。用哨兵值（`__all__`、`__any__`、
  `__none__`、`__root__`、`unset`），在组件边界再映射回领域空值。
- 选项是异步加载的（环境、项目、模型、父文档列表等），当前 value 可能在
  options 就位前先行存在，收起态会短暂闪出原始 id。用 `withMissingOption`
  把当前值补成占位项：加载中传「加载中…」，加载完仍缺失传 `xx（已失效）`。
- 收起态要显示选项表查不到的内容（快照标题、占位回退）时用 `valueLabel`
  覆盖；它只在有选中值时生效，空值时 placeholder 优先。
- 需要分组、复合行内结构等新形态时，在 `src/components/ui/` 下加封装并在
  `eslint-rules/no-raw-select.js` 的 `ALLOWED_FILES` 登记，不要在业务文件里
  直接用原始 `Select`。

## 防线

- `fast-ship/no-raw-select`：业务文件以任何形式引入原始 Select 都会报错——
  `@/components/ui/select`（含相对路径、带扩展名）、`@base-ui/react/select`、
  `@base-ui/react` 根包的具名 `Select`、`export ... from` 转发、动态
  `import()`。
- `fast-ship/select-value-label`：`<SelectValue />`（含 `<Select.Value>` 成员
  写法）无子内容且所属 `<Select>` 没传 `items` 时报错。`{undefined}`、
  `{null}`、`{""}` 这类静态可判空的子内容不算数；Select 侧的 `{...props}`
  展开不算传了 `items`；SelectValue 侧的 `{...props}` 可能携带 children
  （封装层透传）按有子内容处理；其他动态表达式静态无法求证，默认信任。
- `src/components/ui/option-select.test.tsx` 断言收起态渲染 label 而非
  value；页面级回归见 `src/routes/__tests__/automations-page.test.tsx`。
