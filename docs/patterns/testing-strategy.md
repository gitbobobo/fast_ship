# Testing Strategy

Fast Ship 的测试策略和约定。后端使用 Go 标准测试框架，前端使用 Vitest + Testing Library。

## Where It Appears

- **server** — 所有 `*_test.go` 文件（19 个测试文件）
- **web** — `web/src/routes/__tests__/` 和 `*.test.*` 文件（20 个测试文件）

## Convention

### 后端测试

**框架**：Go 标准测试 + 手动断言
**位置**：测试文件与源文件同目录，`*_test.go` 命名
**覆盖范围**：
- `config/config_test.go` — 配置加载测试
- `handler/*_test.go` — Handler HTTP 测试
- `service/*_test.go` — Service 业务逻辑测试
- `router/router_test.go` — 路由注册测试
- `pkg/github/client_test.go` — GitHub 客户端测试
- `pkg/githubmedia/proxy_test.go` — 媒体代理测试
- `middleware/auth_test.go` — 认证中间件测试

**测试模式**：
```go
func TestServiceMethod(t *testing.T) {
    // Setup: 初始化依赖
    // Execute: 调用被测方法
    // Assert: 验证结果
}
```

### 前端测试

**框架**：Vitest + Testing Library + happy-dom/jsdom
**位置**：页面测试在 `web/src/routes/__tests__/`，组件测试在组件同级目录
**测试工具**：`web/src/test/render.tsx` 提供自定义 render 函数（包裹 Provider）
**覆盖范围**：
- `routes/__tests__/auth-pages.test.tsx` — 登录/注册页面
- `routes/__tests__/projects-page.test.tsx` — 项目页面
- `routes/__tests__/versions-page.test.tsx` — 版本页面
- `routes/__tests__/issue-pages.test.tsx` — Issue 页面
- `routes/__tests__/settings-*.test.tsx` — 设置页面
- `routes/__tests__/layout-guards.test.tsx` — 布局和路由守卫
- `components/github-content.test.tsx` — Markdown 渲染
- `components/ui/markdown-editor.test.tsx` — 编辑器
- `components/issues/internal-issue-form.test.tsx` — Issue 表单
- `lib/api/*.test.ts` — API 客户端单元测试

**测试模式**：
```typescript
import { render, screen } from '@/test/render'

test('renders correctly', () => {
  render(<Component />)
  expect(screen.getByText('Expected Text')).toBeInTheDocument()
})
```

## Examples

运行测试：
```bash
# 后端全部测试
cd server && go test ./...

# 后端特定包
cd server && go test ./internal/service/...

# 前端全部测试
cd web && pnpm test

# 前端特定文件
cd web && pnpm test -- src/routes/__tests__/auth-pages.test.tsx
```

CI 执行：
```bash
# CI 只检查前端 lint + typecheck
cd web && pnpm check
```

## Adding to This Pattern

添加新测试时：
1. 后端：在对应包目录创建 `*_test.go`，遵循 Arrange-Act-Assert 模式
2. 前端：页面测试放在 `__tests__/`，组件测试放在同级目录
3. 前端测试使用 `@/test/render.tsx` 的自定义 render 以确保 Provider 包裹
4. Mock API 响应使用 Vitest 的 `vi.mock` 或 Testing Library 的 `msw`
