# Web Routes

页面组件和路由定义。使用 React Router v7 文件系统约定组织路由，每个文件对应一个页面。支持认证布局和主应用布局两种外壳。

## Internal Structure

| File | Purpose |
|---|---|
| `web/src/routes/_auth-layout.tsx` | 认证页面布局（登录/注册），含品牌展示和链接 |
| `web/src/routes/_layout.tsx` | 主应用布局，含 Sidebar + Header，认证检查和用户数据初始化 |
| `web/src/routes/login.tsx` | 登录页面 |
| `web/src/routes/register.tsx` | 注册页面 |
| `web/src/routes/projects/index.tsx` | 项目列表页 |
| `web/src/routes/projects/new.tsx` | 新建项目页 |
| `web/src/routes/projects/$id/index.tsx` | 项目详情页 |
| `web/src/routes/projects/$id/edit.tsx` | 编辑项目页 |
| `web/src/routes/versions/index.tsx` | 版本列表页 |
| `web/src/routes/projects/$id/versions/new.tsx` | 新建版本页 |
| `web/src/routes/projects/$id/versions/$vid.tsx` | 版本详情页（含 Ship 操作） |
| `web/src/routes/issues/index.tsx` | Issue 列表页 |
| `web/src/routes/projects/$id/issues/new.tsx` | 新建 Issue 页 |
| `web/src/routes/projects/$id/issues/form.tsx` | Issue 表单组件（新建/编辑共用） |
| `web/src/routes/projects/$id/issues/$iid.tsx` | Issue 详情页 |
| `web/src/routes/projects/$id/issues/$iid/edit.tsx` | 编辑 Issue 页 |
| `web/src/routes/settings/layout.tsx` | 设置页面布局（侧边导航） |
| `web/src/routes/settings/profile.tsx` | 个人资料设置 |
| `web/src/routes/settings/password.tsx` | 修改密码 |
| `web/src/routes/settings/ai.tsx` | AI 服务配置 |
| `web/src/routes/settings/api-keys.tsx` | API Key 管理 |
| `web/src/routes/settings/general.tsx` | 通用设置 |

## Dependencies

| Depends on | Why |
|---|---|
| web-api | API 调用 |
| web-hooks | TanStack Query 数据获取 |
| web-state | 认证状态和主题 |
| web-components | UI 组件 |
| web-utils | 格式化和验证工具 |

## Implementation Notes

- `_layout.tsx` 是主应用外壳，负责认证检查、用户数据引导（bootstrap）、Sidebar 和 Header 渲染
- `_auth-layout.tsx` 是认证页面外壳（登录/注册），未认证用户访问主应用时自动重定向到登录页
- 路由使用 React Router v7 的文件命名约定：`$id` 表示动态参数，`_layout` 表示布局组件
- Issue 表单组件 (`form.tsx`) 被新建和编辑页面共用
- 设置页面使用嵌套布局，左侧导航切换右侧内容
