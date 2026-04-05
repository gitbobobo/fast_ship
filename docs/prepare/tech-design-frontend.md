# Fast Ship — 前端技术设计文档

## 1. 技术栈

| 层级 | 技术 | 说明 |
|------|------|------|
| 包管理 | [pnpm](https://pnpm.io/) | 磁盘效率高、速度快、严格依赖管理 |
| 构建工具 | [Vite](https://vitejs.dev/) | 极速 HMR、ESBuild 预构建、开箱即用 |
| 框架 | [React 19](https://react.dev/) | 组件化 UI 开发 |
| 语言 | TypeScript | 类型安全 |
| 路由 | [React Router 7](https://reactrouter.com/) | 文件式路由 + Data Loader |
| UI 组件 | [shadcn/ui](https://ui.shadcn.com/) | 基于 Radix UI + Tailwind，源码可控 |
| 样式 | [Tailwind CSS 4](https://tailwindcss.com/) | 原子化 CSS |
| 状态管理 | [Zustand](https://zustand.docs.pmnd.rs/) | 轻量、直觉式全局状态 |
| 数据请求 | [TanStack Query](https://tanstack.com/query) | 服务端状态管理、缓存、自动重试 |
| HTTP 客户端 | [ky](https://github.com/sindresorhus/ky) | 基于 fetch 的轻量 HTTP 客户端 |
| 表单 | [React Hook Form](https://react-hook-form.com/) + [Zod](https://zod.dev/) | 表单管理 + Schema 校验 |
| Markdown | [React Markdown](https://github.com/remarkjs/react-markdown) | Release 说明 Markdown 渲染 |
| 图标 | [Lucide React](https://lucide.dev/) | shadcn/ui 默认图标库 |
| Toast 通知 | [Sonner](https://sonner.emilkowal.dev/) | shadcn/ui 推荐的 Toast 方案 |

## 2. 项目结构

```
web/
├── public/
│   └── favicon.svg
├── src/
│   ├── main.tsx                    # 应用入口
│   ├── App.tsx                     # 根组件（路由 + Provider）
│   ├── routes/                     # 页面路由
│   │   ├── _layout.tsx             # 全局 Layout（侧边栏 + 顶栏）
│   │   ├── _auth-layout.tsx        # 未登录 Layout（居中卡片）
│   │   ├── login.tsx               # 登录页
│   │   ├── register.tsx            # 注册页
│   │   ├── projects/
│   │   │   ├── index.tsx           # 项目列表
│   │   │   ├── new.tsx             # 创建项目
│   │   │   ├── $id/
│   │   │   │   ├── index.tsx       # 项目详情（版本列表）
│   │   │   │   ├── edit.tsx        # 编辑项目
│   │   │   │   └── versions/
│   │   │   │       ├── new.tsx     # 创建版本
│   │   │   │       └── $vid.tsx    # 版本详情（安装包 + 发货）
│   │   └── settings/
│   │       ├── index.tsx           # 个人设置
│   │       └── api-keys.tsx        # API Key 管理
│   ├── components/
│   │   ├── ui/                     # shadcn/ui 组件（自动生成）
│   │   ├── layout/
│   │   │   ├── sidebar.tsx         # 侧边栏
│   │   │   ├── header.tsx          # 顶栏
│   │   │   └── user-menu.tsx       # 用户菜单
│   │   ├── project/
│   │   │   ├── project-card.tsx    # 项目卡片
│   │   │   └── project-form.tsx    # 项目表单（新建/编辑复用）
│   │   ├── version/
│   │   │   ├── version-list.tsx    # 版本列表
│   │   │   ├── version-form.tsx    # 版本表单
│   │   │   ├── version-status.tsx  # 状态徽章
│   │   │   ├── ship-button.tsx     # 发货按钮 + 校验弹窗
│   │   │   └── release-notes.tsx   # Markdown 编辑/预览
│   │   ├── artifact/
│   │   │   ├── artifact-list.tsx   # 安装包列表
│   │   │   ├── upload-zone.tsx     # 文件上传区域
│   │   │   └── artifact-item.tsx   # 单个安装包行
│   │   └── api-key/
│   │       ├── api-key-list.tsx    # API Key 列表
│   │       └── create-dialog.tsx   # 创建 API Key 弹窗
│   ├── lib/
│   │   ├── api/
│   │   │   ├── client.ts          # ky 实例（baseURL、拦截器）
│   │   │   ├── auth.ts            # 认证接口
│   │   │   ├── projects.ts        # 项目接口
│   │   │   ├── versions.ts        # 版本接口
│   │   │   └── artifacts.ts       # 安装包接口
│   │   ├── hooks/
│   │   │   ├── use-auth.ts        # 认证相关 Hook
│   │   │   ├── use-projects.ts    # 项目 CRUD Hook
│   │   │   ├── use-versions.ts    # 版本 CRUD Hook
│   │   │   └── use-artifacts.ts   # 安装包操作 Hook
│   │   ├── store/
│   │   │   └── auth-store.ts      # 全局认证状态（Token、用户信息）
│   │   └── utils/
│   │       ├── format.ts          # 格式化工具（日期、文件大小）
│   │       └── validators.ts      # Zod Schema 定义
│   └── styles/
│       └── globals.css             # Tailwind 全局样式
├── index.html
├── vite.config.ts
├── tsconfig.json
├── tailwind.config.ts
├── components.json                 # shadcn/ui 配置
├── package.json
└── pnpm-lock.yaml
```

## 3. 路由设计

### 3.1 路由表

| 路由 | 页面 | Layout | 鉴权 |
|------|------|--------|------|
| `/login` | 登录 | AuthLayout | 否 |
| `/register` | 注册 | AuthLayout | 否 |
| `/projects` | 项目列表 | AppLayout | 是 |
| `/projects/new` | 创建项目 | AppLayout | 是 |
| `/projects/:id` | 项目详情 | AppLayout | 是 |
| `/projects/:id/edit` | 编辑项目 | AppLayout | 是 |
| `/projects/:id/versions/new` | 创建版本 | AppLayout | 是 |
| `/projects/:id/versions/:vid` | 版本详情 | AppLayout | 是 |
| `/settings` | 个人设置 | AppLayout | 是 |
| `/settings/api-keys` | API Key 管理 | AppLayout | 是 |

### 3.2 路由守卫

```tsx
// 未登录 → 重定向到 /login
// 已登录访问 /login、/register → 重定向到 /projects
```

通过 React Router 的 `loader` 机制在路由级别校验认证状态，避免页面闪烁。

## 4. 数据层设计

### 4.1 HTTP 客户端

```typescript
// lib/api/client.ts
import ky from "ky";

export const api = ky.create({
  prefixUrl: "/api",
  hooks: {
    beforeRequest: [
      (request) => {
        const token = useAuthStore.getState().token;
        if (token) {
          request.headers.set("Authorization", `Bearer ${token}`);
        }
      },
    ],
    afterResponse: [
      async (_request, _options, response) => {
        if (response.status === 401) {
          useAuthStore.getState().logout();
          window.location.href = "/login";
        }
      },
    ],
  },
});
```

### 4.2 TanStack Query 封装

每个业务模块封装为自定义 Hook，统一管理缓存 key 与请求逻辑：

```typescript
// lib/hooks/use-projects.ts
export function useProjects() {
  return useQuery({
    queryKey: ["projects"],
    queryFn: () => projectApi.list(),
  });
}

export function useCreateProject() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: projectApi.create,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["projects"] });
    },
  });
}
```

### 4.3 Query Key 规范

| Key | 说明 |
|-----|------|
| `["projects"]` | 项目列表 |
| `["projects", id]` | 单个项目详情 |
| `["projects", id, "versions"]` | 项目下版本列表 |
| `["versions", vid]` | 单个版本详情 |
| `["versions", vid, "artifacts"]` | 版本下安装包列表 |
| `["api-keys"]` | API Key 列表 |
| `["auth", "me"]` | 当前用户信息 |

### 4.4 全局状态（Zustand）

仅存储认证状态，业务数据全部交给 TanStack Query：

```typescript
// lib/store/auth-store.ts
interface AuthState {
  token: string | null;
  user: User | null;
  setAuth: (token: string, user: User) => void;
  logout: () => void;
}
```

Token 持久化到 `localStorage`，应用启动时读取并调用 `/api/auth/me` 校验有效性。

## 5. 核心页面交互

### 5.1 版本详情页

版本详情页是功能最密集的页面，包含以下区块：

```
┌─────────────────────────────────────────────┐
│  ← 返回项目     版本 v1.2.0      🟡 待发货   │
├─────────────────────────────────────────────┤
│                                             │
│  📋 基本信息                                 │
│  版本号: v1.2.0                              │
│  目标分支: main                              │
│  创建时间: 2026-04-06 10:00                  │
│                                             │
├─────────────────────────────────────────────┤
│                                             │
│  📝 Release 说明                 [编辑] [预览] │
│  ┌─────────────────────────────────────┐    │
│  │  Markdown 编辑器 / 预览区域          │    │
│  └─────────────────────────────────────┘    │
│                                             │
├─────────────────────────────────────────────┤
│                                             │
│  📦 安装包                        [上传文件]  │
│  ┌─────────────────────────────────────┐    │
│  │  app-release.apk   45.2MB  android  🗑  │
│  │  FastShip.ipa      62.1MB  ios      🗑  │
│  └─────────────────────────────────────┘    │
│                                             │
├─────────────────────────────────────────────┤
│                                             │
│              [ 🚀 发货到 GitHub ]             │
│                                             │
└─────────────────────────────────────────────┘
```

### 5.2 发货交互流程

```
点击「发货」按钮
      │
      ▼
┌──────────────────┐
│ 弹出确认对话框     │  ← 展示版本号、安装包数量、目标分支
│  显示校验结果      │     ✅ Release 说明  ✅ 安装包(2)  ✅ 目标分支
└──────┬───────────┘
       │ 用户确认
       ▼
┌──────────────────┐
│ 按钮变为加载状态   │  ← 禁用所有编辑操作
│ 展示进度步骤       │     1. 创建 Tag... ✅
│                   │     2. 创建 Release... ⏳
│                   │     3. 上传安装包...
└──────┬───────────┘
       │
       ├── 成功 → Toast 提示 + 页面刷新（状态变为已发货，所有编辑入口隐藏）
       │
       └── 失败 → 错误对话框展示失败原因 + 引导用户修复后重试
```

### 5.3 文件上传交互

- 使用 shadcn/ui 风格的拖拽上传区域
- 支持多文件同时选择
- 上传时显示进度条（使用 `XMLHttpRequest` 或 `fetch` + `ReadableStream` 获取进度）
- 上传完成后自动刷新安装包列表
- 大文件上传期间禁止重复提交

### 5.4 API Key 创建交互

```
点击「创建 API Key」
      │
      ▼
┌──────────────────┐
│ 弹出对话框         │  ← 输入备注名称（如 "CI-Android"）
└──────┬───────────┘
       │ 提交
       ▼
┌──────────────────┐
│ 展示完整 Key      │  ← 一键复制按钮
│ 警告提示：         │     "此 Key 仅展示一次，请立即复制保存"
└──────────────────┘
```

## 6. 样式与主题

### 6.1 shadcn/ui 配置

```json
{
  "style": "new-york",
  "tailwind": {
    "baseColor": "zinc"
  },
  "aliases": {
    "components": "@/components",
    "utils": "@/lib/utils"
  }
}
```

### 6.2 预装组件清单

根据页面需求，需安装以下 shadcn/ui 组件：

| 组件 | 用途 |
|------|------|
| `button` | 按钮 |
| `input` | 文本输入 |
| `label` | 表单标签 |
| `card` | 项目卡片、信息卡片 |
| `dialog` | 确认弹窗、创建 API Key |
| `table` | API Key 列表、安装包列表 |
| `badge` | 版本状态标签 |
| `tabs` | Markdown 编辑/预览切换 |
| `textarea` | Release 说明编辑 |
| `select` | 平台选择 |
| `dropdown-menu` | 用户菜单、操作菜单 |
| `toast` (sonner) | 操作反馈 |
| `alert-dialog` | 删除二次确认 |
| `separator` | 分隔线 |
| `skeleton` | 加载占位 |
| `form` | 表单校验集成 |
| `progress` | 上传进度 |

## 7. 开发与构建

### 7.1 开发代理

Vite 开发环境将 `/api` 请求代理到后端服务：

```typescript
// vite.config.ts
export default defineConfig({
  server: {
    proxy: {
      "/api": {
        target: "http://localhost:8080",
        changeOrigin: true,
      },
    },
  },
});
```

### 7.2 构建产物

生产构建产物输出到 `web/dist/`，由后端 Gin 服务通过 `gin.Static` 直接托管，实现前后端单体部署：

```go
// 后端路由配置
r.Static("/assets", "./web/dist/assets")
r.NoRoute(func(c *gin.Context) {
    c.File("./web/dist/index.html")
})
```

### 7.3 常用命令

```bash
pnpm install              # 安装依赖
pnpm dev                  # 启动开发服务器
pnpm build                # 生产构建
pnpm preview              # 预览构建产物
pnpm dlx shadcn@latest add <component>  # 添加 shadcn/ui 组件
```

## 8. 关键约定

### 8.1 文件命名

- 组件文件：`kebab-case.tsx`（如 `project-card.tsx`）
- 工具函数：`kebab-case.ts`（如 `format.ts`）
- 类型定义：就近放置在使用模块中，通用类型提取到 `lib/types.ts`

### 8.2 组件规范

- 页面组件使用默认导出，业务组件使用命名导出
- 表单统一使用 React Hook Form + Zod Schema
- 所有 API 请求通过 TanStack Query Hook，不在组件中直接调用 `api`
- 加载态使用 `skeleton` 占位，避免页面跳动

### 8.3 错误处理

- TanStack Query 全局 `onError` 处理网络异常，通过 Sonner Toast 展示
- 表单提交失败在 `mutationFn` 的 `onError` 中处理，展示服务端返回的具体错误信息
- 401 响应全局拦截，自动跳转登录页
