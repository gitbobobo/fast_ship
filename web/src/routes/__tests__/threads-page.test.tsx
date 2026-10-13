import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import ThreadsPage from "@/routes/threads/index";
import { signInAs, teardownAuth } from "@/test/auth";
import { renderWithRoute } from "@/test/render";
import { useThreads } from "@/lib/hooks/use-threads";
import { useThreadListStore } from "@/lib/store/thread-list-store";
import type { Environment, ThreadSummary } from "@/lib/api/threads";

vi.mock("@/lib/hooks/use-threads", () => ({ useThreads: vi.fn() }));

const refetch = vi.fn();

function env(over: Partial<Environment> = {}): Environment {
  return {
    id: "e1",
    name: "Mac",
    address: "http://mac:3773",
    status: "ok",
    updated_at: "2026-10-13T10:00:00Z",
    ...over,
  };
}

function thread(i: number, over: Partial<ThreadSummary> = {}): ThreadSummary {
  return {
    id: `t${i}`,
    environment_id: "e1",
    environment_name: "Mac",
    project_id: "p1",
    project_name: "Alpha",
    title: `Thread ${i}`,
    status: "idle",
    branch: "feature/x",
    provider: "codex",
    model: "gpt-5",
    updated_at: new Date(Date.UTC(2026, 9, 1, 0, i)).toISOString(),
    pull_request_count: 0,
    ...over,
  };
}

function mockThreads(items: ThreadSummary[], environments: Environment[] = [env()]) {
  vi.mocked(useThreads).mockReturnValue({
    data: { items, environments },
    isLoading: false,
    isFetching: false,
    refetch,
  } as unknown as ReturnType<typeof useThreads>);
}

function renderPage() {
  return renderWithRoute(<ThreadsPage />, { path: "/threads", initialEntry: "/threads" });
}

function cardTitles() {
  return screen.queryAllByTestId("thread-card").map((c) => within(c).getByRole("heading").textContent);
}

