import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { BoardIssueColumnContext } from "@/lib/issue-list-context";
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
  BoardIssueCard: ({
    issue,
    getColumnContext,
  }: {
    issue: Issue;
    getColumnContext?: (issue: Issue) => BoardIssueColumnContext | null;
  }) => (
    <div
      data-issue-id={issue.id}
      data-column-context={
        getColumnContext
          ? JSON.stringify(getColumnContext(issue))
          : undefined
      }
    >
      {issue.title}
    </div>
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
    unread_comments_count: 0,
    closed_at: null,
    created_at: "2026-08-22T00:00:00Z",
    updated_at: "2026-08-22T00:00:00Z",
    internal_meta: {
      workflow_status: "todo",
      progress_percent: null,
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
        <BoardColumn
          columnId="todo"
          projectId="project-1"
          filter={{ label: "", source: "all" }}
          onFilterChange={() => {}}
        />
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
        <BoardColumn
          columnId="todo"
          projectId="project-1"
          filter={{ label: "", source: "all" }}
          onFilterChange={() => {}}
        />
      </MemoryRouter>,
    );

    const restored = screen.getByText("问题 issue-0").closest(
      ".overflow-y-auto",
    ) as HTMLElement;
    expect(restored.scrollTop).toBe(720);
  });

  it("passes the column context down to issue cards", () => {
    render(
      <MemoryRouter>
        <BoardColumn
          columnId="todo"
          projectId="project-1"
          filter={{ label: "bug", source: "github" }}
          onFilterChange={() => {}}
          getIssueColumnContext={() => ({
            workflowStatus: "todo",
            label: "bug",
            source: "github",
          })}
        />
      </MemoryRouter>,
    );

    expect(screen.getByText("问题 issue-0")).toHaveAttribute(
      "data-column-context",
      JSON.stringify({ workflowStatus: "todo", label: "bug", source: "github" }),
    );
  });

  it("forwards column filter changes to the owner", () => {
    const onFilterChange = vi.fn();
    render(
      <MemoryRouter>
        <BoardColumn
          columnId="todo"
          projectId="project-1"
          filter={{ label: "", source: "all" }}
          onFilterChange={onFilterChange}
        />
      </MemoryRouter>,
    );

    fireEvent.click(screen.getByRole("button", { name: "筛选" }));
    fireEvent.click(screen.getByRole("menuitemradio", { name: "GitHub 问题" }));

    expect(onFilterChange).toHaveBeenCalledWith({ label: "", source: "github" });
  });
});
