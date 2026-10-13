import type { ReactElement, ReactNode } from "react";
import { render } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

// 组件树里任何 useQuery（如项目表单的 Token 库选择器）都需要 QueryClient。
// rerender 的测试要与 renderWithRoute 用同一个包装，保持元素类型一致，
// 否则整棵树重挂载、未保存的表单输入会被重置。
export function withTestQueryClient(ui: ReactElement) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return (
    <QueryClientProvider client={queryClient}>{ui}</QueryClientProvider>
  );
}

export function renderWithRoute(
  ui: ReactElement,
  {
    path = "/",
    initialEntry = "/",
  }: {
    path?: string;
    initialEntry?: string;
  } = {},
) {
  return render(
    withTestQueryClient(
      <MemoryRouter initialEntries={[initialEntry]}>
        <Routes>
          <Route path={path} element={ui} />
        </Routes>
      </MemoryRouter>,
    ),
  );
}

export function wrapWithRouter(children: ReactNode) {
  return <MemoryRouter>{withTestQueryClient(children as ReactElement)}</MemoryRouter>;
}
