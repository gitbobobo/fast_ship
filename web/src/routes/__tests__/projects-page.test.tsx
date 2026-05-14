import { fireEvent, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import ProjectsPage from "@/routes/projects/index";
import { renderWithRoute } from "@/test/render";
import { useProjects, useDeleteProject, useCreateProject, useUpdateProject, useProject } from "@/lib/hooks/use-projects";

const { mockNavigate } = vi.hoisted(() => ({
  mockNavigate: vi.fn(),
}));

vi.mock("react-router", async (importOriginal) => {
  const actual = await importOriginal<typeof import("react-router")>();
  return {
    ...actual,
    useNavigate: () => mockNavigate,
  };
});

vi.mock("@/lib/hooks/use-projects", () => ({
  useProjects: vi.fn(),
  useDeleteProject: vi.fn(),
  useCreateProject: vi.fn(),
  useUpdateProject: vi.fn(),
  useProject: vi.fn(),
}));

describe("ProjectsPage", () => {
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

    vi.mocked(useDeleteProject).mockReturnValue({
      mutateAsync: vi.fn(),
    } as unknown as ReturnType<typeof useDeleteProject>);

    vi.mocked(useCreateProject).mockReturnValue({
      mutateAsync: vi.fn(),
    } as unknown as ReturnType<typeof useCreateProject>);

    vi.mocked(useUpdateProject).mockReturnValue({
      mutateAsync: vi.fn(),
    } as unknown as ReturnType<typeof useUpdateProject>);

    vi.mocked(useProject).mockReturnValue({
      data: undefined,
      isLoading: false,
    } as unknown as ReturnType<typeof useProject>);
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

  it("navigates to issue list when clicking a project card", async () => {
    renderWithRoute(<ProjectsPage />, { path: "/projects", initialEntry: "/projects" });

    const card = screen.getByText("Alpha App").closest("[class*='cursor-pointer']") as HTMLElement;
    expect(card).toBeTruthy();
    card.click();

    expect(mockNavigate).toHaveBeenCalledWith("/issues?project=p1");
  });

  it("opens dropdown menu and opens edit dialog", async () => {
    const user = userEvent.setup();
    renderWithRoute(<ProjectsPage />, { path: "/projects", initialEntry: "/projects" });

    const menuButtons = screen.getAllByLabelText("更多操作");
    expect(menuButtons.length).toBe(2);

    await user.click(menuButtons[0]);

    const editItem = screen.getByRole("menuitem", { name: /编辑/ });
    expect(editItem).toBeInTheDocument();

    await user.click(editItem);
    expect(screen.getByRole("dialog", { name: "编辑项目" })).toBeInTheDocument();
  });

  it("opens delete confirmation and deletes project", async () => {
    const user = userEvent.setup();
    const deleteMutateAsync = vi.fn().mockResolvedValue(undefined);
    vi.mocked(useDeleteProject).mockReturnValue({
      mutateAsync: deleteMutateAsync,
    } as unknown as ReturnType<typeof useDeleteProject>);

    renderWithRoute(<ProjectsPage />, { path: "/projects", initialEntry: "/projects" });

    const menuButtons = screen.getAllByLabelText("更多操作");
    await user.click(menuButtons[0]);

    const deleteItem = screen.getByRole("menuitem", { name: /删除/ });
    await user.click(deleteItem);

    expect(screen.getByText("确认删除项目?")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "确认删除" }));
    expect(deleteMutateAsync).toHaveBeenCalledWith("p1");
  });
});
