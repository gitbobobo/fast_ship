# Web Components / UI

shadcn/ui 基础组件库。基于 Radix UI 原语和 Tailwind CSS 构建的 20 个标准化 UI 组件。

## Included Components

| Component | File | Description |
|---|---|---|
| AlertDialog | `alert-dialog.tsx` | 确认对话框 |
| Avatar | `avatar.tsx` | 用户头像 |
| Badge | `badge.tsx` | 标签/徽章 |
| Button | `button.tsx` | 按钮（多变体） |
| Card | `card.tsx` | 卡片容器 |
| Dialog | `dialog.tsx` | 通用对话框 |
| DropdownMenu | `dropdown-menu.tsx` | 下拉菜单 |
| Input | `input.tsx` | 输入框 |
| Label | `label.tsx` | 表单标签 |
| MarkdownEditor | `markdown-editor.tsx` | Markdown 编辑器（基于 @uiw/react-md-editor） |
| Select | `select.tsx` | 下拉选择 |
| Separator | `separator.tsx` | 分隔线 |
| Sheet | `sheet.tsx` | 侧边抽屉 |
| Skeleton | `skeleton.tsx` | 加载占位 |
| Sonner | `sonner.tsx` | Toast 通知（基于 sonner） |
| Table | `table.tsx` | 表格 |
| Tabs | `tabs.tsx` | 标签页 |
| Textarea | `textarea.tsx` | 多行文本框 |
| ThemeToggle | `theme-toggle.tsx` | 主题切换按钮 |
| Tooltip | `tooltip.tsx` | 提示信息 |

## Dependencies

| Depends on | Why |
|---|---|
| `@base-ui/react` | 无障碍原语 |
| `class-variance-authority` | 组件变体管理 |
| `tailwind-merge` + `clsx` | CSS 类名合并 |
| `sonner` | Toast 通知 |
| `@uiw/react-md-editor` | Markdown 编辑器 |

## Implementation Notes

- 所有组件遵循 shadcn/ui 约定：使用 `cva` 定义变体，`cn()` 合并类名
- 这些是标准化的基础组件，业务逻辑在 `features` 子模块的组件中
- 修改这些组件通常通过 `shadcn` CLI 重新生成
