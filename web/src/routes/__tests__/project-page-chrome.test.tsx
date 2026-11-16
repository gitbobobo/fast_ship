import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import IssuesPage from "@/routes/issues/index";
import BoardPage from "@/routes/board/index";
import DocumentsPage from "@/routes/documents/index";
import ScreenshotsPage from "@/routes/screenshots/index";
import LogsPage from "@/routes/logs/index";
import LogRunDetailPage from "@/routes/logs/$runId";
import { useProjects } from "@/lib/hooks/use-projects";
import { useIssues, useUpdateIssueWorkflowStatus } from "@/lib/hooks/use-issues";
import { useDocuments } from "@/lib/hooks/use-documents";
import { useLogRun, useLogRuns } from "@/lib/hooks/use-logs";
import { useScreenshotScreens } from "@/lib/hooks/use-screenshots";
import { withTestQueryClient } from "@/test/render";

// 项目列表是面包屑与 notFound 判定的唯一数据源，每个用例单独设置
vi.mock("@/lib/hooks/use-projects", () => ({
  useProjects: vi.fn(),
}));

vi.mock("@/lib/hooks/use-issues", () => ({
  useIssues: vi.fn(() => ({
    data: { items: [], total: 0, page: 1, page_size: 20 },
    isLoading: false,
  })),
  useIssueFilterOptions: vi.fn(() => ({
    data: { labels: [] },
    isLoading: false,
  })),
  useSyncProjectIssues: vi.fn(() => ({
    mutateAsync: vi.fn(),
    isPending: false,
  })),
  useUpdateIssue: vi.fn(() => ({ mutateAsync: vi.fn(), isPending: false })),
  useUpdateIssueWorkflowStatus: vi.fn(() => ({
    mutateAsync: vi.fn(),
    isPending: false,
  })),
}));

vi.mock("@/lib/hooks/use-documents", () => ({
  useDocuments: vi.fn(() => ({
    data: { items: [], total: 0 },
    isLoading: false,
    isError: false,
  })),
  useDocument: vi.fn(() => ({ data: undefined, isLoading: false })),
  useCreateDocument: vi.fn(() => ({
    mutateAsync: vi.fn(),
    isPending: false,
  })),
  useUpdateDocument: vi.fn(() => ({
    mutateAsync: vi.fn(),
    isPending: false,
  })),
  useDeleteDocument: vi.fn(() => ({
    mutateAsync: vi.fn(),
    isPending: false,
  })),
}));

vi.mock("@/lib/hooks/use-logs", () => ({
  useLogRuns: vi.fn(() => ({
    data: { items: [], total: 0, page: 1, page_size: 50 },
    isLoading: false,
    isError: false,
  })),
  useDeleteLogRun: vi.fn(() => ({
    mutateAsync: vi.fn(),
    isPending: false,
  })),
  useClearProjectLogs: vi.fn(() => ({
    mutateAsync: vi.fn(),
    isPending: false,
  })),
  useLogRun: vi.fn(() => ({ data: undefined, isLoading: false })),
  useInfiniteLogEntries: vi.fn(() => ({
    data: { pages: [] },
    isLoading: false,
    isError: false,
    fetchNextPage: vi.fn(),
    hasNextPage: false,
    isFetchingNextPage: false,
  })),
}));

vi.mock("@/lib/hooks/use-screenshots", () => ({
  useScreenshotScreens: vi.fn(() => ({
    data: { items: [] },
    isLoading: false,
  })),
}));

vi.mock("@/lib/hooks/use-screenshot-annotations", () => ({
  useScreenshotAnnotations: vi.fn(() => ({
    data: [],
    isSuccess: true,
    isFetching: false,
  })),
}));

// 重组件用桩替换：这里只验证页面骨架（面包屑/空态/无页内切换器），
// 数据内容由各自页面的测试文件覆盖
vi.mock("@/routes/board/components/board-column", () => ({
  BoardColumn: () => <div data-testid="board-column-stub" />,
}));
vi.mock("@/routes/board/components/recommended-issues-button", () => ({
  RecommendedIssuesButton: () => null,
}));
vi.mock("@/components/ui/markdown-editor", () => ({
  MarkdownEditor: () => null,
}));
vi.mock("@/components/screenshots/screenshot-canvas", () => ({
  ScreenshotCanvas: () => <div data-testid="canvas-stub" />,
  // type-only 导入在运行时不存在，用任意值占位即可
  CanvasLocateRequest: {},
}));
vi.mock("@/components/screenshots/upload-screenshots-dialog", () => ({
  UploadScreenshotsDialog: () => null,
}));
vi.mock("@/components/screenshots/screenshot-lightbox", () => ({
  ScreenshotLightbox: () => null,
}));
vi.mock("@/components/screenshots/launch-annotation-thread-dialog", () => ({
  LaunchAnnotationThreadDialog: () => null,
}));

const PROJECT = {
  id: "proj-1",
  user_id: "user-1",
  name: "Demo",
  description: "",
  github_owner: "",
  github_repo: "",
  repository_url: null,
  code_github_owner: "",
  code_repo_url: null,
  code_github_repo: "",
  public_github_token_id: "",
  code_github_token_id: "",
  code_integration_id: null,
  public_integration_id: null,
  shelved_at: null,
  shelved_until: null,
  is_shelved: false,
  created_at: "2026-04-06T09:00:00Z",
  updated_at: "2026-04-06T09:00:00Z",
};

