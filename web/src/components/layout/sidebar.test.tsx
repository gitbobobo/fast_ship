import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { MobileNav, Sidebar } from "@/components/layout/sidebar";
import { useProjects } from "@/lib/hooks/use-projects";
import { useAuthStore } from "@/lib/store/auth-store";
import { useLastBotStore } from "@/lib/store/last-bot-store";
import { useProjectPreferenceStore } from "@/lib/store/project-preference-store";
import { useSidebarStore } from "@/lib/store/sidebar-store";
import { registerNavigationGuard } from "@/lib/navigation-guard";
import { withTestQueryClient } from "@/test/render";

vi.mock("@/components/user-nav", () => ({
  UserNav: () => <div data-testid="user-nav" />,
}));

// 侧边栏的项目列表/切换器依赖 useProjects，测试直接给定数据。
vi.mock("@/lib/hooks/use-projects", () => ({
  useProjects: vi.fn(),
}));

const mockedUseProjects = vi.mocked(useProjects);

const user = {
  id: "u-1",
  username: "u1",
  email: "u1@example.com",
  avatar_url: "",
  role: "member",
  timezone: "",
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
} as const;

// 顺序即 useProjects 给出的展示顺序（Issue 总数降序），fixture 不再排序
const defaultProjects = [
  { id: "p-alpha", name: "Alpha" },
  { id: "p-beta", name: "Beta" },
] as Project[];

const shelvedProject = {
  id: "p-shelved",
  name: "Shelved App",
  is_shelved: true,
} as Project;

// INT-157：「机器人」入口跳转到 lastBotId 对应的具体聊天页，但激活态
// 仍应按 /bots 分区判定——列表页或别的机器人详情页下菜单项保持高亮
// （NavLink 对改写后的 to 只做单页前缀匹配，会丢分区激活态）。记忆
// 按 userId 绑定：另一账号登录时读取不到上一账号的 lastBot。
// LocationProbe 记录最新 location，供切换跳转断言使用。
function renderSidebar(path: string, projects: Project[] = defaultProjects) {
  mockedUseProjects.mockReturnValue({
    data: { items: projects, total: projects.length },
    isSuccess: true,
  } as unknown as ReturnType<typeof useProjects>);

  let location: ReturnType<typeof useLocation> | undefined;
  function LocationProbe() {
    location = useLocation();
    return null;
  }

  const utils = render(
    withTestQueryClient(
      <MemoryRouter initialEntries={[path]}>
        <Sidebar />
        <LocationProbe />
      </MemoryRouter>,
    ),
  );
  return { ...utils, currentLocation: () => location };
}

async function pickProject(name: string | RegExp) {
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: /切换项目：/ }));
  const popup = await screen.findByRole("dialog", { name: "切换项目" });
  await user.click(within(popup).getByRole("button", { name }));
}

describe("Sidebar 机器人入口", () => {
  beforeEach(() => {
    useAuthStore.setState({ user });
    useLastBotStore.setState({ byUser: { "u-1": "bot-1" } });
    useSidebarStore.setState({ collapsed: false });
    useProjectPreferenceStore.setState({ lastSelectedProjectId: null });
  });

  it("有 lastBotId 时跳转目标改写为对应聊天页", () => {
    renderSidebar("/dashboard");
    const link = screen.getByRole("link", { name: "机器人" });
    expect(link).toHaveAttribute("href", "/bots/bot-1");
  });

  it.each(["/bots", "/bots/other-bot", "/bots/bot-1"])(
    "位于 %s 时机器人菜单项保持分区激活态",
    (path) => {
      renderSidebar(path);
      const link = screen.getByRole("link", { name: "机器人" });
      expect(link.className).toContain("bg-primary");
    },
  );

  it("离开机器人分区后不激活", () => {
    renderSidebar("/projects/p-alpha/issues");
    const link = screen.getByRole("link", { name: "机器人" });
    expect(link.className).not.toContain("bg-primary");
  });

  it("记忆属于别的账号时入口退回 /bots 列表", () => {
    useLastBotStore.setState({ byUser: { "u-2": "bot-9" } });
    renderSidebar("/dashboard");
    const link = screen.getByRole("link", { name: "机器人" });
    expect(link).toHaveAttribute("href", "/bots");
  });

  it("另一账号写入记忆不覆盖当前账号的（per-user 槽位）", () => {
    useAuthStore.setState({ user: { ...user, id: "u-2" } });
    useLastBotStore.getState().setLastBot("bot-9");
    useAuthStore.setState({ user });
    renderSidebar("/dashboard");
    const link = screen.getByRole("link", { name: "机器人" });
    expect(link).toHaveAttribute("href", "/bots/bot-1");
  });
});

