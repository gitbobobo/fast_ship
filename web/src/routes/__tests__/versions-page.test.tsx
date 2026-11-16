import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import VersionsPage from "@/routes/versions/index";
import { renderWithRoute } from "@/test/render";
import { useProjects } from "@/lib/hooks/use-projects";
import { useVersions } from "@/lib/hooks/use-versions";

const mockVersionFormDialog = vi.fn((_props: unknown) => null);

vi.mock("@/lib/hooks/use-projects", () => ({
  useProjects: vi.fn(),
}));

vi.mock("@/lib/hooks/use-versions", () => ({
  useVersions: vi.fn(),
}));

vi.mock("@/components/versions/version-form-dialog", () => ({
  VersionFormDialog: (props: unknown) => mockVersionFormDialog(props),
}));

// 页面只认路径：项目上下文来自 /projects/:id/versions，不再有页内选择逻辑
function mockProjectsList(
  items: unknown[],
  { isLoading = false }: { isLoading?: boolean } = {},
) {
  vi.mocked(useProjects).mockReturnValue({
    data: { items, total: items.length, page: 1, page_size: 100 },
    isLoading,
  } as unknown as ReturnType<typeof useProjects>);
}

describe("VersionsPage", () => {
  beforeEach(() => {
    mockProjectsList([
      {
        id: "proj-1",
        user_id: "user-1",
        name: "Alpha App",
        description: "release pipeline",
        github_owner: "acme",
        github_repo: "alpha",
        latest_version: null,
        created_at: "2026-04-06T09:00:00Z",
        updated_at: "2026-04-06T09:00:00Z",
      },
    ]);
    vi.mocked(useVersions).mockReturnValue({
      data: {
        items: [
          {
            id: "ver-1",
            project_id: "proj-1",
            version_number: "v1.2.0",
            status: "pending",
            release_notes: "",
            target_commitish: "",
            github_release_url: "",
            error_log: "",
            ship_status: "",
            ship_stage: "",
            ship_message: "",
            created_at: "2026-04-06T10:00:00Z",
            shipped_at: null,
            artifacts: [],
          },
        ],
        total: 1,
        page: 1,
        page_size: 100,
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useVersions>);
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it("takes the project from the path and shows the breadcrumb", () => {
    renderWithRoute(<VersionsPage />, {
      path: "/projects/:id/versions",
      initialEntry: "/projects/proj-1/versions",
    });

    expect(useVersions).toHaveBeenCalledWith("proj-1");
    const crumb = screen.getByTestId("project-breadcrumb");
    expect(crumb).toHaveTextContent("fast_ship");
    expect(crumb).toHaveTextContent("Alpha App");
    expect(crumb).toHaveTextContent("版本");
    expect(screen.getByText("v1.2.0")).toBeInTheDocument();
    screen.getByRole("button", { name: /创建版本/i });
    expect(
      screen.queryByRole("link", { name: /创建版本/i }),
    ).not.toBeInTheDocument();
  });

  it("does not render an in-page project switcher anymore", () => {
    renderWithRoute(<VersionsPage />, {
      path: "/projects/:id/versions",
      initialEntry: "/projects/proj-1/versions",
    });

    // 项目切换已上移侧边栏；页内不再有「请选择项目」下拉或占位
    expect(screen.queryByText("暂无项目")).not.toBeInTheDocument();
    expect(screen.queryByTestId("project-not-found")).not.toBeInTheDocument();
  });

  it("shows the gate placeholder while the project list is loading", () => {
    // 冷启动：项目有效性未知，先渲染门控占位，页面数据 hooks 不挂载（不发请求）
    vi.mocked(useProjects).mockReturnValue({
      data: undefined,
      isLoading: true,
    } as unknown as ReturnType<typeof useProjects>);
    vi.mocked(useVersions).mockReturnValue({
      data: undefined,
      isLoading: true,
    } as unknown as ReturnType<typeof useVersions>);

    renderWithRoute(<VersionsPage />, {
      path: "/projects/:id/versions",
      initialEntry: "/projects/proj-1/versions",
    });

    expect(screen.getByTestId("project-page-loading")).toBeInTheDocument();
    expect(screen.queryByTestId("project-breadcrumb")).not.toBeInTheDocument();
    // 加载中不算无效项目，不渲染空态
    expect(screen.queryByTestId("project-not-found")).not.toBeInTheDocument();
  });

  it("opens create version dialog with the active project id", async () => {
    const user = userEvent.setup();

    renderWithRoute(<VersionsPage />, {
      path: "/projects/:id/versions",
      initialEntry: "/projects/proj-1/versions",
    });

    await user.click(screen.getByRole("button", { name: /创建版本/i }));

    expect(mockVersionFormDialog).toHaveBeenCalledWith(
      expect.objectContaining({
        open: true,
        projectId: "proj-1",
      }),
    );
  });

  it("renders the project-not-found empty state for an invalid project id without requesting data", () => {
    mockProjectsList([
      {
        id: "other",
        user_id: "user-1",
        name: "Other",
        created_at: "2026-04-06T09:00:00Z",
        updated_at: "2026-04-06T09:00:00Z",
      },
    ]);
    vi.mocked(useVersions).mockClear();

    renderWithRoute(<VersionsPage />, {
      path: "/projects/:id/versions",
      initialEntry: "/projects/proj-1/versions",
    });

    expect(screen.getByText("项目不存在或无权访问")).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "返回项目列表" }),
    ).toHaveAttribute("href", "/projects");
    // 无效项目不请求数据，也不静默换项目
    expect(useVersions).not.toHaveBeenCalled();
    expect(screen.queryByText("v1.2.0")).not.toBeInTheDocument();
  });
});
