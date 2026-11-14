import { act, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useParams, useLocation } from "react-router";
import { describe, expect, it, vi } from "vitest";
import { useIssueSourceList } from "./use-issue-source";

const { useInfiniteIssueListMock } = vi.hoisted(() => ({
  useInfiniteIssueListMock: vi.fn(),
}));

vi.mock("@/lib/hooks/use-issues", () => ({
  useInfiniteIssueList: (...args: unknown[]) =>
    useInfiniteIssueListMock(...args),
}));

function makeIssue(id: string, reference: string, title: string): Issue {
  return {
    id,
    project_id: "proj-1",
    source: "github",
    sequence_number: 1,
    reference,
    state: "open",
    state_reason: "",
    title,
    body: "",
    body_html: "",
    author: { login: "alice", avatar_url: "" },
    unread_comments_count: 0,
    closed_at: null,
    created_at: "2026-04-10T10:00:00Z",
    updated_at: "2026-04-12T10:00:00Z",
  };
}

function page(items: Issue[], total = items.length, pageNumber = 1) {
  return { items, total, page: pageNumber, page_size: 20 };
}

function mockQuery(
  pages: ReturnType<typeof page>[],
  overrides: Partial<Record<string, unknown>> = {},
) {
  useInfiniteIssueListMock.mockReset();
  useInfiniteIssueListMock.mockReturnValue({
    data: pages.length > 0 ? { pages } : undefined,
    isLoading: false,
    isError: false,
    refetch: vi.fn(),
    fetchNextPage: vi.fn(),
    hasNextPage: false,
    isFetching: false,
    isFetchingNextPage: false,
    ...overrides,
  });
}

const BOARD_CONTEXT = {
  kind: "board",
  workflowStatus: "todo",
  label: "",
  source: "",
} as const;

/** 渲染 hook 并暴露当前 location 与动作按钮；currentIssueId 跟随路由 iid，可用 currentIssueIdOverride 覆盖 */
function Harness(props: {
  projectId?: string;
  context?: unknown;
  currentIssueIdOverride?: string;
  currentIssueProjectId?: string;
}) {
  const { iid } = useParams();
  const source = useIssueSourceList({
    projectId: props.projectId ?? "proj-1",
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    context: (props.context ?? BOARD_CONTEXT) as any,
    currentIssueId: props.currentIssueIdOverride ?? iid ?? "",
    currentIssueProjectId: props.currentIssueProjectId,
  });
  const location = useLocation();
  return (
    <div>
      <span data-testid="location">{location.pathname + location.search}</span>
      <span data-testid="count">{source.items.length}</span>
      <span data-testid="current-index">{source.currentIndex}</span>
      <span data-testid="advancing">{String(source.isAdvancing)}</span>
      <ul data-testid="list">
        {source.items.map((entry) => (
          <li
            key={entry.issue.id}
            data-testid={`entry-${entry.issue.id}`}
            data-stale={entry.stale ? "true" : undefined}
          >
            {entry.issue.reference}
          </li>
        ))}
      </ul>
      <button type="button" onClick={source.goToNext}>
        next
      </button>
      <button type="button" onClick={source.goToPrevious}>
        prev
      </button>
    </div>
  );
}

function harnessElement(props: {
  projectId?: string;
  context?: unknown;
  currentIssueIdOverride?: string;
  currentIssueProjectId?: string;
}) {
  return (
    <Routes>
      <Route
        path="/projects/:id/issues/:iid"
        element={<Harness {...props} />}
      />
    </Routes>
  );
}

function renderHarness(
  initialEntry: string,
  props: {
    projectId?: string;
    context?: unknown;
    currentIssueIdOverride?: string;
    currentIssueProjectId?: string;
  },
) {
  return render(
    <MemoryRouter initialEntries={[initialEntry]}>
      {harnessElement(props)}
    </MemoryRouter>,
  );
}

/** 按渲染顺序返回列表条目的 data-testid，用于断言 J/K 顺序 */
function entryIds(): string[] {
  return Array.from(screen.getByTestId("list").children).map(
    (element) => element.getAttribute("data-testid") ?? "",
  );
}

