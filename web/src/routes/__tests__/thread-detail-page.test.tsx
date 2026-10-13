import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiRequestError } from "@/lib/api/client";
import ThreadDetailPage from "@/routes/threads/$environmentId/$threadId";
import type { ThreadDetail, ThreadItem } from "@/lib/api/threads";
import { signInAs, teardownAuth } from "@/test/auth";

const { detail, itemDetail } = vi.hoisted(() => ({
  detail: vi.fn(),
  itemDetail: vi.fn(),
}));

vi.mock("@/lib/api/threads", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/threads")>()),
  threadApi: { list: vi.fn(), detail, itemDetail },
}));

function detailOf(
  items: ThreadItem[],
  status: ThreadDetail["thread"]["status"] = "idle",
  updatedAt = "2026-10-13T10:00:00Z",
) {
  return {
    data: {
      thread: {
        id: "t1",
        environment_id: "e1",
        environment_name: "Mac",
        project_id: "p1",
        project_name: "Alpha",
        title: "Fix login",
        status,
        branch: "main",
        provider: "codex",
        model: "gpt-5",
        updated_at: updatedAt,
        pull_request_count: 1,
      },
      items,
    },
  };
}

function mockDetail(items: ThreadItem[], status: ThreadDetail["thread"]["status"] = "idle") {
  detail.mockResolvedValue(detailOf(items, status));
}

function item(over: Partial<ThreadItem> & Pick<ThreadItem, "id" | "type">): ThreadItem {
  return { text: "", summary: "", ...over };
}

let client: QueryClient;

