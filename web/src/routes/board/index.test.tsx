import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/copy", () => ({ copyWithToast: vi.fn() }));

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
              unread_comments_count: 0,
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

vi.mock("@/lib/hooks/use-recommendations", () => ({
  useRecommendations: () => ({
    data: { items: [] },
    isLoading: false,
    isError: false,
  }),
  useDeferRecommendation: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useRestoreRecommendation: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useRemoveRecommendation: () => ({ mutateAsync: vi.fn(), isPending: false }),
}));

vi.mock("@/lib/hooks/use-issue-prompt", () => ({
  useIssuePromptList: () => [
    {
      id: "default",
      name: "默认",
      content: "请处理此问题",
    },
  ],
}));

vi.mock("@/routes/board/components/close-all-done-button", () => ({
  CloseAllDoneButton: () => null,
}));

// Header 渲染通知铃（INT-90）：本文件不关心通知数据，mock 其钩子。
vi.mock("@/lib/hooks/use-notifications", () => ({
  NOTIFICATION_UNREAD_POLL_MS: 30000,
  useNotifications: vi.fn(() => ({ data: [] })),
  useUnreadNotificationCount: vi.fn(() => ({ data: 0 })),
  useMarkNotificationRead: vi.fn(() => ({ mutate: vi.fn(), mutateAsync: vi.fn() })),
  useMarkAllNotificationsRead: vi.fn(() => ({ mutate: vi.fn(), mutateAsync: vi.fn() })),
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

// 多选只服务于批量复制，随其移除：看板不再出现多选开关、勾选框与批量工具栏。
describe("BoardPage without multi-select", () => {
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

  it("renders issues and keeps no multi-select affordance even while Control is held", async () => {
    await renderBoardPage();

    expect(screen.getAllByText("INT-1").length).toBeGreaterThan(0);
    expect(
      screen.queryByRole("button", { name: /多选/ }),
    ).not.toBeInTheDocument();
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
    expect(screen.queryByText(/已选/)).not.toBeInTheDocument();

    fireEvent.keyDown(window, { key: "Control", ctrlKey: true });
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
  });
});
