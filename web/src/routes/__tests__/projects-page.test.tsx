import { fireEvent, screen } from "@testing-library/react";
import { vi } from "vitest";
import ProjectsPage from "@/routes/projects/index";
import { renderWithRoute } from "@/test/render";
import { useProjects } from "@/lib/hooks/use-projects";

vi.mock("@/lib/hooks/use-projects", () => ({
  useProjects: vi.fn(),
}));

describe('ProjectsPage', () => {
  beforeEach(() => {
    vi.mocked(useProjects).mockReturnValue({
      data: {
        items: [
          {
            id: "p1",
            user_id: "u1",
            name: "Alpha App",
            description: "iOS release pipeline",
            github_owner: "acme",
            github_repo: "alpha",
            latest_version: {
              id: "v1",
              version_number: "v1.2.0",
              status: "shipped",
              created_at: "2026-04-06T10:00:00Z",
            },
            created_at: "2026-04-06T09:00:00Z",
            updated_at: "2026-04-06T09:00:00Z",
          },
          {
            id: "p2",
            user_id: "u1",
            name: "Beta Console",
            description: "Windows installer",
            github_owner: "acme",
            github_repo: "beta",
            latest_version: null,
            created_at: "2026-04-06T09:00:00Z",
            updated_at: "2026-04-06T09:00:00Z",
          },
        ],
        total: 2,
        page: 1,
        page_size: 100,
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useProjects>);
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it("renders latest version status and version number", () => {
    renderWithRoute(<ProjectsPage />, { path: "/projects", initialEntry: "/projects" });

    expect(screen.getByText("Alpha App")).toBeInTheDocument();
    expect(screen.getByText("最新版本 v1.2.0")).toBeInTheDocument();
    expect(screen.getByText("已发货")).toBeInTheDocument();
    expect(screen.getByText("未创建版本")).toBeInTheDocument();
  });

  it("filters projects by search input", () => {
    renderWithRoute(<ProjectsPage />, { path: "/projects", initialEntry: "/projects" });

    fireEvent.change(screen.getByPlaceholderText("搜索项目名、描述或仓库"), {
      target: { value: "windows" },
    });

    expect(screen.getByText("Beta Console")).toBeInTheDocument();
    expect(screen.queryByText("Alpha App")).not.toBeInTheDocument();
  });
});
