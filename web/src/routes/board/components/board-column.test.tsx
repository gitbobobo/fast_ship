import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { BoardColumn } from "./board-column";
import { resetScrollPositions } from "@/lib/scroll-positions";

const { mockUseInfiniteBoardIssues } = vi.hoisted(() => ({
  mockUseInfiniteBoardIssues: vi.fn(),
}));

vi.mock("@dnd-kit/core", () => ({
  useDroppable: () => ({
    setNodeRef: vi.fn(),
    isOver: false,
  }),
}));

vi.mock("@/lib/hooks/use-issues", () => ({
  useInfiniteBoardIssues: (...args: unknown[]) =>
    mockUseInfiniteBoardIssues(...args),
  useIssueFilterOptions: () => ({ data: { labels: [] } }),
}));

vi.mock("./board-issue-card", () => ({
  BoardIssueCard: ({ issue }: { issue: Issue }) => (
    <div data-issue-id={issue.id}>{issue.title}</div>
  ),
}));

const scrollTopValues = new WeakMap<HTMLElement, number>();

function installScrollTopStub() {
  Object.defineProperty(HTMLElement.prototype, "scrollTop", {
    configurable: true,
    get(this: HTMLElement) {
      return scrollTopValues.get(this) ?? 0;
    },
    set(this: HTMLElement, value: number) {
      scrollTopValues.set(this, Number(value) || 0);
    },
  });
}

function makeIssue(id: string): Issue {
  return {
    id,
    project_id: "project-1",
    source: "internal",
    sequence_number: 1,
    reference: `#${id}`,
    state: "open",
    state_reason: "",
    title: `问题 ${id}`,
    body: "",
    body_html: "",
    author: { login: "alice", avatar_url: "" },
    created_at: "2026-08-22T00:00:00Z",
    updated_at: "2026-08-22T00:00:00Z",
    internal_meta: {
      workflow_status: "todo",
      checklist_total: 0,
      checklist_done: 0,
      labels: [],
    },
  };
}

function mockLoadedIssues() {
  mockUseInfiniteBoardIssues.mockReturnValue({
    data: {
      pages: [
        {
          items: Array.from({ length: 8 }, (_, i) => makeIssue(`issue-${i}`)),
          total: 40,
          page: 1,
          page_size: 8,
        },
      ],
    },
    fetchNextPage: vi.fn(),
    hasNextPage: true,
    isFetchingNextPage: false,
    isLoading: false,
  });
}

