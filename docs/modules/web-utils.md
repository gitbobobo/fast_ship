# Web Utils

工具函数、验证器和格式化器。

## Public API

| Export | File | Description |
|---|---|---|
| `cn(...classes)` | `utils.ts` | CSS 类名合并 (clsx + tailwind-merge) |
| `formatFileSize(bytes)` | `utils/format.ts` | 文件大小格式化 (KB/MB/GB) |
| `formatDate(date)` | `utils/format.ts` | 日期格式化 |
| `formatRelativeTime(date)` | `utils/format.ts` | 相对时间格式化 (3 分钟前) |
| `githubMediaProxyUrl(url)` | `utils/github-media-proxy.ts` | 将 GitHub 原始 URL 转为代理 URL |
| Zod schemas | `utils/validators.ts` | 表单验证 schemas（项目/版本/Issue/用户设置） |

## Internal Structure

| File | Purpose |
|---|---|
| `web/src/lib/utils.ts` | CSS 类名合并工具 |
| `web/src/lib/utils/format.ts` | 文件大小和日期格式化 |
| `web/src/lib/utils/github-media-proxy.ts` | GitHub 媒体 URL 代理转换 |
| `web/src/lib/utils/validators.ts` | Zod 验证 schemas |

## Dependencies

| Depends on | Why |
|---|---|
| `clsx` + `tailwind-merge` | CSS 类名合并 |
| `zod` | 表单验证 |

## Dependents

| Used by | How |
|---|---|
| web-components | `cn()` 用于所有组件的类名合并 |
| web-routes | 格式化函数和验证器在页面中使用 |
| web-components/features | `githubMediaProxyUrl` 在 Markdown 渲染中替换图片 URL |

## Implementation Notes

- `cn()` 是整个 UI 层的基础工具，合并 Tailwind CSS 类时自动处理冲突
- `validators.ts` 定义了所有表单的 Zod schema，与 `react-hook-form` 的 `zodResolver` 配合使用
- `githubMediaProxyUrl` 将 GitHub 用户内容 URL（`raw.githubusercontent.com`）替换为本地代理端点（`/api/github/media-proxy`），并附加认证 Token
