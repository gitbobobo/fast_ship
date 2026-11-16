import { render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { Sidebar } from "@/components/layout/sidebar";
import { useAuthStore } from "@/lib/store/auth-store";
import { useLastBotStore } from "@/lib/store/last-bot-store";
import { useSidebarStore } from "@/lib/store/sidebar-store";

vi.mock("@/components/user-nav", () => ({
  UserNav: () => <div data-testid="user-nav" />,
}));

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

// INT-157：「机器人」入口跳转到 lastBotId 对应的具体聊天页，但激活态
// 仍应按 /bots 分区判定——列表页或别的机器人详情页下菜单项保持高亮
// （NavLink 对改写后的 to 只做单页前缀匹配，会丢分区激活态）。记忆
// 按 userId 绑定：另一账号登录时读取不到上一账号的 lastBot。
function renderSidebar(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Sidebar />
    </MemoryRouter>,
  );
}

describe("Sidebar 机器人入口", () => {
  beforeEach(() => {
    useAuthStore.setState({ user });
    useLastBotStore.setState({ byUser: { "u-1": "bot-1" } });
    useSidebarStore.setState({ collapsed: false });
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
    renderSidebar("/issues");
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
