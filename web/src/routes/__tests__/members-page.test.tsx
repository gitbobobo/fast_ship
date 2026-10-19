import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import MembersPage from "@/routes/settings/members";
import { signInAs, teardownAuth } from "@/test/auth";
import { renderWithRoute } from "@/test/render";
import { useAuthStore } from "@/lib/store/auth-store";
import { toast } from "sonner";

const { listMembers, updateMemberRole, listInvites, createInvite, revokeInvite, getSettings } =
  vi.hoisted(() => ({
    listMembers: vi.fn(),
    updateMemberRole: vi.fn(),
    listInvites: vi.fn(),
    createInvite: vi.fn(),
    revokeInvite: vi.fn(),
    getSettings: vi.fn(),
  }));

vi.mock("sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));

vi.mock("@/lib/api/members", () => ({
  memberApi: {
    listMembers,
    updateMemberRole,
    listInvites,
    createInvite,
    revokeInvite,
  },
}));

// 邀请链接的基址读部署设置（public_url）：mock 掉真实请求。
vi.mock("@/lib/api/deployment", () => ({
  deploymentApi: { getSettings },
}));

// Header 渲染通知铃（INT-90）：与 integrations-page.test.tsx 同一口径 mock。
vi.mock("@/lib/hooks/use-notifications", () => ({
  NOTIFICATION_UNREAD_POLL_MS: 30000,
  useNotifications: vi.fn(() => ({ data: [] })),
  useUnreadNotificationCount: vi.fn(() => ({ data: 0 })),
  useMarkNotificationRead: vi.fn(() => ({ mutate: vi.fn(), mutateAsync: vi.fn() })),
  useMarkAllNotificationsRead: vi.fn(() => ({ mutate: vi.fn(), mutateAsync: vi.fn() })),
}));

function member(id: string, role: UserRole): Member {
  return {
    id,
    username: id,
    email: `${id}@example.com`,
    avatar_url: "",
    role,
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
  };
}

function invite(overrides: Partial<Invite> = {}): Invite {
  return {
    id: "inv-1",
    expires_at: "2026-10-22T00:00:00Z",
    revoked_at: null,
    used_at: null,
    used_by: null,
    created_at: "2026-10-19T00:00:00Z",
    ...overrides,
  };
}

// 后端列表端点统一 `{items}` 信封（与 /api/environments 同形）：
// mock 一律按真实 wire 形状给，前端解包错一处这里就会白屏或恒空。
function membersPage(items: Member[]) {
  return { data: { items } };
}

function invitesPage(items: Invite[]) {
  return { data: { items } };
}

function renderPage() {
  return renderWithRoute(<MembersPage />, {
    path: "/settings/members",
    initialEntry: "/settings/members",
  });
}

afterEach(() => {
  teardownAuth();
  vi.clearAllMocks();
});

describe("MembersPage admin view", () => {
  beforeEach(() => {
    signInAs("admin-1", "admin");
    listInvites.mockResolvedValue(invitesPage([]));
    getSettings.mockResolvedValue({
      data: { public_url: "", public_url_source: "none", updated_at: null },
    });
  });

  it("renders member and invite rows from the {items} envelope", async () => {
    // 回归用例：列表响应是 {data:{items:[...]}} 时页面必须渲染出成员行与邀请行。
    // 此前 hooks 直接把 data 当数组用，真实形状下 members.filter 抛 TypeError
    // （整站白屏）、invites?.length 恒 undefined（永远「暂无邀请记录」）。
    listMembers.mockResolvedValue(membersPage([member("admin-1", "admin")]));
    listInvites.mockResolvedValue(invitesPage([invite()]));

    renderPage();

    // 成员行：用户名与邮箱都在表格里。
    expect(await screen.findByText("admin-1")).toBeInTheDocument();
    expect(screen.getByText("admin-1@example.com")).toBeInTheDocument();
    // 邀请行：有效邀请带状态徽标与撤销按钮，而不是「暂无邀请记录」。
    expect(await screen.findByText("有效")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "撤销" }),
    ).toBeInTheDocument();
    expect(screen.queryByText("暂无邀请记录")).not.toBeInTheDocument();
  });

  it("disables demotion for the last admin and explains why", async () => {
    listMembers.mockResolvedValue(
      membersPage([member("admin-1", "admin"), member("u2", "member")]),
    );
    renderPage();

    const row = (await screen.findByText("admin-1")).closest("tr")!;
    const demote = row.querySelector("button")!;
    expect(demote).toBeDisabled();
    expect(
      screen.getByText("最后一个管理员，不能降级"),
    ).toBeInTheDocument();

    // 普通成员行仍可升级
    const memberRow = screen.getByText("u2").closest("tr")!;
    expect(memberRow.querySelector("button")).toBeEnabled();
  });

  it("updates the auth store role when demoting yourself", async () => {
    listMembers.mockResolvedValue(
      membersPage([member("admin-1", "admin"), member("admin-2", "admin")]),
    );
    updateMemberRole.mockResolvedValue({ data: member("admin-1", "member") });
    const user = userEvent.setup();
    renderPage();

    const row = (await screen.findByText("admin-1")).closest("tr")!;
    await user.click(row.querySelector("button")!);

    await waitFor(() =>
      expect(useAuthStore.getState().user?.role).toBe("member"),
    );
    // 侧栏等依赖 auth store 的管理入口立即收敛，不必等 /auth/me 回读。
    await waitFor(() => expect(toast.success).toHaveBeenCalled());
  });

  it("allows demoting an admin when another admin exists", async () => {
    listMembers.mockResolvedValue(
      membersPage([member("admin-1", "admin"), member("admin-2", "admin")]),
    );
    updateMemberRole.mockResolvedValue({ data: member("admin-2", "member") });
    const user = userEvent.setup();
    renderPage();

    const row = (await screen.findByText("admin-2")).closest("tr")!;
    const demote = row.querySelector("button")!;
    expect(demote).toBeEnabled();
    await user.click(demote);

    await waitFor(() =>
      expect(updateMemberRole).toHaveBeenCalledWith("admin-2", {
        role: "member",
      }),
    );
  });

  it("creates an invite and shows the one-time link", async () => {
    listMembers.mockResolvedValue(membersPage([member("admin-1", "admin")]));
    createInvite.mockResolvedValue({
      data: { ...invite(), token: "secret-token" },
    });
    const user = userEvent.setup();
    renderPage();

    await screen.findByText("admin-1");
    await user.click(screen.getByRole("button", { name: "创建邀请" }));

    await waitFor(() =>
      expect(createInvite).toHaveBeenCalledWith({ expires_in_hours: 72 }),
    );
    // public_url 未配置时回落当前站点源。
    const link = `${window.location.origin}/register?invite=secret-token`;
    expect(await screen.findByText(link)).toBeInTheDocument();
  });

  it("builds the invite link from the deployment public_url when set", async () => {
    getSettings.mockResolvedValue({
      data: {
        public_url: "https://fs.example.com",
        public_url_source: "database",
        updated_at: "2026-10-01T00:00:00Z",
      },
    });
    listMembers.mockResolvedValue(membersPage([member("admin-1", "admin")]));
    createInvite.mockResolvedValue({
      data: { ...invite(), token: "secret-token" },
    });
    const user = userEvent.setup();
    renderPage();

    await screen.findByText("admin-1");
    await user.click(screen.getByRole("button", { name: "创建邀请" }));

    // 邀请链接优先用部署 public_url，而不是浏览器当前源。
    expect(
      await screen.findByText(
        "https://fs.example.com/register?invite=secret-token",
      ),
    ).toBeInTheDocument();
  });

  it("keeps 创建邀请 disabled until the deployment public_url is known", async () => {
    // public_url 未就绪时创建会用一个可能对收件人不可达的地址，而 token 只回一次。
    let resolveSettings: (value: unknown) => void = () => {};
    getSettings.mockReturnValue(
      new Promise((resolve) => {
        resolveSettings = resolve;
      }),
    );
    listMembers.mockResolvedValue(membersPage([member("admin-1", "admin")]));
    renderPage();

    await screen.findByText("admin-1");
    const createButton = screen.getByRole("button", { name: "创建邀请" });
    await waitFor(() => expect(createButton).toBeDisabled());

    resolveSettings({
      data: { public_url: "", public_url_source: "none", updated_at: null },
    });
    await waitFor(() => expect(createButton).toBeEnabled());
  });

  it("keeps 创建邀请 disabled when the deployment settings query failed", async () => {
    // 查询重试用尽后 isLoading 变 false 但 data 仍为空：这时不能放行，
    // 否则一次性邀请会用当前站点源拼链接。
    getSettings.mockRejectedValue(new Error("settings unavailable"));
    listMembers.mockResolvedValue(membersPage([member("admin-1", "admin")]));
    renderPage();

    await screen.findByText("admin-1");
    const createButton = screen.getByRole("button", { name: "创建邀请" });
    await waitFor(() => expect(createButton).toBeDisabled());
    expect(createInvite).not.toHaveBeenCalled();
  });

  it("revokes an active invite after confirmation", async () => {
    listMembers.mockResolvedValue(membersPage([member("admin-1", "admin")]));
    listInvites.mockResolvedValue(invitesPage([invite()]));
    revokeInvite.mockResolvedValue({ data: null });
    const user = userEvent.setup();
    renderPage();

    await user.click(await screen.findByRole("button", { name: "撤销" }));
    await user.click(
      await screen.findByRole("button", { name: "确认撤销" }),
    );

    await waitFor(() => expect(revokeInvite).toHaveBeenCalledWith("inv-1"));
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith("邀请已撤销"),
    );
  });

  it("shows invite status without a revoke action for used invites", async () => {
    listMembers.mockResolvedValue(membersPage([member("admin-1", "admin")]));
    listInvites.mockResolvedValue(
      invitesPage([invite({ used_at: "2026-10-20T00:00:00Z", used_by: "u9" })]),
    );
    renderPage();

    expect(await screen.findByText("已使用")).toBeInTheDocument();
    expect(screen.getByText("u9")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "撤销" }),
    ).not.toBeInTheDocument();
  });
});

describe("MembersPage member view", () => {
  it("shows an admin-required empty state for non-admins", async () => {
    signInAs("u2", "member");
    listMembers.mockResolvedValue(membersPage([]));
    listInvites.mockResolvedValue(invitesPage([]));

    renderPage();

    expect(await screen.findByText(/需要管理员权限/)).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "创建邀请" }),
    ).not.toBeInTheDocument();
  });
});
