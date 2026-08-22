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
  BoardIssueCard: ({ issue }: { issue: Issue }) => <div>{issue.title}</div>,
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
