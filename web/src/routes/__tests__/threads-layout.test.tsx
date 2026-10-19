import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import ThreadsLayout from "@/routes/threads/layout";
import ThreadsPage from "@/routes/threads/index";
import { signInAs, teardownAuth } from "@/test/auth";
import { withTestQueryClient } from "@/test/render";
import { makeEnvironment } from "@/test/threads";
import { useThreads } from "@/lib/hooks/use-threads";
import { useThreadListStore } from "@/lib/store/thread-list-store";
import type { Environment, ThreadSummary } from "@/lib/api/threads";

vi.mock("@/lib/hooks/use-threads", () => ({ useThreads: vi.fn() }));

const refetch = vi.fn();

const env = makeEnvironment;

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
    settled: false,
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

// 与 App.tsx 同构的嵌套路由：布局挂 index 与详情子路由；详情用占位元素，
// 不触发真实详情接口。
function renderPage(initialEntry = "/threads") {
  return render(
    withTestQueryClient(
      <MemoryRouter initialEntries={[initialEntry]}>
        <Routes>
          <Route path="/threads" element={<ThreadsLayout />}>
            <Route index element={<ThreadsPage />} />
            <Route
              path=":environmentId/:threadId"
              element={<div data-testid="thread-detail-slot">详情占位</div>}
            />
          </Route>
        </Routes>
      </MemoryRouter>,
    ),
  );
}

/** 项目/状态/完结三个筛选收在「筛选线程」按钮后面。 */
async function openFilters(user: ReturnType<typeof userEvent.setup>) {
  const button = screen.getByRole("button", { name: /^筛选线程/ });
  if (button.getAttribute("aria-expanded") !== "true") {
    await user.click(button);
  }
}

function cardTitles() {
  return screen.queryAllByTestId("thread-card").map((c) => within(c).getByRole("heading").textContent);
}