describe("useIssueSourceList", () => {
  it("disables the query without source context", () => {
    mockQuery([]);
    renderHarness("/projects/proj-1/issues/issue-1", {
      context: { kind: "none" },
      currentIssueIdOverride: "issue-1",
    });

    expect(useInfiniteIssueListMock).toHaveBeenCalledWith(
      "proj-1",
      {},
      { enabled: false },
    );
    expect(screen.getByTestId("count")).toHaveTextContent("0");
  });

  it("builds board filters with fixed open state", () => {
    mockQuery([page([makeIssue("issue-1", "GH-1", "A")])]);
    renderHarness("/projects/proj-1/issues/issue-1", {
      context: {
        kind: "board",
        workflowStatus: "unset",
        label: "bug",
        source: "github",
      },
      currentIssueIdOverride: "issue-1",
    });

    expect(useInfiniteIssueListMock).toHaveBeenCalledWith(
      "proj-1",
      {
        state: "open",
        label: "bug",
        source: "github",
        workflow_status: "unset",
      },
      { enabled: true },
    );
  });

  it("auto-fetches pages until the current issue appears", async () => {
    mockQuery([page([makeIssue("issue-0", "GH-0", "A")])], {
      hasNextPage: true,
    });
    const fetchNextPage = vi.fn();
    useInfiniteIssueListMock.mockReturnValue({
      data: { pages: [page([makeIssue("issue-0", "GH-0", "A")])] },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
      fetchNextPage,
      hasNextPage: true,
      isFetching: false,
      isFetchingNextPage: false,
    });

    const { rerender } = renderHarness("/projects/proj-1/issues/issue-9", {
      currentIssueIdOverride: "issue-9",
    });

    // 当前项不在已加载页时触发一次补拉
    await waitFor(() => expect(fetchNextPage).toHaveBeenCalled());

    // 补拉返回后当前项出现：不再继续补拉，位置正确
    useInfiniteIssueListMock.mockReturnValue({
      data: {
        pages: [
          page([makeIssue("issue-0", "GH-0", "A")]),
          page([makeIssue("issue-9", "GH-9", "B")], 2, 2),
        ],
      },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
      fetchNextPage,
      hasNextPage: false,
      isFetching: false,
      isFetchingNextPage: false,
    });
    rerender(
      <MemoryRouter initialEntries={["/projects/proj-1/issues/issue-9"]}>
        {harnessElement({})}
      </MemoryRouter>,
    );

    await waitFor(() =>
      expect(screen.getByTestId("entry-issue-9")).toBeInTheDocument(),
    );
    expect(screen.getByTestId("current-index")).toHaveTextContent("1");
  });

  it("navigates with replace keeping board params and clearing page params", async () => {
    mockQuery([
      page([
        makeIssue("issue-1", "GH-1", "A"),
        makeIssue("issue-2", "GH-2", "B"),
      ]),
    ]);

    renderHarness(
      "/projects/proj-1/issues/issue-1?issue_from=board&issue_workflow_status=todo",
      {},
    );

    act(() => {
      screen.getByRole("button", { name: "next" }).click();
    });

    await waitFor(() =>
      expect(screen.getByTestId("location")).toHaveTextContent(
        "/projects/proj-1/issues/issue-2?issue_from=board&issue_workflow_status=todo",
      ),
    );
    // 翻页后当前项随路由移动
    expect(screen.getByTestId("current-index")).toHaveTextContent("1");

    act(() => {
      screen.getByRole("button", { name: "prev" }).click();
    });
    await waitFor(() =>
      expect(screen.getByTestId("location")).toHaveTextContent(
        "/projects/proj-1/issues/issue-1?issue_from=board&issue_workflow_status=todo",
      ),
    );
  });

  it("updates issue_page to the target page for list context", async () => {
    const issue3 = makeIssue("issue-3", "GH-3", "C");
    mockQuery([
      page([makeIssue("issue-1", "GH-1", "A"), makeIssue("issue-2", "GH-2", "B")]),
      page([issue3], 3, 2),
    ]);

    renderHarness(
      "/projects/proj-1/issues/issue-1?issue_state=open&issue_sort=updated_desc&issue_page=1",
      {
        context: {
          kind: "list",
          state: "open",
          q: "",
          label: "",
          source: "",
          workflowStatus: "",
          sort: "updated_desc",
          page: 1,
        },
      },
    );

    act(() => {
      screen.getByRole("button", { name: "next" }).click();
    });
    await waitFor(() =>
      expect(screen.getByTestId("location")).toHaveTextContent("issues/issue-2"),
    );

    act(() => {
      screen.getByRole("button", { name: "next" }).click();
    });
    // issue_page 跟随目标所在页（issue-3 在第 2 页）
    await waitFor(() =>
      expect(screen.getByTestId("location")).toHaveTextContent(
        "issue_state=open&issue_sort=updated_desc&issue_page=2",
      ),
    );
  });

  it("fetches the next page then advances from the last item", async () => {
    const fetchNextPage = vi.fn();
    useInfiniteIssueListMock.mockReturnValue({
      data: { pages: [page([makeIssue("issue-1", "GH-1", "A")])] },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
      fetchNextPage,
      hasNextPage: true,
      isFetching: false,
      isFetchingNextPage: false,
    });

    const view = renderHarness("/projects/proj-1/issues/issue-1", {
      currentIssueIdOverride: "issue-1",
    });

    // 末项前进：先标记补拉中，防重复跳转
    act(() => {
      screen.getByRole("button", { name: "next" }).click();
    });
    expect(fetchNextPage).toHaveBeenCalledTimes(1);
    expect(screen.getByTestId("advancing")).toHaveTextContent("true");

    // 再按 J 不应重复补拉
    act(() => {
      screen.getByRole("button", { name: "next" }).click();
    });
    expect(fetchNextPage).toHaveBeenCalledTimes(1);

    // 补拉完成、新页包含下一条：自动前进
    useInfiniteIssueListMock.mockReturnValue({
      data: {
        pages: [
          page([makeIssue("issue-1", "GH-1", "A")]),
          page([makeIssue("issue-2", "GH-2", "B")], 2, 2),
        ],
      },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
      fetchNextPage,
      hasNextPage: false,
      isFetching: false,
      isFetchingNextPage: false,
    });
    view.rerender(
      <MemoryRouter initialEntries={["/projects/proj-1/issues/issue-1"]}>
        {harnessElement({ currentIssueIdOverride: "issue-1" })}
      </MemoryRouter>,
    );

    await waitFor(() =>
      expect(screen.getByTestId("location")).toHaveTextContent(
        "/projects/proj-1/issues/issue-2",
      ),
    );
    expect(screen.getByTestId("advancing")).toHaveTextContent("false");
  });

  it("keeps the current issue in place once the query drops it", async () => {
    mockQuery([
      page([
        makeIssue("issue-1", "GH-1", "A"),
        makeIssue("issue-2", "GH-2", "B"),
        makeIssue("issue-3", "GH-3", "C"),
      ]),
    ]);

    const view = renderHarness("/projects/proj-1/issues/issue-2", {
      currentIssueIdOverride: "issue-2",
    });
    expect(screen.getByTestId("current-index")).toHaveTextContent("1");

    // 查询移出 issue-2：原位置保留并标记 stale
    mockQuery([
      page([
        makeIssue("issue-1", "GH-1", "A"),
        makeIssue("issue-3", "GH-3", "C"),
      ]),
    ]);
    view.rerender(
      <MemoryRouter initialEntries={["/projects/proj-1/issues/issue-2"]}>
        <Routes>
          <Route
            path="/projects/:id/issues/:iid"
            element={<Harness currentIssueIdOverride="issue-2" />}
          />
        </Routes>
      </MemoryRouter>,
    );

    expect(screen.getByTestId("current-index")).toHaveTextContent("1");
    expect(screen.getByTestId("entry-issue-2")).toHaveAttribute(
      "data-stale",
      "true",
    );
  });

  it("keeps retained items across iid switches so K can return to them", async () => {
    mockQuery([
      page([
        makeIssue("issue-1", "GH-1", "A"),
        makeIssue("issue-2", "GH-2", "B"),
        makeIssue("issue-3", "GH-3", "C"),
      ]),
    ]);

    const view = renderHarness("/projects/proj-1/issues/issue-2", {});

    // 查询移出 issue-2：按原位置保留为 stale
    mockQuery([
      page([
        makeIssue("issue-1", "GH-1", "A"),
        makeIssue("issue-3", "GH-3", "C"),
      ]),
    ]);
    view.rerender(
      <MemoryRouter initialEntries={["/projects/proj-1/issues/issue-2"]}>
        {harnessElement({})}
      </MemoryRouter>,
    );
    expect(screen.getByTestId("entry-issue-2")).toHaveAttribute(
      "data-stale",
      "true",
    );

    // J 前进到 C：B 的保留项不被当前问题锚点覆盖，仍在列表
    act(() => {
      screen.getByRole("button", { name: "next" }).click();
    });
    await waitFor(() =>
      expect(screen.getByTestId("location")).toHaveTextContent(
        "issues/issue-3",
      ),
    );
    expect(screen.getByTestId("entry-issue-2")).toHaveAttribute(
      "data-stale",
      "true",
    );
    expect(screen.getByTestId("count")).toHaveTextContent("3");

    // K 回退可回到保留项 B
    act(() => {
      screen.getByRole("button", { name: "prev" }).click();
    });
    await waitFor(() =>
      expect(screen.getByTestId("location")).toHaveTextContent(
        "issues/issue-2",
      ),
    );
    expect(screen.getByTestId("current-index")).toHaveTextContent("1");
  });

  it("pauses auto page-chasing after a failed fetch until refetch succeeds", async () => {
    const fetchNextPage = vi.fn();
    useInfiniteIssueListMock.mockReturnValue({
      data: { pages: [page([makeIssue("issue-1", "GH-1", "A")])] },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
      fetchNextPage,
      hasNextPage: true,
      isFetching: false,
      isFetchingNextPage: false,
    });

    const view = renderHarness("/projects/proj-1/issues/issue-9", {
      currentIssueIdOverride: "issue-9",
    });
    await waitFor(() => expect(fetchNextPage).toHaveBeenCalledTimes(1));

    // 补拉失败：isError 置位、数据不变，不得再次自动补拉形成请求循环
    useInfiniteIssueListMock.mockReturnValue({
      data: { pages: [page([makeIssue("issue-1", "GH-1", "A")])] },
      isLoading: false,
      isError: true,
      refetch: vi.fn(),
      fetchNextPage,
      hasNextPage: true,
      isFetching: false,
      isFetchingNextPage: false,
    });
    view.rerender(
      <MemoryRouter initialEntries={["/projects/proj-1/issues/issue-9"]}>
        {harnessElement({ currentIssueIdOverride: "issue-9" })}
      </MemoryRouter>,
    );
    await act(async () => {});
    expect(fetchNextPage).toHaveBeenCalledTimes(1);

    // refetch 成功（isError 复位）后恢复自动补拉
    useInfiniteIssueListMock.mockReturnValue({
      data: { pages: [page([makeIssue("issue-1", "GH-1", "A")])] },
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
      fetchNextPage,
      hasNextPage: true,
      isFetching: false,
      isFetchingNextPage: false,
    });
    view.rerender(
      <MemoryRouter initialEntries={["/projects/proj-1/issues/issue-9"]}>
        {harnessElement({ currentIssueIdOverride: "issue-9" })}
      </MemoryRouter>,
    );
    await waitFor(() => expect(fetchNextPage).toHaveBeenCalledTimes(2));
  });

  it("releases the pending advance when the page fetch fails so J/K recover", async () => {
    const fetchNextPage = vi.fn();
    const lastItemQuery = {
      data: {
        pages: [
          page([
            makeIssue("issue-1", "GH-1", "A"),
            makeIssue("issue-2", "GH-2", "B"),
          ]),
        ],
      },
      isLoading: false,
      isError: true,
      refetch: vi.fn(),
      fetchNextPage,
      hasNextPage: true,
      isFetching: false,
      isFetchingNextPage: false,
    };
    useInfiniteIssueListMock.mockReturnValue({ ...lastItemQuery, isError: false });

    const view = renderHarness("/projects/proj-1/issues/issue-2", {
      currentIssueIdOverride: "issue-2",
    });

    // 末项前进触发补拉，补拉期间防重复跳转
    act(() => {
      screen.getByRole("button", { name: "next" }).click();
    });
    expect(fetchNextPage).toHaveBeenCalledTimes(1);
    expect(screen.getByTestId("advancing")).toHaveTextContent("true");

    // 补拉失败：放弃自动前进，恢复 J/K
    useInfiniteIssueListMock.mockReturnValue(lastItemQuery);
    view.rerender(
      <MemoryRouter initialEntries={["/projects/proj-1/issues/issue-2"]}>
        {harnessElement({ currentIssueIdOverride: "issue-2" })}
      </MemoryRouter>,
    );
    await waitFor(() =>
      expect(screen.getByTestId("advancing")).toHaveTextContent("false"),
    );

    // 再次按 J 变为手动重试，仍可重新触发补拉
    act(() => {
      screen.getByRole("button", { name: "next" }).click();
    });
    expect(fetchNextPage).toHaveBeenCalledTimes(2);

    // K 反向移动恢复可用
    act(() => {
      screen.getByRole("button", { name: "prev" }).click();
    });
    await waitFor(() =>
      expect(screen.getByTestId("location")).toHaveTextContent(
        "issues/issue-1",
      ),
    );
  });

  it("resets the pinned item when the source changes", () => {
    mockQuery([
      page([
        makeIssue("issue-1", "GH-1", "A"),
        makeIssue("issue-2", "GH-2", "B"),
      ]),
    ]);

    const view = renderHarness("/projects/proj-1/issues/issue-1", {
      currentIssueIdOverride: "issue-1",
    });

    mockQuery([page([makeIssue("issue-2", "GH-2", "B")])]);
    view.rerender(
      <MemoryRouter
        initialEntries={[
          "/projects/proj-1/issues/issue-1?issue_from=board&issue_workflow_status=done",
        ]}
      >
        <Routes>
          <Route
            path="/projects/:id/issues/:iid"
            element={
              <Harness
                context={{
                  kind: "board",
                  workflowStatus: "done",
                  label: "",
                  source: "",
                }}
                currentIssueIdOverride="issue-1"
              />
            }
          />
        </Routes>
      </MemoryRouter>,
    );

    // 新来源里没有 issue-1，但它是新来源下从未出现过的项，不应被保留插入
    expect(screen.getByTestId("count")).toHaveTextContent("1");
    expect(screen.queryByTestId("entry-issue-1")).not.toBeInTheDocument();
  });

  it("keeps navigation silent when the current issue belongs to another project", () => {
    mockQuery([
      page([
        makeIssue("issue-1", "GH-1", "A"),
        makeIssue("issue-2", "GH-2", "B"),
      ]),
    ]);

    renderHarness("/projects/proj-1/issues/issue-1", {
      currentIssueIdOverride: "issue-1",
      currentIssueProjectId: "proj-other",
    });

    act(() => {
      screen.getByRole("button", { name: "next" }).click();
    });
    expect(screen.getByTestId("location")).not.toHaveTextContent("issue-2");
  });

  it("keeps multiple retained items in original order across sequential drops", async () => {
    mockQuery([
      page([
        makeIssue("issue-1", "GH-1", "A"),
        makeIssue("issue-2", "GH-2", "B"),
        makeIssue("issue-3", "GH-3", "C"),
        makeIssue("issue-4", "GH-4", "D"),
      ]),
    ]);

    const view = renderHarness("/projects/proj-1/issues/issue-2", {
      currentIssueIdOverride: "issue-2",
    });

    // 查询移出 B：锚定其后继 C，列表顺序不变
    mockQuery([
      page([
        makeIssue("issue-1", "GH-1", "A"),
        makeIssue("issue-3", "GH-3", "C"),
        makeIssue("issue-4", "GH-4", "D"),
      ]),
    ]);
    view.rerender(
      <MemoryRouter initialEntries={["/projects/proj-1/issues/issue-2"]}>
        {harnessElement({ currentIssueIdOverride: "issue-2" })}
      </MemoryRouter>,
    );
    expect(entryIds()).toEqual([
      "entry-issue-1",
      "entry-issue-2",
      "entry-issue-3",
      "entry-issue-4",
    ]);
    expect(screen.getByTestId("entry-issue-2")).toHaveAttribute(
      "data-stale",
      "true",
    );

    // 切到 C：C 命中并锚定其后继 D（原始下标与 B 相同的场景），B 的保留项不受影响
    view.rerender(
      <MemoryRouter initialEntries={["/projects/proj-1/issues/issue-3"]}>
        {harnessElement({ currentIssueIdOverride: "issue-3" })}
      </MemoryRouter>,
    );
    expect(entryIds()).toEqual([
      "entry-issue-1",
      "entry-issue-2",
      "entry-issue-3",
      "entry-issue-4",
    ]);

    // 再移出 C：B、C 沿链解析后依次插在 A 与 D 之间，保持 B→C 顺序
    mockQuery([
      page([
        makeIssue("issue-1", "GH-1", "A"),
        makeIssue("issue-4", "GH-4", "D"),
      ]),
    ]);
    view.rerender(
      <MemoryRouter initialEntries={["/projects/proj-1/issues/issue-3"]}>
        {harnessElement({ currentIssueIdOverride: "issue-3" })}
      </MemoryRouter>,
    );
    expect(entryIds()).toEqual([
      "entry-issue-1",
      "entry-issue-2",
      "entry-issue-3",
      "entry-issue-4",
    ]);
    expect(screen.getByTestId("entry-issue-2")).toHaveAttribute(
      "data-stale",
      "true",
    );
    expect(screen.getByTestId("entry-issue-3")).toHaveAttribute(
      "data-stale",
      "true",
    );

    // 放开 override，让路由 iid 驱动当前问题（路由仍在 issue-2）：
    // K/J 沿保留项顺序翻页：B(保留, 下标 1) → C(stale, 下标 2)
    view.rerender(
      <MemoryRouter initialEntries={["/projects/proj-1/issues/issue-3"]}>
        {harnessElement({})}
      </MemoryRouter>,
    );
    expect(screen.getByTestId("current-index")).toHaveTextContent("1");
    act(() => {
      screen.getByRole("button", { name: "next" }).click();
    });
    await waitFor(() =>
      expect(screen.getByTestId("location")).toHaveTextContent(
        "issues/issue-3",
      ),
    );
    expect(screen.getByTestId("current-index")).toHaveTextContent("2");
  });

  it("dedupes cross-page raw items so a retained group is inserted only once", () => {
    // 跨页重复：D 同时出现在第 1 页末尾与第 2 页开头（翻页间隙数据变动）
    mockQuery([
      page([makeIssue("issue-1", "GH-1", "A"), makeIssue("issue-2", "GH-2", "B"), makeIssue("issue-4", "GH-4", "D")], 3, 1),
      page([makeIssue("issue-4", "GH-4", "D")], 3, 2),
    ]);

    // 先让 B 命中（锚定后继 D）
    const view = renderHarness("/projects/proj-1/issues/issue-2", {
      currentIssueIdOverride: "issue-2",
    });
    expect(screen.getAllByTestId("entry-issue-4")).toHaveLength(1);

    // B 被查询移出：保留为 stale；重复的真实条目只保留一份，保留组也只插入一次
    mockQuery([
      page([makeIssue("issue-1", "GH-1", "A"), makeIssue("issue-4", "GH-4", "D")], 3, 1),
      page([makeIssue("issue-4", "GH-4", "D")], 3, 2),
    ]);
    view.rerender(
      <MemoryRouter initialEntries={["/projects/proj-1/issues/issue-2"]}>
        {harnessElement({ currentIssueIdOverride: "issue-2" })}
      </MemoryRouter>,
    );
    expect(entryIds()).toEqual(["entry-issue-1", "entry-issue-2", "entry-issue-4"]);
    expect(screen.getByTestId("entry-issue-2")).toHaveAttribute("data-stale", "true");
    expect(screen.getAllByTestId("entry-issue-4")).toHaveLength(1);
  });

  it("keeps sequential reverse-order drops in their original relative order", () => {
    mockQuery([
      page([
        makeIssue("issue-1", "GH-1", "A"),
        makeIssue("issue-2", "GH-2", "B"),
        makeIssue("issue-3", "GH-3", "C"),
        makeIssue("issue-4", "GH-4", "D"),
      ]),
    ]);

    // 先访问 C 并被移出：锚定渲染后继 D
    const view = renderHarness("/projects/proj-1/issues/issue-3", {
      currentIssueIdOverride: "issue-3",
    });
    mockQuery([
      page([
        makeIssue("issue-1", "GH-1", "A"),
        makeIssue("issue-2", "GH-2", "B"),
        makeIssue("issue-4", "GH-4", "D"),
      ]),
    ]);
    view.rerender(
      <MemoryRouter initialEntries={["/projects/proj-1/issues/issue-3"]}>
        {harnessElement({ currentIssueIdOverride: "issue-3" })}
      </MemoryRouter>,
    );

    // K 回到 B 再被移出：B 的渲染后继是 stale 的 C，逆序移出仍应保持 B→C
    view.rerender(
      <MemoryRouter initialEntries={["/projects/proj-1/issues/issue-2"]}>
        {harnessElement({ currentIssueIdOverride: "issue-2" })}
      </MemoryRouter>,
    );
    mockQuery([
      page([makeIssue("issue-1", "GH-1", "A"), makeIssue("issue-4", "GH-4", "D")]),
    ]);
    view.rerender(
      <MemoryRouter initialEntries={["/projects/proj-1/issues/issue-2"]}>
        {harnessElement({ currentIssueIdOverride: "issue-2" })}
      </MemoryRouter>,
    );

    expect(entryIds()).toEqual([
      "entry-issue-1",
      "entry-issue-2",
      "entry-issue-3",
      "entry-issue-4",
    ]);
    expect(screen.getByTestId("entry-issue-2")).toHaveAttribute("data-stale", "true");
    expect(screen.getByTestId("entry-issue-3")).toHaveAttribute("data-stale", "true");
  });

  it("releases pending advance when a fetch completes without growing the list", async () => {
    const fetchNextPage = vi.fn().mockResolvedValue(undefined);
    mockQuery([
      page([makeIssue("issue-1", "GH-1", "A"), makeIssue("issue-2", "GH-2", "B")]),
    ], { hasNextPage: true, fetchNextPage });

    const view = renderHarness("/projects/proj-1/issues/issue-2", {
      currentIssueIdOverride: "issue-2",
    });

    // 末项 J 触发补拉
    act(() => {
      screen.getByRole("button", { name: "next" }).click();
    });
    expect(screen.getByTestId("advancing")).toHaveTextContent("true");

    // 补拉进行中
    mockQuery([
      page([makeIssue("issue-1", "GH-1", "A"), makeIssue("issue-2", "GH-2", "B")]),
    ], { hasNextPage: true, fetchNextPage, isFetchingNextPage: true });
    view.rerender(
      <MemoryRouter initialEntries={["/projects/proj-1/issues/issue-2"]}>
        {harnessElement({ currentIssueIdOverride: "issue-2" })}
      </MemoryRouter>,
    );

    // 完成：新页全部被跨页去重/为空，列表没增长 → 释放 J/K
    mockQuery([
      page([makeIssue("issue-1", "GH-1", "A"), makeIssue("issue-2", "GH-2", "B")]),
    ], { hasNextPage: true, fetchNextPage });
    view.rerender(
      <MemoryRouter initialEntries={["/projects/proj-1/issues/issue-2"]}>
        {harnessElement({ currentIssueIdOverride: "issue-2" })}
      </MemoryRouter>,
    );
    await waitFor(() =>
      expect(screen.getByTestId("advancing")).toHaveTextContent("false"),
    );
  });

  it("removes a retained item once it re-hits the query while another issue is current", () => {
    mockQuery([
      page([
        makeIssue("issue-1", "GH-1", "A"),
        makeIssue("issue-2", "GH-2", "B"),
        makeIssue("issue-3", "GH-3", "C"),
        makeIssue("issue-4", "GH-4", "D"),
      ]),
    ]);

    const view = renderHarness("/projects/proj-1/issues/issue-2", {
      currentIssueIdOverride: "issue-2",
    });

    // 当前 B 被查询移出：保留为 stale
    mockQuery([
      page([
        makeIssue("issue-1", "GH-1", "A"),
        makeIssue("issue-3", "GH-3", "C"),
        makeIssue("issue-4", "GH-4", "D"),
      ]),
    ]);
    view.rerender(
      <MemoryRouter initialEntries={["/projects/proj-1/issues/issue-2"]}>
        {harnessElement({ currentIssueIdOverride: "issue-2" })}
      </MemoryRouter>,
    );
    expect(screen.getByTestId("entry-issue-2")).toHaveAttribute(
      "data-stale",
      "true",
    );

    // 切到 C：B 不再是当前问题，但仍保留
    view.rerender(
      <MemoryRouter initialEntries={["/projects/proj-1/issues/issue-3"]}>
        {harnessElement({ currentIssueIdOverride: "issue-3" })}
      </MemoryRouter>,
    );
    expect(screen.getByTestId("entry-issue-2")).toHaveAttribute(
      "data-stale",
      "true",
    );

    // B 重新命中查询（如 refetch 后回来）：真实条目让位，保留集合移除 B
    mockQuery([
      page([
        makeIssue("issue-1", "GH-1", "A"),
        makeIssue("issue-2", "GH-2", "B"),
        makeIssue("issue-3", "GH-3", "C"),
        makeIssue("issue-4", "GH-4", "D"),
      ]),
    ]);
    view.rerender(
      <MemoryRouter initialEntries={["/projects/proj-1/issues/issue-3"]}>
        {harnessElement({ currentIssueIdOverride: "issue-3" })}
      </MemoryRouter>,
    );
    expect(screen.getByTestId("entry-issue-2")).not.toHaveAttribute(
      "data-stale",
    );

    // 再次移出 B（当前仍是 C）：旧保留项不得以旧位置复现
    mockQuery([
      page([
        makeIssue("issue-1", "GH-1", "A"),
        makeIssue("issue-3", "GH-3", "C"),
        makeIssue("issue-4", "GH-4", "D"),
      ]),
    ]);
    view.rerender(
      <MemoryRouter initialEntries={["/projects/proj-1/issues/issue-3"]}>
        {harnessElement({ currentIssueIdOverride: "issue-3" })}
      </MemoryRouter>,
    );
    expect(screen.queryByTestId("entry-issue-2")).not.toBeInTheDocument();
    expect(entryIds()).toEqual([
      "entry-issue-1",
      "entry-issue-3",
      "entry-issue-4",
    ]);
  });

  it("does not leak retained items or anchors across project switches", () => {
    mockQuery([
      page([
        makeIssue("issue-1", "GH-1", "A"),
        makeIssue("issue-2", "GH-2", "B"),
        makeIssue("issue-3", "GH-3", "C"),
      ]),
    ]);

    const view = renderHarness("/projects/proj-1/issues/issue-2", {
      currentIssueIdOverride: "issue-2",
    });

    // proj-1 中 B 被查询移出：保留为 stale
    mockQuery([
      page([
        makeIssue("issue-1", "GH-1", "A"),
        makeIssue("issue-3", "GH-3", "C"),
      ]),
    ]);
    view.rerender(
      <MemoryRouter initialEntries={["/projects/proj-1/issues/issue-2"]}>
        {harnessElement({ currentIssueIdOverride: "issue-2" })}
      </MemoryRouter>,
    );
    expect(screen.getByTestId("entry-issue-2")).toHaveAttribute(
      "data-stale",
      "true",
    );

    // 切到 proj-2：筛选相同、iid 暂时不变且 enabled；旧项目的保留项与锚点必须全部失效
    mockQuery([
      page([
        makeIssue("p2-1", "FS-1", "X"),
        makeIssue("p2-2", "FS-2", "Y"),
      ]),
    ]);
    view.rerender(
      <MemoryRouter initialEntries={["/projects/proj-2/issues/issue-2"]}>
        {harnessElement({ projectId: "proj-2", currentIssueIdOverride: "issue-2" })}
      </MemoryRouter>,
    );
    expect(entryIds()).toEqual(["entry-p2-1", "entry-p2-2"]);
    expect(screen.queryByTestId("entry-issue-2")).not.toBeInTheDocument();

    // proj-2 里当前问题（iid 暂时相同）被查询移出：不得复用旧项目锚点复现旧条目
    mockQuery([page([makeIssue("p2-1", "FS-1", "X")])]);
    view.rerender(
      <MemoryRouter initialEntries={["/projects/proj-2/issues/issue-2"]}>
        {harnessElement({ projectId: "proj-2", currentIssueIdOverride: "issue-2" })}
      </MemoryRouter>,
    );
    expect(entryIds()).toEqual(["entry-p2-1"]);
    expect(screen.queryByTestId("entry-issue-2")).not.toBeInTheDocument();
  });
});