describe("BoardColumn multi-select wiring", () => {
  beforeEach(() => {
    resetScrollPositions();
    mockUseInfiniteBoardIssues.mockReset();
    mockLoadedIssues();
    installScrollTopStub();
    vi.stubGlobal(
      "IntersectionObserver",
      class {
        observe() {}
        disconnect() {}
        unobserve() {}
      },
    );
  });

  afterEach(() => {
    resetScrollPositions();
    vi.unstubAllGlobals();
  });

  it("reports loaded issues through onColumnIssuesChange when issues change", () => {
    const onColumnIssuesChange = vi.fn();

    const { rerender } = render(
      <MemoryRouter>
        <BoardColumn
          columnId="todo"
          projectId="project-1"
          onColumnIssuesChange={onColumnIssuesChange}
        />
      </MemoryRouter>,
    );

    expect(onColumnIssuesChange).toHaveBeenCalledTimes(1);
    const [reportedColumn, reportedIssues] =
      onColumnIssuesChange.mock.calls[0];
    expect(reportedColumn).toBe("todo");
    expect(reportedIssues.map((issue: Issue) => issue.id)).toEqual(
      Array.from({ length: 8 }, (_, i) => `issue-${i}`),
    );

    const filteredIssues = [makeIssue("issue-0"), makeIssue("issue-3")];
    mockUseInfiniteBoardIssues.mockReturnValue({
      data: {
        pages: [
          {
            items: filteredIssues,
            total: 2,
            page: 1,
            page_size: 8,
          },
        ],
      },
      fetchNextPage: vi.fn(),
      hasNextPage: false,
      isFetchingNextPage: false,
      isLoading: false,
    });

    rerender(
      <MemoryRouter>
        <BoardColumn
          columnId="todo"
          projectId="project-1"
          onColumnIssuesChange={onColumnIssuesChange}
        />
      </MemoryRouter>,
    );

    expect(onColumnIssuesChange).toHaveBeenCalledTimes(2);
    expect(onColumnIssuesChange).toHaveBeenLastCalledWith(
      "todo",
      filteredIssues,
    );
  });

  it("does not report an empty list while the column is loading a new filter", () => {
    const onColumnIssuesChange = vi.fn();

    const { rerender } = render(
      <MemoryRouter>
        <BoardColumn
          columnId="todo"
          projectId="project-1"
          onColumnIssuesChange={onColumnIssuesChange}
        />
      </MemoryRouter>,
    );

    expect(onColumnIssuesChange).toHaveBeenCalledTimes(1);
    expect(onColumnIssuesChange.mock.calls[0][1].length).toBe(8);

    // 模拟切换筛选条件：queryKey 变化，TanStack Query 先把 data 置为 undefined
    mockUseInfiniteBoardIssues.mockReturnValue({
      data: undefined,
      fetchNextPage: vi.fn(),
      hasNextPage: false,
      isFetchingNextPage: false,
      isLoading: true,
    });

    rerender(
      <MemoryRouter>
        <BoardColumn
          columnId="todo"
          projectId="project-1"
          onColumnIssuesChange={onColumnIssuesChange}
        />
      </MemoryRouter>,
    );

    // isLoading 期间不得上报空列表，否则会把该列勾选全部剪掉
    expect(onColumnIssuesChange).toHaveBeenCalledTimes(1);
    expect(
      onColumnIssuesChange.mock.calls.some(
        (call) => (call[1] as Issue[]).length === 0,
      ),
    ).toBe(false);

    // 新数据到达后再上报真实列表
    const filteredIssues = [makeIssue("issue-2")];
    mockUseInfiniteBoardIssues.mockReturnValue({
      data: {
        pages: [
          {
            items: filteredIssues,
            total: 1,
            page: 1,
            page_size: 8,
          },
        ],
      },
      fetchNextPage: vi.fn(),
      hasNextPage: false,
      isFetchingNextPage: false,
      isLoading: false,
    });

    rerender(
      <MemoryRouter>
        <BoardColumn
          columnId="todo"
          projectId="project-1"
          onColumnIssuesChange={onColumnIssuesChange}
        />
      </MemoryRouter>,
    );

    expect(onColumnIssuesChange).toHaveBeenCalledTimes(2);
    expect(onColumnIssuesChange).toHaveBeenLastCalledWith(
      "todo",
      filteredIssues,
    );
  });
});

describe("BoardColumn scroll restoration", () => {
  beforeEach(() => {
    resetScrollPositions();
    mockUseInfiniteBoardIssues.mockReset();
    mockLoadedIssues();
    installScrollTopStub();
    vi.stubGlobal(
      "IntersectionObserver",
      class {
        observe() {}
        disconnect() {}
        unobserve() {}
      },
    );
  });

  afterEach(() => {
    resetScrollPositions();
    vi.unstubAllGlobals();
  });

  it("restores the column list offset after leaving and coming back", () => {
    const { unmount } = render(
      <MemoryRouter>
        <BoardColumn columnId="todo" projectId="project-1" />
      </MemoryRouter>,
    );

    const scroller = screen.getByText("问题 issue-0").closest(
      ".overflow-y-auto",
    ) as HTMLElement;
    scroller.scrollTop = 720;
    fireEvent.scroll(scroller);
    unmount();

    render(
      <MemoryRouter>
        <BoardColumn columnId="todo" projectId="project-1" />
      </MemoryRouter>,
    );

    const restored = screen.getByText("问题 issue-0").closest(
      ".overflow-y-auto",
    ) as HTMLElement;
    expect(restored.scrollTop).toBe(720);
  });
});
