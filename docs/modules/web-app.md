# Web App

前端应用入口和根组件。`main.tsx` 挂载 React 应用并配置 Provider；`App.tsx` 定义路由结构和认证守卫布局。

## Public API

无直接导出，作为应用入口使用。

## Internal Structure

| File | Purpose |
|---|---|
| `web/src/main.tsx` | 应用入口：渲染 `<App />`，包裹 ThemeProvider 和 StrictMode |
| `web/src/App.tsx` | 路由定义：使用 React Router v7 的 `createLazyRoute` 懒加载所有页面 |
| `web/src/index.css` | 全局样式：Tailwind CSS 导入和自定义 CSS 变量 |

## Dependencies

| Depends on | Why |
|---|---|
| web-state | ThemeProvider 和 auth 初始化 |
| web-components | ThemeToggle 等基础组件 |
| web-routes | 所有页面路由组件 |

## Implementation Notes

- `App.tsx` 使用 `@tanstack/react-query` 的 `QueryClientProvider` 包裹整个应用，配置了默认的 staleTime 和 retry 策略
- 所有页面组件通过 `createLazyRoute` 实现代码分割，减少首屏加载体积
- Vite 开发服务器通过 `server.proxy` 将 `/api` 请求转发到后端 `http://localhost:8080`
- 生产环境由 Go 后端直接托管 `web/dist/` 静态文件
