import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useLocation } from "react-router";
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

// 观察地址栏：深链参数应被消费后移除
function LocationProbe() {
  return <output data-testid="location-search">{useLocation().search}</output>;
}

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

  it("按 useProjects 给出的顺序展示卡片，并显示问题总数", () => {
    vi.mocked(useProjects).mockReturnValue({
      data: {
        items: [
          {
            id: "few",
            user_id: "u1",
            name: "Few Issues",
            description: "",
            github_owner: "",
            github_repo: "",
            latest_version: null,
            issue_count: 2,
            created_at: "2026-04-06T09:00:00Z",
            updated_at: "2026-04-06T09:00:00Z",
          },
          {
            id: "many",
            user_id: "u1",
            name: "Many Issues",
            description: "",
            github_owner: "",
            github_repo: "",
            latest_version: null,
            issue_count: 30,
            created_at: "2026-04-05T09:00:00Z",
            updated_at: "2026-04-05T09:00:00Z",
          },
          {
            id: "none",
            user_id: "u1",
            name: "No Count",
            description: "",
            github_owner: "",
            github_repo: "",
            latest_version: null,
            created_at: "2026-04-07T09:00:00Z",
            updated_at: "2026-04-07T09:00:00Z",
          },
        ],
        total: 3,
        page: 1,
        page_size: 100,
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useProjects>);

    renderWithRoute(<ProjectsPage />, { path: "/projects", initialEntry: "/projects" });

    const names = screen
      .getAllByRole("heading", { level: 3 })
      .map((heading) => heading.textContent);
    // 排序由 useProjects 统一提供（Issue 总数降序，含已关闭），页面不再自己排：
    // 传进来什么顺序就渲染什么顺序
    expect(names).toEqual(["Few Issues", "Many Issues", "No Count"]);
    // 卡片显示问题数，让排序依据可见；计数缺失显示 0
    expect(screen.getByText("30 个问题")).toBeInTheDocument();
    expect(screen.getByText("2 个问题")).toBeInTheDocument();
    expect(screen.getByText("0 个问题")).toBeInTheDocument();
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

  it("opens the edit dialog at the T3 section for ?edit=&section=t3 and clears the params", async () => {
    const scrollSpy = vi
      .spyOn(window.HTMLElement.prototype, "scrollIntoView")
      .mockImplementation(() => {});

    renderWithRoute(
      <>
        <ProjectsPage />
        <LocationProbe />
      </>,
      { path: "/projects", initialEntry: "/projects?edit=p1&section=t3" },
    );

    const dialog = await screen.findByRole("dialog", { name: "编辑项目" });
    expect(
      await screen.findByRole("region", { name: "T3 绑定" }),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(scrollSpy).toHaveBeenCalledWith({ block: "start" }),
    );
    // 深链参数被消费一次后即移除，刷新/回退不再重开弹框
    await waitFor(() =>
      expect(screen.getByTestId("location-search")).toHaveTextContent(""),
    );
    expect(dialog).toBeInTheDocument();
  });

  it("ignores ?edit= when the project does not exist", async () => {
    renderWithRoute(
      <>
        <ProjectsPage />
        <LocationProbe />
      </>,
      { path: "/projects", initialEntry: "/projects?edit=ghost&section=t3" },
    );

    await waitFor(() => expect(screen.getByText("Alpha App")).toBeInTheDocument());
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    // 目标项目不存在时参数保留，便于排查
    expect(screen.getByTestId("location-search")).toHaveTextContent(
      "edit=ghost",
    );
  });

  it("opens the plain edit dialog for ?edit= without a section", async () => {
    const scrollSpy = vi
      .spyOn(window.HTMLElement.prototype, "scrollIntoView")
      .mockImplementation(() => {});

    renderWithRoute(<ProjectsPage />, {
      path: "/projects",
      initialEntry: "/projects?edit=p2",
    });

    expect(
      await screen.findByRole("dialog", { name: "编辑项目" }),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(screen.getByRole("region", { name: "T3 绑定" })).toBeInTheDocument(),
    );
    expect(scrollSpy).not.toHaveBeenCalled();
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
