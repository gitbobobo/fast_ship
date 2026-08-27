import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/hooks/use-projects", () => ({
  useProjects: () => ({
    data: {
      items: [
        {
          id: "project-1",
          name: "musiver",
        },
      ],
    },
    isLoading: false,
  }),
}));

vi.mock("@/lib/hooks/use-issues", () => ({
  useInfiniteBoardIssues: () => ({
    data: {
      pages: [
        {
          items: [
            {
              id: "issue-1",
              project_id: "project-1",
              source: "internal",
              sequence_number: 1,
              reference: "INT-1",
              state: "open",
              state_reason: "",
              title: "预览选择提示",
              body: "",
              body_html: "",
              author: { login: "alice", avatar_url: "" },
              created_at: "2026-08-22T00:00:00Z",
              updated_at: "2026-08-22T00:00:00Z",
              internal_meta: {
                workflow_status: "",
                checklist_total: 0,
                checklist_done: 0,
                labels: [],
              },
            } satisfies Issue,
          ],
          total: 1,
          page: 1,
          page_size: 20,
        },
      ],
    },
    fetchNextPage: vi.fn(),
    hasNextPage: false,
    isFetchingNextPage: false,
    isLoading: false,
  }),
  useIssueFilterOptions: () => ({ data: { labels: [] } }),
  useUpdateIssueWorkflowStatus: () => ({ mutateAsync: vi.fn() }),
}));

vi.mock("@/lib/hooks/use-issue-prompt", () => ({
  useIssuePromptList: () => [
    {
      id: "default",
      name: "默认",
      content: "请处理此问题",
      supports_batch: true,
    },
  ],
}));

vi.mock("@/routes/board/components/close-all-done-button", () => ({
  CloseAllDoneButton: () => null,
}));

vi.mock("@dnd-kit/core", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@dnd-kit/core")>();
  return {
    ...actual,
    useDraggable: () => ({
      attributes: {},
      listeners: {},
      setNodeRef: vi.fn(),
      transform: null,
      isDragging: false,
    }),
    useDroppable: () => ({
      setNodeRef: vi.fn(),
      isOver: false,
    }),
  };
});

async function renderBoardPage() {
  const { default: BoardPage } = await import("@/routes/board/index");
  return render(
    <MemoryRouter initialEntries={["/board?project=project-1"]}>
      <BoardPage />
    </MemoryRouter>,
  );
}

describe("BoardPage select preview", () => {
  beforeEach(() => {
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
    vi.unstubAllGlobals();
  });

  it("shows card checkboxes while Control is held", async () => {
    await renderBoardPage();

    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();

    fireEvent.keyDown(window, { key: "Control", ctrlKey: true });

    expect(screen.getAllByRole("checkbox").length).toBeGreaterThan(0);

    fireEvent.keyUp(window, { key: "Control", ctrlKey: false });

    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
  });
});