describe("ThreadsLayout", () => {
  beforeEach(() => {
    useThreadListStore.getState().resetFilters();
    useThreadListStore.getState().setSettledOpen(false);
    vi.clearAllMocks();
    signInAs("u1");
  });

  afterEach(teardownAuth);

  it("offers 新建线程 as the entry to the composer page", () => {
    mockThreads([thread(1)]);
    renderPage();
    expect(screen.getByRole("link", { name: /新建线程/ })).toHaveAttribute("href", "/threads/new");
  });

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
    expect(within(first).getByText("等待")).toBeInTheDocument();
    expect(within(first).getByText("Alpha")).toBeInTheDocument();
    expect(within(first).getByText("feature/x")).toBeInTheDocument();
    expect(within(first).getByLabelText("2 个 PR")).toBeInTheDocument();
    expect(screen.getByText("运行中")).toBeInTheDocument();
  });

  it("points a thread row at /threads/{env}/{id} so it opens in the right column", () => {
    mockThreads([thread(1), thread(2, { environment_id: "e2" })]);
    renderPage();
    const hrefs = screen
      .getAllByTestId("thread-card")
      .map((c) => c.getAttribute("href"));
    expect(hrefs).toContain("/threads/e1/t1");
    expect(hrefs).toContain("/threads/e2/t2");
  });

  it("shows the empty hint in the right column when no thread is selected", () => {
    mockThreads([thread(1)]);
    renderPage();
    expect(
      screen.getByText("从左侧选择一个线程查看详情"),
    ).toBeInTheDocument();
    expect(screen.queryByTestId("thread-detail-slot")).toBeNull();
  });

  it("renders the child detail route in the right column without dropping the list", () => {
    mockThreads([thread(1), thread(2)]);
    renderPage("/threads/e1/t1");
    expect(screen.getByTestId("thread-detail-slot")).toBeInTheDocument();
    const rows = screen.getAllByTestId("thread-card");
    expect(rows).toHaveLength(2);
    // 当前打开的线程在左栏高亮，其余不高亮（列表按更新时间倒序，不能按位置取）。
    const current = rows.find((row) => row.getAttribute("href") === "/threads/e1/t1");
    const other = rows.find((row) => row.getAttribute("href") === "/threads/e1/t2");
    expect(current).toHaveAttribute("aria-current", "page");
    expect(current).toHaveClass("bg-accent");
    expect(other).not.toHaveAttribute("aria-current");
  });

  it("switches only the right column when a row is clicked and refreshes the list once", async () => {
    const user = userEvent.setup();
    mockThreads([thread(1), thread(2)]);
    renderPage();
    const list = screen.getByTestId("thread-grid");
    const scroll = screen.getByTestId("thread-list-scroll");

    // 列表按更新时间倒序：第一行是 t2。
    await user.click(screen.getAllByTestId("thread-card")[0]);

    expect(await screen.findByTestId("thread-detail-slot")).toBeInTheDocument();
    // 左栏不重挂载：仍是同一批 DOM 节点，滚动容器也没换。
    expect(screen.getByTestId("thread-grid")).toBe(list);
    expect(screen.getByTestId("thread-list-scroll")).toBe(scroll);
    const current = screen
      .getAllByTestId("thread-card")
      .find((row) => row.getAttribute("aria-current") === "page");
    expect(current).toHaveAttribute("href", "/threads/e1/t2");
    expect(refetch).toHaveBeenCalledTimes(1);
  });

  it("keeps project/status/settled behind the filter toggle", async () => {
    const user = userEvent.setup();
    mockThreads([thread(1)]);
    renderPage();

    expect(screen.queryByRole("combobox", { name: "筛选状态" })).toBeNull();
    const toggle = screen.getByRole("button", { name: "筛选线程" });
    expect(toggle).toHaveAttribute("aria-expanded", "false");

    await user.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    await user.click(screen.getByRole("combobox", { name: "筛选状态" }));
    await user.click(await screen.findByRole("option", { name: "运行中" }));

    // 收起面板后，生效项数要能被读屏读到（不靠视觉小点）。
    await user.click(toggle);
    expect(screen.queryByRole("combobox", { name: "筛选状态" })).toBeNull();
    expect(
      screen.getByRole("button", { name: "筛选线程（1 项生效）" }),
    ).toBeInTheDocument();
  });

  it("keeps the thread list in its own scroll container", () => {
    mockThreads([thread(1)]);
    renderPage();
    expect(screen.getByTestId("thread-list-scroll")).toHaveClass(
      "min-h-0",
      "flex-1",
      "overflow-y-auto",
    );
  });

  it("shows the run time on running cards when the start is known", () => {
    mockThreads([
      thread(1, {
        status: "running",
        run_started_at: new Date(Date.now() - 42 * 60_000 - 5_000).toISOString(),
      }),
      thread(2, { status: "running" }),
    ]);
    renderPage();
    expect(screen.getByText("运行中 · 42 分钟")).toBeInTheDocument();
    expect(screen.getByText("运行中")).toBeInTheDocument();
  });

  it("dims settled cards and filters them by completion", async () => {
    const user = userEvent.setup();
    mockThreads([
      thread(1, { title: "Open work" }),
      thread(2, { title: "Done work", settled: true }),
      thread(3, { title: "Done archived", settled: true, status: "archived" }),
    ]);
    renderPage();
    // 默认视图：已完结收进底部折叠区，展开后才出现且排在未完结之后。
    expect(cardTitles()).toEqual(["Open work"]);
    await user.click(
      screen.getByRole("button", { name: "已完结线程（1 条）" }),
    );
    expect(cardTitles()).toEqual(["Open work", "已完结，Done work"]);
    const settledCard = screen.getAllByTestId("thread-card")[1];
    expect(settledCard).toHaveAttribute("data-settled", "true");
    expect(screen.getAllByTestId("thread-card")[0]).not.toHaveAttribute("data-settled");

    await openFilters(user);
    await user.click(screen.getByRole("combobox", { name: "筛选完结状态" }));
    await user.click(await screen.findByRole("option", { name: "未完结" }));
    expect(cardTitles()).toEqual(["Open work"]);

    await user.click(screen.getByRole("combobox", { name: "筛选完结状态" }));
    await user.click(await screen.findByRole("option", { name: "已完结" }));
    // 显式选了完结筛选 → 平铺列表，没有折叠条。
    expect(cardTitles()).toEqual(["已完结，Done work"]);
    expect(
      screen.queryByRole("button", { name: /已完结线程/ }),
    ).toBeNull();

    await user.click(screen.getByRole("combobox", { name: "筛选状态" }));
    await user.click(await screen.findByRole("option", { name: "已归档" }));
    expect(cardTitles()).toEqual(["已完结，Done archived"]);
    expect(
      screen.queryByRole("button", { name: /已完结线程/ }),
    ).toBeNull();

    // 清除筛选回到默认视图；折叠区保持之前的展开状态。
    await user.click(screen.getByRole("button", { name: "清除筛选" }));
    expect(cardTitles()).toEqual(["Open work", "已完结，Done work"]);
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

    await openFilters(user);
    await user.click(screen.getByRole("combobox", { name: "筛选状态" }));
    await user.click(await screen.findByRole("option", { name: "已归档" }));
    expect(cardTitles()).toEqual(["Old"]);
    await user.click(screen.getByRole("button", { name: "清除筛选" }));

    await user.click(screen.getByRole("combobox", { name: "筛选项目" }));
    await user.click(await screen.findByRole("option", { name: "Beta" }));
    expect(cardTitles()).toEqual(["Billing"]);
    await user.click(screen.getByRole("button", { name: "清除筛选" }));

    await user.click(screen.getByRole("button", { name: /筛选环境：Linux/ }));
    expect(cardTitles()).toEqual(["Remote job"]);
    await user.click(screen.getByRole("button", { name: /筛选环境：全部环境/ }));
    expect(cardTitles()).toHaveLength(3);
  });

  it("counts threads per environment, ignores the env selection, and tracks other filters", async () => {
    const user = userEvent.setup();
    mockThreads(
      [
        thread(1),
        thread(2),
        thread(3, { title: "Remote job", environment_id: "e2", environment_name: "Linux" }),
        thread(4, { title: "Remote run", environment_id: "e2", environment_name: "Linux", status: "running" }),
      ],
      [env(), env({ id: "e2", name: "Linux" })],
    );
    renderPage();

    expect(
      screen.getByRole("button", {
        name: "筛选环境：全部环境（运行中 1、空闲 3 个线程）",
      }),
    ).toHaveAttribute("aria-pressed", "true");
    expect(
      screen.getByRole("button", { name: "筛选环境：Mac（空闲 2 个线程）" }),
    ).toHaveAttribute("aria-pressed", "false");
    expect(
      screen.getByRole("button", {
        name: "筛选环境：Linux（运行中 1、空闲 1 个线程）",
      }),
    ).toHaveAttribute("aria-pressed", "false");

    // 选中环境后其它环境的计数不变（分面计数忽略环境这一项）。
    await user.click(screen.getByRole("button", { name: /筛选环境：Linux/ }));
    expect(cardTitles()).toEqual(["Remote run", "Remote job"]);
    expect(
      screen.getByRole("button", { name: "筛选环境：Mac（空闲 2 个线程）" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", {
        name: /筛选环境：Linux（运行中 1、空闲 1 个线程）/,
      }),
    ).toHaveAttribute("aria-pressed", "true");
    expect(
      screen.getByRole("button", { name: /筛选环境：全部环境/ }),
    ).toHaveAttribute("aria-pressed", "false");

    // 搜索与状态筛选叠加进计数。
    await user.type(screen.getByLabelText("搜索标题"), "run");
    expect(
      screen.getByRole("button", {
        name: "筛选环境：全部环境（运行中 1 个线程）",
      }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "筛选环境：Mac（0 个线程）" }),
    ).toBeInTheDocument();
    await openFilters(user);
    await user.click(screen.getByRole("combobox", { name: "筛选状态" }));
    await user.click(await screen.findByRole("option", { name: "运行中" }));
    expect(
      screen.getByRole("button", {
        name: "筛选环境：Linux（运行中 1 个线程）",
      }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", {
        name: "筛选环境：全部环境（运行中 1 个线程）",
      }),
    ).toBeInTheDocument();
  });

  it("shows per-status count pills on environment rows and hides zero buckets", () => {
    mockThreads(
      [
        thread(1, { status: "running" }),
        thread(2, { status: "waiting" }),
        thread(3, { environment_id: "e2", environment_name: "Linux" }),
      ],
      [env(), env({ id: "e2", name: "Linux" })],
    );
    renderPage();

    const mac = screen.getByRole("button", {
      name: "筛选环境：Mac（运行中 1、等待 1 个线程）",
    });
    expect(within(mac).getByTitle("运行中 1")).toBeInTheDocument();
    expect(within(mac).getByTitle("等待 1")).toBeInTheDocument();
    expect(within(mac).queryByTitle(/^空闲/)).toBeNull();
    expect(within(mac).queryByTitle(/^已归档/)).toBeNull();

    const linux = screen.getByRole("button", {
      name: "筛选环境：Linux（空闲 1 个线程）",
    });
    expect(within(linux).getByTitle("空闲 1")).toBeInTheDocument();

    const all = screen.getByRole("button", {
      name: "筛选环境：全部环境（运行中 1、等待 1、空闲 1 个线程）",
    });
    expect(within(all).getByTitle("运行中 1")).toBeInTheDocument();
    expect(within(all).queryByTitle(/^已归档/)).toBeNull();
  });

  it("keeps settled threads behind a collapsed bar in the default view", async () => {
    const user = userEvent.setup();
    mockThreads([
      thread(1, { title: "Open work" }),
      thread(2, { title: "Done A", settled: true }),
      thread(3, { title: "Done B", settled: true }),
    ]);
    renderPage();

    // 收起时只有未完结行；折叠条显示已完结匹配总数。
    expect(cardTitles()).toEqual(["Open work"]);
    const bar = screen.getByRole("button", {
      name: "已完结线程（2 条）",
    });
    expect(bar).toHaveAttribute("aria-expanded", "false");
    expect(bar).toHaveTextContent("已完结 · 2");

    // 展开：已完结行接在未完结行后面。
    await user.click(bar);
    expect(
      screen.getByRole("button", { name: "已完结线程（2 条）" }),
    ).toHaveAttribute("aria-expanded", "true");
    expect(cardTitles()).toEqual([
      "Open work",
      "已完结，Done B",
      "已完结，Done A",
    ]);

    // 再点收起。
    await user.click(
      screen.getByRole("button", { name: "已完结线程（2 条）" }),
    );
    expect(cardTitles()).toEqual(["Open work"]);
  });

  it("paginates only unresolved rows and renders all settled rows when expanded", async () => {
    const user = userEvent.setup();
    mockThreads([
      ...Array.from({ length: 70 }, (_, i) => thread(i)),
      thread(200, { title: "Done A", settled: true }),
      thread(201, { title: "Done B", settled: true }),
      thread(202, { title: "Done C", settled: true }),
    ]);
    renderPage();

    // 「加载更多」的剩余数只按未完结计（70 - 60，不含已完结 3 条）。
    expect(cardTitles()).toHaveLength(60);
    const bar = screen.getByRole("button", {
      name: "已完结线程（3 条）",
    });
    await user.click(
      screen.getByRole("button", { name: "加载更多（还有 10 条）" }),
    );
    expect(cardTitles()).toHaveLength(70);
    expect(screen.queryByRole("button", { name: /加载更多/ })).toBeNull();

    // 展开后已完结行一次性全部渲染，不再分页。
    await user.click(bar);
    expect(cardTitles()).toHaveLength(73);
    expect(cardTitles().slice(-3)).toEqual([
      "已完结，Done C",
      "已完结，Done B",
      "已完结，Done A",
    ]);
  });

  it("shows only the collapse bar when every matched thread is settled", async () => {
    const user = userEvent.setup();
    mockThreads([
      thread(1, { title: "Done A", settled: true }),
      thread(2, { title: "Done B", settled: true }),
    ]);
    renderPage();

    expect(screen.queryByText("暂无线程")).toBeNull();
    expect(cardTitles()).toEqual([]);
    await user.click(
      screen.getByRole("button", { name: "已完结线程（2 条）" }),
    );
    expect(cardTitles()).toEqual(["已完结，Done B", "已完结，Done A"]);
  });

  it("keeps unhealthy environments selectable in the rail and marks their status", async () => {
    const user = userEvent.setup();
    mockThreads(
      [thread(1)],
      [
        env(),
        env({ id: "e2", name: "Linux", status: "unreachable", error: "connection refused" }),
      ],
    );
    renderPage();

    expect(screen.getByRole("alert")).toHaveTextContent(
      "部分环境正在同步或暂时无法读取",
    );
    const linux = screen.getByRole("button", { name: "筛选环境：Linux（0 个线程）" });
    expect(within(linux).getByRole("img", { name: "不可达" })).toBeInTheDocument();

    await user.click(linux);
    expect(linux).toHaveAttribute("aria-pressed", "true");
    expect(cardTitles()).toEqual([]);
    expect(screen.getByText("没有符合筛选条件的线程")).toBeInTheDocument();
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

    await openFilters(user);
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

    // 改筛选会回到第一页的 60 条，不会带着旧 limit。
    await user.type(screen.getByLabelText("搜索标题"), "Thread 129");
    expect(cardTitles()).toEqual(["Thread 129"]);
    await user.clear(screen.getByLabelText("搜索标题"));
    expect(cardTitles()).toHaveLength(60);
  }, 20000);

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
      "部分环境正在同步或暂时无法读取",
    );
    expect(screen.queryByText("所有环境都无法读取")).toBeNull();
    expect(cardTitles()).toEqual(["Thread 1"]);
  });

  it("does not claim there are no threads while an environment is still syncing", () => {
    mockThreads([], [env({ status: "degraded", error: "正在同步 T3 线程快照" })]);
    renderPage();

    expect(screen.getByText("环境尚未同步完成，稍后会自动显示线程。")).toBeInTheDocument();
    expect(screen.queryByText("暂无线程")).toBeNull();
  });

  it("refreshes manually", async () => {
    const user = userEvent.setup();
    mockThreads([thread(1)]);
    renderPage();
    await user.click(screen.getByRole("button", { name: "刷新线程" }));
    expect(refetch).toHaveBeenCalledTimes(1);
  });
});