describe("Sidebar 导航分组", () => {
  beforeEach(() => {
    useAuthStore.setState({ user });
    useSidebarStore.setState({ collapsed: false });
    useProjectPreferenceStore.setState({ lastSelectedProjectId: null });
  });

  it("展开态渲染组标题，且无通知与套餐额度项", () => {
    renderSidebar("/dashboard");

    const nav = screen.getByRole("navigation");
    expect(within(nav).getByText("项目内")).toBeInTheDocument();
    expect(within(nav).getByText("智能体")).toBeInTheDocument();
    expect(within(nav).queryByRole("link", { name: "通知" })).not.toBeInTheDocument();
    expect(within(nav).queryByRole("link", { name: "套餐额度" })).not.toBeInTheDocument();
  });

  it("收起态不渲染组标题，改渲染分隔线", () => {
    useSidebarStore.setState({ collapsed: true });
    renderSidebar("/dashboard");

    const nav = screen.getByRole("navigation");
    expect(within(nav).queryByText("项目内")).not.toBeInTheDocument();
    expect(within(nav).queryByText("智能体")).not.toBeInTheDocument();
    expect(
      within(nav).getAllByTestId("sidebar-group-separator"),
    ).toHaveLength(2);
  });
});

describe("Sidebar 项目切换器与项目内导航（INT-237 PR2）", () => {
  beforeEach(() => {
    useAuthStore.setState({ user });
    useSidebarStore.setState({ collapsed: false });
    useProjectPreferenceStore.setState({ lastSelectedProjectId: null });
  });

  it("展开态「项目内」六项指向当前项目的分区路径", () => {
    renderSidebar("/projects/p-alpha/issues");

    const expected = [
      ["问题", "/projects/p-alpha/issues"],
      ["看板", "/projects/p-alpha/board"],
      ["版本", "/projects/p-alpha/versions"],
      ["文档", "/projects/p-alpha/documents"],
      ["截图", "/projects/p-alpha/screenshots"],
      ["日志", "/projects/p-alpha/logs"],
    ] as const;
    for (const [label, href] of expected) {
      expect(screen.getByRole("link", { name: label })).toHaveAttribute(
        "href",
        href,
      );
    }
  });

  it("全局页无路径项目时按偏好/兜底裁决当前项目", () => {
    renderSidebar("/dashboard");

    // 无偏好时兜底取首个未搁置项目
    expect(screen.getByRole("link", { name: "问题" })).toHaveAttribute(
      "href",
      "/projects/p-alpha/issues",
    );
  });

  it("项目级列表页上分区项直接高亮（不再依赖旧路由重定向）", () => {
    renderSidebar("/projects/p-alpha/issues?state=open");

    expect(screen.getByRole("link", { name: "问题" }).className).toContain(
      "bg-primary",
    );
  });

  it("日志分区在 runId 子页上保持激活（end 不精确匹配）", () => {
    renderSidebar("/projects/p-alpha/logs/run-1");

    expect(screen.getByRole("link", { name: "日志" }).className).toContain(
      "bg-primary",
    );
  });

  it("列表页切换项目：延续同名 section 且丢页内 query", async () => {
    const utils = renderSidebar("/projects/p-alpha/issues?state=open");

    await pickProject("Beta");

    expect(utils.currentLocation()?.pathname).toBe("/projects/p-beta/issues");
    expect(utils.currentLocation()?.search).toBe("");
  });

  it("导航守卫取消时切换器中止跳转，留在原页面", async () => {
    const confirmMock = vi.fn().mockReturnValue(false);
    vi.stubGlobal("confirm", confirmMock);
    const unregister = registerNavigationGuard(() => "有未保存的修改");
    const utils = renderSidebar("/projects/p-alpha/issues");

    try {
      await pickProject("Beta");

      expect(utils.currentLocation()?.pathname).toBe(
        "/projects/p-alpha/issues",
      );
      expect(confirmMock).toHaveBeenCalledWith("有未保存的修改");
      expect(
        useProjectPreferenceStore.getState().lastSelectedProjectId,
      ).not.toBe("p-beta");
    } finally {
      unregister();
    }
  });

  it("导航守卫确认后切换照常进行", async () => {
    vi.stubGlobal("confirm", vi.fn().mockReturnValue(true));
    const unregister = registerNavigationGuard(() => "有未保存的修改");
    const utils = renderSidebar("/projects/p-alpha/issues");

    try {
      await pickProject("Beta");

      expect(utils.currentLocation()?.pathname).toBe("/projects/p-beta/issues");
    } finally {
      unregister();
    }
  });

  it("侧栏链接点击同样受导航守卫拦截", async () => {
    const confirmMock = vi.fn().mockReturnValue(false);
    vi.stubGlobal("confirm", confirmMock);
    const unregister = registerNavigationGuard(() => "有未保存的修改");
    const utils = renderSidebar("/projects/p-alpha/documents");

    try {
      await userEvent.setup().click(screen.getByRole("link", { name: "问题" }));

      expect(utils.currentLocation()?.pathname).toBe(
        "/projects/p-alpha/documents",
      );
      expect(confirmMock).toHaveBeenCalledTimes(1);
    } finally {
      unregister();
    }
  });

  it("详情子页切换项目：回目标项目对应列表页", async () => {
    const utils = renderSidebar("/projects/p-alpha/issues/123?tab=detail");

    await pickProject("Beta");

    expect(utils.currentLocation()?.pathname).toBe("/projects/p-beta/issues");
    expect(utils.currentLocation()?.search).toBe("");
  });

  it("全局页切换项目：不跳转，只更新偏好并刷新链接", async () => {
    const utils = renderSidebar("/dashboard");

    await pickProject("Beta");

    expect(utils.currentLocation()?.pathname).toBe("/dashboard");
    expect(useProjectPreferenceStore.getState().lastSelectedProjectId).toBe(
      "p-beta",
    );
    // 偏好生效后「项目内」各项随之指向新项目
    expect(screen.getByRole("link", { name: "问题" })).toHaveAttribute(
      "href",
      "/projects/p-beta/issues",
    );
  });

  it("收起态触发器缩为首字母方块，tooltip 显示项目全名", async () => {
    useSidebarStore.setState({ collapsed: true });
    renderSidebar("/projects/p-alpha/issues");

    const trigger = screen.getByRole("button", { name: "切换项目：Alpha" });
    expect(trigger).toHaveTextContent("A");

    const user = userEvent.setup();
    await user.hover(trigger);
    expect(await screen.findByText("Alpha")).toBeInTheDocument();
  });

  it("当前项目为搁置项目（深链进入）时触发器正常显示并带搁置标记", () => {
    renderSidebar("/projects/p-shelved/issues", [
      ...defaultProjects,
      shelvedProject,
    ]);

    const trigger = screen.getByRole("button", { name: "切换项目：Shelved App" });
    expect(trigger).toHaveTextContent("Shelved App");
    expect(within(trigger).getByText("已搁置")).toBeInTheDocument();
    // 深链项目是路径裁决的当前项目，六项链接指向它
    expect(screen.getByRole("link", { name: "问题" })).toHaveAttribute(
      "href",
      "/projects/p-shelved/issues",
    );
  });

  it("无项目时切换器位置换成创建入口，「项目内」组隐藏", () => {
    renderSidebar("/dashboard", []);

    expect(screen.getByRole("link", { name: "创建项目" })).toHaveAttribute(
      "href",
      "/projects",
    );
    expect(screen.queryByRole("button", { name: /切换项目：/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "问题" })).not.toBeInTheDocument();
    expect(screen.queryByText("项目内")).not.toBeInTheDocument();
    // 智能体组不受影响
    expect(screen.getByRole("link", { name: "审查" })).toBeInTheDocument();
  });
});