function mockProjects(
  items: unknown[] | undefined,
  { isLoading = false }: { isLoading?: boolean } = {},
) {
  vi.mocked(useProjects).mockReturnValue({
    data:
      items === undefined
        ? undefined
        : { items, total: items.length, page: 1, page_size: 100 },
    isLoading,
  } as unknown as ReturnType<typeof useProjects>);
}

const PAGES = [
  {
    key: "issues",
    element: <IssuesPage />,
    route: "/projects/:id/issues",
    entry: "/projects/proj-1/issues",
    section: "问题",
    dataHook: () => vi.mocked(useIssues),
  },
  {
    key: "board",
    element: <BoardPage />,
    route: "/projects/:id/board",
    entry: "/projects/proj-1/board",
    section: "看板",
    dataHook: () => vi.mocked(useUpdateIssueWorkflowStatus),
  },
  {
    key: "documents",
    element: <DocumentsPage />,
    route: "/projects/:id/documents",
    entry: "/projects/proj-1/documents",
    section: "文档",
    dataHook: () => vi.mocked(useDocuments),
  },
  {
    key: "screenshots",
    element: <ScreenshotsPage />,
    route: "/projects/:id/screenshots",
    entry: "/projects/proj-1/screenshots",
    section: "截图",
    dataHook: () => vi.mocked(useScreenshotScreens),
  },
  {
    key: "logs",
    element: <LogsPage />,
    route: "/projects/:id/logs",
    entry: "/projects/proj-1/logs",
    section: "日志",
    dataHook: () => vi.mocked(useLogRuns),
  },
];

function renderPage(element: React.ReactElement, route: string, entry: string) {
  return render(
    withTestQueryClient(
      <MemoryRouter initialEntries={[entry]}>
        <Routes>
          <Route path={route} element={element} />
        </Routes>
      </MemoryRouter>,
    ),
  );
}

describe("项目级页面骨架回归（INT-237 PR3）", () => {
  beforeEach(() => {
    mockProjects([PROJECT]);
  });

  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it.each(PAGES)(
    "$key 页面包屑为 Fast Ship / 项目名 / section，且不再渲染页内切换器",
    ({ element, route, entry, section }) => {
      renderPage(element, route, entry);

      const crumb = screen.getByTestId("project-breadcrumb");
      expect(crumb).toHaveTextContent("Fast Ship");
      expect(crumb).toHaveTextContent("Demo");
      expect(crumb).toHaveTextContent(section);
      // 页内项目选择已上移侧边栏：不再出现下拉占位文案
      expect(screen.queryByText("请选择项目")).not.toBeInTheDocument();
      expect(screen.queryByText("暂无项目")).not.toBeInTheDocument();
    },
  );

  it.each(PAGES)(
    "$key 页在无效项目 ID 下渲染空态且不请求数据",
    ({ element, route, entry, dataHook }) => {
      mockProjects([{ ...PROJECT, id: "other" }]);
      const hook = dataHook();
      hook.mockClear();

      renderPage(element, route, entry);

      expect(screen.getByText("项目不存在或无权访问")).toBeInTheDocument();
      expect(
        screen.getByRole("link", { name: "返回项目列表" }),
      ).toHaveAttribute("href", "/projects");
      expect(hook).not.toHaveBeenCalled();
    },
  );

  it.each(PAGES)(
    "$key 页在项目列表加载中渲染门控占位，不挂数据 hooks",
    ({ element, route, entry, dataHook }) => {
      // 冷启动：列表查询未返回时项目有效性未知，页面数据 hooks 不得挂载，
      // 否则无效 ID 深链会在确认前发出数据请求
      mockProjects(undefined, { isLoading: true });
      const dataHookMock = dataHook();
      dataHookMock.mockClear?.();

      renderPage(element, route, entry);

      expect(screen.getByTestId("project-page-loading")).toBeInTheDocument();
      expect(screen.queryByTestId("project-breadcrumb")).not.toBeInTheDocument();
      expect(screen.queryByTestId("project-not-found")).not.toBeInTheDocument();
      if (dataHookMock.mock) {
        expect(dataHookMock).not.toHaveBeenCalled();
      }
    },
  );

  it("运行详情页同样只认路径：面包屑、无效 ID 空态、无页内切换器", () => {
    vi.mocked(useLogRun).mockReturnValue({
      data: {
        project_id: "proj-1",
        run_id: "run-1",
        description: "demo run",
        entry_count: 0,
        first_entry_at: null,
        last_entry_at: null,
        source: "",
        created_at: "2026-07-01T00:00:00Z",
        updated_at: "2026-07-01T00:00:00Z",
      },
      isLoading: false,
      isError: false,
    } as unknown as ReturnType<typeof useLogRun>);

    renderPage(<LogRunDetailPage />, "/projects/:id/logs/:runId", "/projects/proj-1/logs/run-1");

    const crumb = screen.getByTestId("project-breadcrumb");
    expect(crumb).toHaveTextContent("Fast Ship");
    expect(crumb).toHaveTextContent("Demo");
    expect(crumb).toHaveTextContent("运行详情");
    expect(screen.queryByText("请选择项目")).not.toBeInTheDocument();
    expect(screen.getByText("demo run")).toBeInTheDocument();
  });

  it("运行详情页在无效项目 ID 下渲染空态且不请求运行数据", () => {
    mockProjects([{ ...PROJECT, id: "other" }]);
    vi.mocked(useLogRun).mockClear();

    renderPage(<LogRunDetailPage />, "/projects/:id/logs/:runId", "/projects/proj-1/logs/run-1");

    expect(screen.getByText("项目不存在或无权访问")).toBeInTheDocument();
    expect(useLogRun).not.toHaveBeenCalled();
  });
});
