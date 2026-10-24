import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useLocation } from "react-router";
import { vi } from "vitest";
import ProjectsPage from "@/routes/projects/index";
import { renderWithRoute } from "@/test/render";
import { useProjects, useDeleteProject, useCreateProject, useUpdateProject, useProject, useShelveProject, useUnshelveProject } from "@/lib/hooks/use-projects";

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
  useShelveProject: vi.fn(),
  useUnshelveProject: vi.fn(),
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

    vi.mocked(useShelveProject).mockReturnValue({
      mutateAsync: vi.fn(),
      isPending: false,
    } as unknown as ReturnType<typeof useShelveProject>);

    vi.mocked(useUnshelveProject).mockReturnValue({
      mutateAsync: vi.fn(),
      isPending: false,
    } as unknown as ReturnType<typeof useUnshelveProject>);
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

  describe("搁置", () => {
    const shelvedItem = {
      id: "p3",
      user_id: "u1",
      name: "Dormant Tool",
      description: "暂停的项目",
      github_owner: "",
      github_repo: "",
      latest_version: null,
      issue_count: 4,
      shelved_at: "2026-10-10T00:00:00Z",
      shelved_until: null,
      is_shelved: true,
      created_at: "2026-04-06T09:00:00Z",
      updated_at: "2026-04-06T09:00:00Z",
    };

    function mockProjectsWithShelved() {
      const items = [
        {
          id: "p1",
          user_id: "u1",
          name: "Alpha App",
          description: "iOS release pipeline",
          github_owner: "acme",
          github_repo: "alpha",
          latest_version: null,
          shelved_at: null,
          shelved_until: null,
          is_shelved: false,
          created_at: "2026-04-06T09:00:00Z",
          updated_at: "2026-04-06T09:00:00Z",
        },
        shelvedItem,
      ];
      vi.mocked(useProjects).mockReturnValue({
        data: { items, total: items.length, page: 1, page_size: 100 },
        isLoading: false,
      } as unknown as ReturnType<typeof useProjects>);
    }

    it("搁置项目收进默认折叠的分组，展开后可见且带标记", async () => {
      const user = userEvent.setup();
      mockProjectsWithShelved();
      renderWithRoute(<ProjectsPage />, { path: "/projects", initialEntry: "/projects" });

      // 默认折叠：日常网格只有未搁置项目
      expect(screen.getByText("Alpha App")).toBeInTheDocument();
      expect(screen.queryByText("Dormant Tool")).not.toBeInTheDocument();

      const toggle = screen.getByRole("button", { name: /已搁置 \(1\)/ });
      expect(toggle).toHaveAttribute("aria-expanded", "false");
      await user.click(toggle);
      expect(toggle).toHaveAttribute("aria-expanded", "true");

      expect(screen.getByText("Dormant Tool")).toBeInTheDocument();
      // 无限期搁置的卡片显示「已搁置」徽标
      expect(screen.getByText("已搁置")).toBeInTheDocument();
    });

    it("有截止日期的搁置卡片显示「搁置至 YYYY-MM-DD」", async () => {
      const user = userEvent.setup();
      mockProjectsWithShelved();
      vi.mocked(useProjects).mockReturnValue({
        data: {
          items: [
            {
              ...shelvedItem,
              shelved_until: "2026-11-20T12:00:00Z",
            },
          ],
          total: 1,
          page: 1,
          page_size: 100,
        },
        isLoading: false,
      } as unknown as ReturnType<typeof useProjects>);
      renderWithRoute(<ProjectsPage />, { path: "/projects", initialEntry: "/projects" });

      await user.click(screen.getByRole("button", { name: /已搁置 \(1\)/ }));
      expect(screen.getByText(/^搁置至 \d{4}-\d{2}-\d{2}$/)).toBeInTheDocument();
    });

    it("未搁置卡片菜单提供「搁置…」入口，确认后调用搁置 mutation", async () => {
      const user = userEvent.setup();
      const shelveMutateAsync = vi.fn().mockResolvedValue(undefined);
      vi.mocked(useShelveProject).mockReturnValue({
        mutateAsync: shelveMutateAsync,
        isPending: false,
      } as unknown as ReturnType<typeof useShelveProject>);
      mockProjectsWithShelved();
      renderWithRoute(<ProjectsPage />, { path: "/projects", initialEntry: "/projects" });

      await user.click(screen.getAllByLabelText("更多操作")[0]);
      await user.click(screen.getByRole("menuitem", { name: /^搁置/ }));

      const dialog = await screen.findByRole("dialog", { name: "搁置项目" });
      expect(within(dialog).getByLabelText(/截止日期/)).toBeInTheDocument();

      // 留空截止日期按无限期搁置
      await user.click(within(dialog).getByRole("button", { name: "确认搁置" }));
      expect(shelveMutateAsync).toHaveBeenCalledWith({ id: "p1", until: undefined });
    });

    it("搁置对话框选了日期后按当天结束时间提交", async () => {
      const user = userEvent.setup();
      const shelveMutateAsync = vi.fn().mockResolvedValue(undefined);
      vi.mocked(useShelveProject).mockReturnValue({
        mutateAsync: shelveMutateAsync,
        isPending: false,
      } as unknown as ReturnType<typeof useShelveProject>);
      mockProjectsWithShelved();
      renderWithRoute(<ProjectsPage />, { path: "/projects", initialEntry: "/projects" });

      await user.click(screen.getAllByLabelText("更多操作")[0]);
      await user.click(screen.getByRole("menuitem", { name: /^搁置/ }));
      const dialog = await screen.findByRole("dialog", { name: "搁置项目" });

      fireEvent.change(within(dialog).getByLabelText(/截止日期/), {
        target: { value: "2027-01-15" },
      });
      await user.click(within(dialog).getByRole("button", { name: "确认搁置" }));

      expect(shelveMutateAsync).toHaveBeenCalledWith({
        id: "p1",
        until: expect.any(String),
      });
      const until = shelveMutateAsync.mock.calls[0][0].until as string;
      const parsed = new Date(until);
      // 截止日期语义为「搁置到当天结束」
      expect(parsed.getFullYear()).toBe(2027);
      expect(parsed.getMonth()).toBe(0);
      expect(parsed.getDate()).toBe(15);
      expect(parsed.getHours()).toBe(23);
      expect(parsed.getMinutes()).toBe(59);
    });

    it("搁置卡片菜单提供「取消搁置」入口", async () => {
      const user = userEvent.setup();
      const unshelveMutateAsync = vi.fn().mockResolvedValue(undefined);
      vi.mocked(useUnshelveProject).mockReturnValue({
        mutateAsync: unshelveMutateAsync,
        isPending: false,
      } as unknown as ReturnType<typeof useUnshelveProject>);
      mockProjectsWithShelved();
      renderWithRoute(<ProjectsPage />, { path: "/projects", initialEntry: "/projects" });

      await user.click(screen.getByRole("button", { name: /已搁置 \(1\)/ }));
      // 展开后第二个「更多操作」属于搁置卡片
      const menuButtons = screen.getAllByLabelText("更多操作");
      await user.click(menuButtons[1]);
      await user.click(screen.getByRole("menuitem", { name: /取消搁置/ }));
      expect(unshelveMutateAsync).toHaveBeenCalledWith("p3");
    });
  });
});