describe("MobileNav 项目上下文与桌面侧边栏一致", () => {
  beforeEach(() => {
    useAuthStore.setState({ user });
    useSidebarStore.setState({ collapsed: false });
    useProjectPreferenceStore.setState({ lastSelectedProjectId: null });
    mockedUseProjects.mockReturnValue({
      data: { items: defaultProjects, total: defaultProjects.length },
      isSuccess: true,
    } as unknown as ReturnType<typeof useProjects>);
  });

  it("Sheet 打开后同样渲染切换器与项目内六项", async () => {
    render(
      withTestQueryClient(
        <MemoryRouter initialEntries={["/projects/p-alpha/issues"]}>
          <MobileNav />
        </MemoryRouter>,
      ),
    );

    const user = userEvent.setup();
    await user.click(screen.getByRole("button"));

    expect(
      await screen.findByRole("button", { name: "切换项目：Alpha" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "问题" })).toHaveAttribute(
      "href",
      "/projects/p-alpha/issues",
    );
    expect(screen.getByRole("link", { name: "日志" })).toHaveAttribute(
      "href",
      "/projects/p-alpha/logs",
    );
    expect(screen.getByRole("navigation")).toBeInTheDocument();
  });

  it("全局页切换项目后 Sheet 收起（不依赖 pathname 变化）", async () => {
    render(
      withTestQueryClient(
        <MemoryRouter initialEntries={["/dashboard"]}>
          <MobileNav />
        </MemoryRouter>,
      ),
    );

    const user = userEvent.setup();
    await user.click(screen.getByRole("button"));
    await screen.findByRole("button", { name: "切换项目：Alpha" });

    // 全局页分支只更新偏好、不改 pathname，Sheet 必须由 onNavigate 显式关闭
    await pickProject("Beta");

    expect(
      useProjectPreferenceStore.getState().lastSelectedProjectId,
    ).toBe("p-beta");
    expect(
      screen.queryByRole("button", { name: "切换项目：Alpha" }),
    ).not.toBeInTheDocument();
  });
});