async function renderPage() {
  const view = render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/threads/e1/t1"]}>
        <Routes>
          <Route path="/threads/:environmentId/:threadId" element={<ThreadDetailPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  await screen.findByText("Mac · Alpha");
  return view;
}

describe("ThreadDetailPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    signInAs("u1");
  });
  afterEach(teardownAuth);

  it("requests the thread from the route params", async () => {
    mockDetail([]);
    await renderPage();
    expect(detail).toHaveBeenCalledWith("e1", "t1", expect.any(AbortSignal));
    expect(screen.getByText("这个线程还没有内容")).toBeInTheDocument();
  });

  it("renders full markdown for messages and never shows images", async () => {
    const longTail = "END-OF-LONG-MESSAGE";
    mockDetail([
      item({ id: "1", type: "user_message", text: "## 需求\n\n- 一\n- 二\n\n![图](https://x/y.png)" }),
      item({
        id: "2",
        type: "assistant_message",
        text: `${"很长的内容 ".repeat(3000)}\n\n\`\`\`ts\nconst a = 1;\n\`\`\`\n\n${longTail}`,
      }),
    ]);
    const { container } = await renderPage();

    const user = screen.getByTestId("thread-item-user_message");
    expect(within(user).getByRole("heading", { name: "需求" })).toBeInTheDocument();
    expect(user.querySelectorAll("li")).toHaveLength(2);
    expect(container.querySelector("img")).toBeNull();
    expect(screen.getByTestId("thread-item-assistant_message").textContent).toContain(longTail);
    expect(container.querySelector("pre code")).not.toBeNull();
  });

  it("collapses reasoning, commands, tools and file changes until expanded", async () => {
    const user = userEvent.setup();
    mockDetail([
      item({ id: "r", type: "reasoning", summary: "想一想", text: "SECRET-REASONING" }),
      item({ id: "c", type: "command_execution", summary: "pnpm test", text: "OUT-LINE", status: "completed" }),
      item({ id: "f", type: "file_change", summary: "修改 3 个文件", text: "a.ts\nb.ts" }),
      item({ id: "d", type: "dynamic_tool", summary: "search", text: "TOOL-OUT" }),
    ]);
    await renderPage();

    for (const text of ["SECRET-REASONING", "OUT-LINE", "a.ts", "TOOL-OUT"]) {
      expect(screen.queryByText(text, { exact: false })).toBeNull();
    }
    expect(screen.getByText("想一想")).toBeInTheDocument();
    expect(screen.getByText("pnpm test")).toBeInTheDocument();

    await user.click(screen.getByText("pnpm test"));
    expect(screen.getByText("OUT-LINE")).toBeInTheDocument();
    await user.click(screen.getByText("想一想"));
    expect(screen.getByText("SECRET-REASONING")).toBeInTheDocument();
    await user.click(screen.getByText("修改 3 个文件"));
    expect(screen.getByText(/a\.ts/)).toBeInTheDocument();
    expect(itemDetail).not.toHaveBeenCalled();
  });

  it("links subagents to their child thread", async () => {
    const user = userEvent.setup();
    mockDetail([item({ id: "s", type: "subagent", summary: "Explorer", text: "找到了入口", child_thread_id: "child 1" })]);
    await renderPage();

    expect(screen.queryByRole("link", { name: "查看子线程" })).toBeNull();
    await user.click(screen.getByText("Explorer"));
    expect(screen.getByRole("link", { name: "查看子线程" })).toHaveAttribute(
      "href",
      "/threads/e1/child%201",
    );
  });

  it("is read only: no inputs or write actions", async () => {
    mockDetail([item({ id: "1", type: "assistant_message", text: "hi" })], "running");
    await renderPage();
    expect(screen.queryByRole("textbox")).toBeNull();
    expect(screen.queryByRole("form")).toBeNull();
    expect(screen.getByText("线程运行中，内容会自动更新。")).toBeInTheDocument();
  });

  it("shows errors inline", async () => {
    mockDetail([item({ id: "e", type: "error", text: "boom" })]);
    await renderPage();
    expect(screen.getByRole("alert")).toHaveTextContent("boom");
  });

  describe("lazy item detail", () => {
    const cmd = item({
      id: "src:c1",
      type: "command_execution",
      summary: "pnpm test",
      status: "completed",
      detail_available: true,
    });

    it("loads the full command input and output only when expanded", async () => {
      const user = userEvent.setup();
      mockDetail([cmd]);
      itemDetail.mockResolvedValue({
        data: { ...cmd, detail_available: false, text: "$ pnpm test\nFULL-OUTPUT-TAIL" },
      });
      await renderPage();
      expect(itemDetail).not.toHaveBeenCalled();

      await user.click(screen.getByText("pnpm test"));
      expect(await screen.findByText(/FULL-OUTPUT-TAIL/)).toBeInTheDocument();
      expect(itemDetail).toHaveBeenCalledWith("e1", "t1", "src:c1", expect.any(AbortSignal));
    });

    it("shows a loading state, then an error with retry", async () => {
      const user = userEvent.setup();
      mockDetail([cmd]);
      let reject!: (e: Error) => void;
      itemDetail.mockReturnValueOnce(new Promise((_, r) => (reject = r)));
      await renderPage();

      await user.click(screen.getByText("pnpm test"));
      expect(screen.getByRole("status")).toHaveTextContent("加载中");

      await act(async () => reject(new Error("boom")));
      expect(await screen.findByRole("alert")).toHaveTextContent("详情加载失败");

      itemDetail.mockResolvedValueOnce({ data: { ...cmd, text: "RECOVERED" } });
      await user.click(screen.getByRole("button", { name: "重试" }));
      expect(await screen.findByText("RECOVERED")).toBeInTheDocument();
      expect(screen.queryByRole("alert")).toBeNull();
    });

    it("keeps an expanded item's body without re-reading when only the thread updated_at changes", async () => {
      const user = userEvent.setup();
      const running = { ...cmd, status: "running", updated_at: "2026-10-13T09:00:00Z" };
      detail.mockResolvedValue(detailOf([running], "running", "2026-10-13T10:00:00Z"));
      itemDetail.mockResolvedValue({ data: { ...running, text: "OUT-v1" } });
      await renderPage();

      await user.click(screen.getByText("pnpm test"));
      expect(await screen.findByText("OUT-v1")).toBeInTheDocument();

      detail.mockResolvedValue(detailOf([running], "running", "2026-10-13T10:00:05Z"));
      await act(() => client.refetchQueries({ predicate: (q) => q.queryKey[2] === "thread" }));
      expect(detail).toHaveBeenCalledTimes(2);
      expect(screen.getByText("OUT-v1")).toBeInTheDocument();
      expect(itemDetail).toHaveBeenCalledTimes(1);
    });

    it("re-reads an expanded item when its own updated_at changes", async () => {
      const user = userEvent.setup();
      const running = { ...cmd, status: "running", updated_at: "2026-10-13T09:00:00Z" };
      detail.mockResolvedValue(detailOf([running], "running", "2026-10-13T10:00:00Z"));
      itemDetail.mockResolvedValueOnce({ data: { ...running, text: "OUT-v1" } });
      await renderPage();

      await user.click(screen.getByText("pnpm test"));
      expect(await screen.findByText("OUT-v1")).toBeInTheDocument();

      const updated = { ...running, updated_at: "2026-10-13T10:00:05Z" };
      detail.mockResolvedValue(detailOf([updated], "running", "2026-10-13T10:00:05Z"));
      itemDetail.mockResolvedValueOnce({ data: { ...updated, text: "OUT-v2" } });
      await act(() => client.refetchQueries({ predicate: (q) => q.queryKey[2] === "thread" }));
      expect(await screen.findByText("OUT-v2")).toBeInTheDocument();
      expect(itemDetail).toHaveBeenCalledTimes(2);
    });

    it("falls back to the thread updated_at when the item has no updated_at", async () => {
      const user = userEvent.setup();
      const running = { ...cmd, status: "running" };
      detail.mockResolvedValue(detailOf([running], "running", "2026-10-13T10:00:00Z"));
      itemDetail.mockResolvedValueOnce({ data: { ...running, text: "OUT-v1" } });
      await renderPage();

      await user.click(screen.getByText("pnpm test"));
      expect(await screen.findByText("OUT-v1")).toBeInTheDocument();

      detail.mockResolvedValue(detailOf([running], "running", "2026-10-13T10:00:05Z"));
      itemDetail.mockResolvedValueOnce({ data: { ...running, text: "OUT-v2" } });
      await act(() => client.refetchQueries({ predicate: (q) => q.queryKey[2] === "thread" }));
      expect(await screen.findByText("OUT-v2")).toBeInTheDocument();
      expect(itemDetail).toHaveBeenCalledTimes(2);
    });
  });

  it("keeps the stale thread and warns when a refresh fails", async () => {
    const user = userEvent.setup();
    mockDetail([item({ id: "1", type: "assistant_message", text: "still here" })]);
    await renderPage();

    detail.mockRejectedValue(new Error("down"));
    await act(() => client.refetchQueries());
    expect(await screen.findByRole("alert")).toHaveTextContent("刷新失败");
    expect(screen.getByText("still here")).toBeInTheDocument();

    mockDetail([item({ id: "1", type: "assistant_message", text: "fresh" })]);
    await user.click(screen.getByRole("button", { name: "重试" }));
    expect(await screen.findByText("fresh")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("explains a missing thread when the detail request 404s", async () => {
    detail.mockRejectedValue(new ApiRequestError(404, 40418, "T3 线程不存在"));

    render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={["/threads/e1/t1"]}>
          <Routes>
            <Route path="/threads/:environmentId/:threadId" element={<ThreadDetailPage />} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(await screen.findByText("线程不存在，可能已被删除或移动。")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "去重新配对" })).toBeNull();
  });

  it("surfaces the server message and a re-pair link for rejected credentials", async () => {
    detail.mockRejectedValue(
      new ApiRequestError(502, 50202, "T3 环境拒绝凭证（可能已过期或被吊销），请重新配对"),
    );

    render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={["/threads/e1/t1"]}>
          <Routes>
            <Route path="/threads/:environmentId/:threadId" element={<ThreadDetailPage />} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(await screen.findByText(/请重新配对/)).toBeInTheDocument();
    const link = await screen.findByRole("link", { name: "去重新配对" });
    expect(link).toHaveAttribute("href", "/settings/environments");
  });

  it("shows the server message and re-pair link when a refresh fails", async () => {
    mockDetail([item({ id: "1", type: "assistant_message", text: "still here" })]);
    await renderPage();

    detail.mockRejectedValue(
      new ApiRequestError(502, 50202, "T3 环境拒绝凭证（可能已过期或被吊销），请重新配对"),
    );
    await act(() => client.refetchQueries());

    const banner = await screen.findByRole("alert");
    expect(banner).toHaveTextContent("请重新配对");
    expect(banner).toHaveTextContent("可能不是最新内容");
    expect(
      within(banner).getByRole("link", { name: "去重新配对" }),
    ).toHaveAttribute("href", "/settings/environments");
    expect(screen.getByText("still here")).toBeInTheDocument();
  });
});