describe("ThreadsPage", () => {
  beforeEach(() => {
    useThreadListStore.getState().resetFilters();
    vi.clearAllMocks();
    signInAs("u1");
  });

  afterEach(teardownAuth);

  it("guides to environment setup when nothing is paired", () => {
    mockThreads([], []);
    renderPage();
    expect(screen.getByText("还没有连接环境")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /去设置配对/ })).toHaveAttribute(
      "href",
      "/settings/environments",
    );
  });

  it("shows card facts without a message preview, waiting first, archived hidden", () => {
    mockThreads([
      thread(1),
      thread(2, { status: "waiting", pull_request_count: 2, title: "Needs approval" }),
      thread(3, { status: "archived" }),
      thread(4, { status: "running" }),
    ]);
    renderPage();

    expect(cardTitles()).toEqual(["Needs approval", "Thread 4", "Thread 1"]);
    const first = screen.getAllByTestId("thread-card")[0];
    expect(first).toHaveAttribute("href", "/threads/e1/t2");
    expect(within(first).getByText("等待输入或审批")).toBeInTheDocument();
    expect(within(first).getByText("Mac · Alpha")).toBeInTheDocument();
    expect(within(first).getByText("feature/x")).toBeInTheDocument();
    expect(within(first).getByText("codex / gpt-5")).toBeInTheDocument();
    expect(within(first).getByLabelText("2 个 PR")).toBeInTheDocument();
    expect(screen.getByText("运行中")).toBeInTheDocument();
  });

  it("filters by title, project, status and environment, and clears", async () => {
    const user = userEvent.setup();
    mockThreads(
      [
        thread(1, { title: "Login bug" }),
        thread(2, { title: "Billing", project_id: "p2", project_name: "Beta", status: "running" }),
        thread(3, { title: "Old", status: "archived" }),
        thread(4, { title: "Remote job", environment_id: "e2", environment_name: "Linux" }),
      ],
      [env(), env({ id: "e2", name: "Linux" })],
    );
    renderPage();
    expect(cardTitles()).toHaveLength(3);

    await user.type(screen.getByLabelText("搜索标题"), "login");
    expect(cardTitles()).toEqual(["Login bug"]);
    await user.click(screen.getByRole("button", { name: "清除筛选" }));
    expect(cardTitles()).toHaveLength(3);

    await user.click(screen.getByRole("combobox", { name: "筛选状态" }));
    await user.click(await screen.findByRole("option", { name: "已归档" }));
    expect(cardTitles()).toEqual(["Old"]);
    await user.click(screen.getByRole("button", { name: "清除筛选" }));

    await user.click(screen.getByRole("combobox", { name: "筛选项目" }));
    await user.click(await screen.findByRole("option", { name: "Beta" }));
    expect(cardTitles()).toEqual(["Billing"]);
    await user.click(screen.getByRole("button", { name: "清除筛选" }));

    await user.click(screen.getByRole("button", { name: "筛选环境" }));
    await user.click(await screen.findByRole("menuitemcheckbox", { name: "Linux" }));
    expect(cardTitles()).toEqual(["Remote job"]);
    await user.click(screen.getByRole("menuitemcheckbox", { name: "Mac" }));
    expect(cardTitles()).toHaveLength(3);
  });

  it("selects a specific project when the same name exists in two environments", async () => {
    const user = userEvent.setup();
    mockThreads(
      [
        thread(1, { title: "Mac work" }),
        thread(2, { title: "Linux work", environment_id: "e2", environment_name: "Linux" }),
      ],
      [env(), env({ id: "e2", name: "Linux" })],
    );
    renderPage();

    await user.click(screen.getByRole("combobox", { name: "筛选项目" }));
    expect(await screen.findByRole("option", { name: "Alpha · Mac" })).toBeInTheDocument();
    await user.click(screen.getByRole("option", { name: "Alpha · Linux" }));
    expect(cardTitles()).toEqual(["Linux work"]);
    expect(screen.getByRole("combobox", { name: "筛选项目" })).toHaveTextContent("Alpha · Linux");
  });

  it("paginates 60 at a time and keeps the loaded count in the store", async () => {
    const user = userEvent.setup();
    mockThreads(Array.from({ length: 130 }, (_, i) => thread(i)));
    const { unmount } = renderPage();

    expect(cardTitles()).toHaveLength(60);
    await user.click(screen.getByRole("button", { name: /加载更多/ }));
    expect(cardTitles()).toHaveLength(120);
    unmount();

    renderPage();
    expect(cardTitles()).toHaveLength(120);
    await user.click(screen.getByRole("button", { name: /加载更多（还有 10 条）/ }));
    expect(cardTitles()).toHaveLength(130);
    expect(screen.queryByRole("button", { name: /加载更多/ })).toBeNull();
  });

  it("restores filters after leaving and coming back", async () => {
    const user = userEvent.setup();
    mockThreads([thread(1, { title: "Login bug" }), thread(2)]);
    const { unmount } = renderPage();
    await user.type(screen.getByLabelText("搜索标题"), "login");
    unmount();

    renderPage();
    expect(screen.getByLabelText("搜索标题")).toHaveValue("login");
    expect(cardTitles()).toEqual(["Login bug"]);
  });

  it("keeps healthy environments visible and flags the failing ones", () => {
    mockThreads(
      [thread(1)],
      [
        env(),
        env({ id: "e2", name: "Linux", status: "unreachable", error: "connection refused" }),
        env({ id: "e3", name: "Office", status: "repair" }),
      ],
    );
    renderPage();

    expect(cardTitles()).toEqual(["Thread 1"]);
    const alert = screen.getByRole("alert");
    const rows = within(alert).getAllByRole("listitem");
    expect(rows).toHaveLength(2);
    expect(rows[0]).toHaveTextContent("Linux — 不可达（connection refused）");
    expect(rows[1]).toHaveTextContent("Office — 需重新配对");
    expect(within(alert).queryByText(/Mac/)).toBeNull();
  });

  it("guides re-pairing instead of an empty list when every environment is down", () => {
    mockThreads([], [env({ status: "repair" })]);
    renderPage();

    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("所有环境都无法读取");
    expect(screen.queryByText("暂无线程")).toBeNull();
    expect(screen.getByRole("link", { name: "去重新配对" })).toHaveAttribute(
      "href",
      "/settings/environments",
    );
  });

  it("does not call a partially readable environment down", () => {
    mockThreads(
      [thread(1)],
      [
        env({ status: "degraded", error: "归档线程暂不可读" }),
        env({ id: "e2", name: "测试", status: "unreachable" }),
      ],
    );
    renderPage();

    expect(screen.getByRole("alert")).toHaveTextContent(
      "部分环境暂时无法读取",
    );
    expect(screen.queryByText("所有环境都无法读取")).toBeNull();
    expect(cardTitles()).toEqual(["Thread 1"]);
  });

  it("refreshes manually", async () => {
    const user = userEvent.setup();
    mockThreads([thread(1)]);
    renderPage();
    await user.click(screen.getByRole("button", { name: "刷新线程" }));
    expect(refetch).toHaveBeenCalledTimes(1);
  });
});
