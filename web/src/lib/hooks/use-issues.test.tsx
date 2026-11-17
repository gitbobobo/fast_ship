import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const listMock = vi.fn().mockResolvedValue({ data: { items: [], total: 0, page: 1, page_size: 20 } });

vi.mock("@/lib/api/issues", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/issues")>()),
  issueApi: { list: (...args: unknown[]) => listMock(...args) },
}));

import { useInfiniteBoardIssues } from "./use-issues";

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

describe("useInfiniteBoardIssues workflow_status 接线", () => {
  beforeEach(() => listMock.mockClear());

  it("未设置列（''）发 workflow_status=unset，与列表 unset 过滤同源", async () => {
    renderHook(() => useInfiniteBoardIssues("p1", ""), { wrapper });
    await waitFor(() => expect(listMock).toHaveBeenCalled());
    expect(listMock.mock.calls[0][1]).toMatchObject({ state: "open", workflow_status: "unset" });
  });

  it("其余列同名透传", async () => {
    renderHook(() => useInfiniteBoardIssues("p1", "in_progress"), { wrapper });
    await waitFor(() => expect(listMock).toHaveBeenCalled());
    expect(listMock.mock.calls[0][1]).toMatchObject({ workflow_status: "in_progress" });
  });

  it("未知取值回落发原值，后端等值过滤得空列表而非放宽成全量", async () => {
    // 只能经 as 越域到达；钉住回落语义防将来新调用方把整列数据渲染进错列。
    renderHook(() => useInfiniteBoardIssues("p1", "bogus" as "todo"), { wrapper });
    await waitFor(() => expect(listMock).toHaveBeenCalled());
    expect(listMock.mock.calls[0][1]).toMatchObject({ workflow_status: "bogus" });
  });
});
