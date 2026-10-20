import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import { type ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { projectApi } from "@/lib/api/projects";
import { useProjects } from "./use-projects";

vi.mock("@/lib/api/projects", () => ({
  projectApi: {
    list: vi.fn(),
  },
}));

const listMock = vi.mocked(projectApi.list);

function makeProject(id: string, issueCount?: number): Project {
  return {
    id,
    user_id: "user-1",
    name: `项目-${id}`,
    description: "",
    github_owner: "",
    github_repo: "",
    code_github_owner: "",
    code_github_repo: "",
    public_github_token_id: "",
    code_github_token_id: "",
    ...(issueCount === undefined ? {} : { issue_count: issueCount }),
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
  };
}

function page(page: number, items: Project[], total: number) {
  return {
    code: 0,
    message: "success",
    data: { items, total, page, page_size: 100 },
  };
}

function renderUseProjects() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  );
  return renderHook(() => useProjects(), { wrapper });
}

describe("useProjects", () => {
  beforeEach(() => {
    listMock.mockReset();
  });

  it("单页时只请求一次并返回 items", async () => {
    listMock.mockResolvedValueOnce(page(1, [makeProject("a")], 1));
    const { result } = renderUseProjects();

    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.items.map((p) => p.id)).toEqual(["a"]);
    expect(listMock).toHaveBeenCalledTimes(1);
    expect(listMock).toHaveBeenCalledWith(1, 100);
  });

  it("total 超过第一页时按序拉完剩余分页并合并", async () => {
    const page1 = Array.from({ length: 100 }, (_, i) => makeProject(`p${i}`));
    const page2 = Array.from({ length: 30 }, (_, i) => makeProject(`p${100 + i}`));
    listMock
      .mockResolvedValueOnce(page(1, page1, 130))
      .mockResolvedValueOnce(page(2, page2, 130));
    const { result } = renderUseProjects();

    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.items).toHaveLength(130);
    expect(result.current.data?.items[129].id).toBe("p129");
    expect(result.current.data?.total).toBe(130);
    expect(listMock).toHaveBeenCalledTimes(2);
    expect(listMock).toHaveBeenNthCalledWith(2, 2, 100);
  });

  it("服务端少于一页时提前停止，不空转分页", async () => {
    // total 声称 200 但第 2 页已不满页：防止脏 total 导致无限翻页
    const page1 = Array.from({ length: 100 }, (_, i) => makeProject(`p${i}`));
    const page2 = [makeProject("extra")];
    listMock
      .mockResolvedValueOnce(page(1, page1, 200))
      .mockResolvedValueOnce(page(2, page2, 200));
    const { result } = renderUseProjects();

    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.items).toHaveLength(101);
    expect(listMock).toHaveBeenCalledTimes(2);
  });

  it("合并分页后按 Issue 总数降序排列，缺省按 0 且不原地修改响应数组", async () => {
    // 服务端按 created_at DESC 分页返回，排序只在合并后做一次：
    // 否则跨页的大计数项目会被埋在后面的页里
    const firstPage = [makeProject("old-empty"), makeProject("old-few", 2)];
    listMock
      .mockResolvedValueOnce(page(1, firstPage, 3))
      .mockResolvedValueOnce(page(2, [makeProject("new-busy", 9)], 3));
    const { result } = renderUseProjects();

    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.items.map((p) => p.id)).toEqual([
      "new-busy",
      "old-few",
      "old-empty",
    ]);
    // 排的是副本：响应页数组保持服务端返回顺序
    expect(firstPage.map((p) => p.id)).toEqual(["old-empty", "old-few"]);
  });

  it("同计数保持服务端顺序，显式 0 与计数缺失并列", async () => {
    // 计数并列（含缺失与显式 0）时不引入第二排序键，稳定排序保持服务端顺序
    listMock.mockResolvedValueOnce(
      page(
        1,
        [
          makeProject("tie-first", 2),
          makeProject("missing"),
          makeProject("zero", 0),
          makeProject("tie-second", 2),
        ],
        4,
      ),
    );
    const { result } = renderUseProjects();

    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.items.map((p) => p.id)).toEqual([
      "tie-first",
      "tie-second",
      "missing",
      "zero",
    ]);
  });
});
