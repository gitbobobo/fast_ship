# Web Components / Features

应用业务组件。包含布局组件和功能组件。

## Internal Structure

| File | Purpose |
|---|---|
| `web/src/components/layout/header.tsx` | 顶部导航栏：用户菜单、主题切换、通知 |
| `web/src/components/layout/sidebar.tsx` | 侧边导航：桌面和移动端适配，支持折叠 |
| `web/src/components/user-nav.tsx` | 用户头像下拉菜单：设置、登出 |
| `web/src/components/theme-provider.tsx` | next-themes 主题 Provider 包裹组件 |
| `web/src/components/github-content.tsx` | GitHub Markdown 内容渲染器，含媒体代理 URL 替换 |
| `web/src/components/issues/internal-issue-form.tsx` | 内部 Issue 创建/编辑表单，含工作流状态和 Checklist |
| `web/src/components/projects/github-token-help-dialog.tsx` | GitHub Token 创建指南对话框 |

## Dependencies

| Depends on | Why |
|---|---|
| web-components/ui | Button, Dialog, DropdownMenu 等基础组件 |
| web-state | auth-store（用户信息）、theme-store |
| web-hooks | 数据获取 hooks |
| web-utils | github-media-proxy URL 转换 |

## Dependents

| Used by | How |
|---|---|
| web-routes | 页面组件中使用布局和功能组件 |

## Implementation Notes

- `github-content.tsx` 在渲染 Markdown 前扫描所有图片 URL，将 GitHub 原始 URL 替换为代理 URL，确保私有仓库图片可正常显示
- `sidebar.tsx` 使用 Sheet 组件实现移动端侧边栏抽屉效果
- `internal-issue-form.tsx` 支持内部 Issue 的完整创建流程，含标题、描述、工作流状态选择和 Checklist 编辑
