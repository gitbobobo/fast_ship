import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import GeneralPage from "@/routes/settings/general";
import { renderWithRoute } from "@/test/render";
import { authApi } from "@/lib/api/auth";
import { useAuthStore } from "@/lib/store/auth-store";

vi.mock("sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));

// SettingsPageShell 直接渲染 Header；这里只关心时区卡片，Header 不需要。
vi.mock("@/components/layout/header", () => ({
  Header: () => null,
}));

vi.mock("@/lib/api/auth", () => ({
  authApi: { me: vi.fn(), updateMe: vi.fn() },
}));

function fakeUser(overrides: Partial<User> = {}): User {
  return {
    id: "user-1",
    username: "godbobo",
    email: "godbobo@example.com",
    avatar_url: "",
    role: "member",
    timezone: "",
    effective_timezone: "UTC",
    created_at: "2026-04-06T10:00:00Z",
    updated_at: "2026-04-06T10:00:00Z",
    ...overrides,
  };
}

function renderPage() {
  return renderWithRoute(<GeneralPage />, {
    path: "/settings/general",
    initialEntry: "/settings/general",
  });
}

describe("Settings General Page - timezone", () => {
  afterEach(() => {
    vi.clearAllMocks();
    useAuthStore.setState({ user: null });
  });

  it("渲染时区卡片并显示当前时区与该时区的当前时间", () => {
    useAuthStore.setState({ user: fakeUser({ timezone: "Asia/Shanghai" }) });

    renderPage();

    expect(screen.getByText("时区")).toBeInTheDocument();
    expect(
      screen.getByText(
        "机器人理解「每天 9 点」这类时间、新建定时任务和夜间整理都按此时区计算",
      ),
    ).toBeInTheDocument();
    // 显式选了时区时不再重复一行「当前生效时区」，下拉本身就是事实来源。
    expect(screen.queryByText("当前生效时区：")).not.toBeInTheDocument();
    expect(screen.getByText("Asia/Shanghai (UTC+8)")).toBeInTheDocument();
    // 当前时间不写死：按所选时区渲染，形如 2026-10-22 13:05 (UTC+8)。
    expect(
      screen.getByText(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2} \(UTC\+8\)$/),
    ).toBeInTheDocument();
  });

  it("未设置时区时显示服务端算出的生效时区与它的当前时间", () => {
    // 服务端按「用户设置 → 部署配置 → UTC」算出生效时区并随 /auth/me 返回，
    // 前端直接用，不需要自己拼回退链，也不会把本机时间当成机器人的时间。
    useAuthStore.setState({
      user: fakeUser({ timezone: "", effective_timezone: "Asia/Tokyo" }),
    });

    renderPage();

    expect(screen.getByText("Asia/Tokyo（来自部署配置）")).toBeInTheDocument();
    expect(screen.getByText("该时区当前时间：")).toBeInTheDocument();
    expect(screen.getByText(/\(UTC\+9\)$/)).toBeInTheDocument();
    expect(
      screen.getByRole("combobox", { name: "时区" }),
    ).toHaveTextContent("默认（跟随部署配置）");
  });

  it("生效时区缺失时只说规则，不拿本机时区冒充部署时区，并补拉一次 /auth/me", () => {
    useAuthStore.setState({
      user: fakeUser({ timezone: "", effective_timezone: undefined }),
    });
    const refreshed = fakeUser({
      timezone: "",
      effective_timezone: "Europe/Berlin",
    });
    vi.mocked(authApi.me).mockResolvedValue({
      data: refreshed,
    } as Awaited<ReturnType<typeof authApi.me>>);

    renderPage();

    // 先按保守分支显示规则，绝不写「<本机时区>（来自部署配置）」。
    expect(
      screen.getByText("机器人按部署配置的时区计算，部署未配置时区时为 UTC"),
    ).toBeInTheDocument();
    expect(screen.queryByText(/（来自部署配置）/)).not.toBeInTheDocument();

    // 同时补拉 /auth/me 写回 store，拿到真实生效时区后正常显示。
    return waitFor(() =>
      expect(useAuthStore.getState().user?.effective_timezone).toBe(
        "Europe/Berlin",
      ),
    );
  });

  it("切换时区后调用 updateMe 并更新 store", async () => {
    useAuthStore.setState({ user: fakeUser({ timezone: "Asia/Shanghai" }) });
    const updated = fakeUser({ timezone: "Asia/Tokyo" });
    vi.mocked(authApi.updateMe).mockResolvedValue({
      data: updated,
    } as Awaited<ReturnType<typeof authApi.updateMe>>);

    const user = userEvent.setup();
    renderPage();

    await user.click(screen.getByRole("combobox", { name: "时区" }));
    const listbox = await screen.findByRole("listbox");
    await user.click(
      within(listbox).getByRole("option", { name: "Asia/Tokyo (UTC+9)" }),
    );

    await waitFor(() =>
      expect(authApi.updateMe).toHaveBeenCalledWith({ timezone: "Asia/Tokyo" }),
    );
    await waitFor(() =>
      expect(useAuthStore.getState().user?.timezone).toBe("Asia/Tokyo"),
    );
    // 下拉展开时选项里有同名文本，这里只断言收起态触发器上的值。
    expect(screen.getByRole("combobox", { name: "时区" })).toHaveTextContent(
      "Asia/Tokyo (UTC+9)",
    );
  });

  it("选择默认入口时清空时区（回落部署配置）", async () => {
    useAuthStore.setState({ user: fakeUser({ timezone: "Asia/Tokyo" }) });
    const updated = fakeUser({ timezone: "" });
    vi.mocked(authApi.updateMe).mockResolvedValue({
      data: updated,
    } as Awaited<ReturnType<typeof authApi.updateMe>>);

    const user = userEvent.setup();
    renderPage();

    await user.click(screen.getByRole("combobox", { name: "时区" }));
    const listbox = await screen.findByRole("listbox");
    await user.click(
      within(listbox).getByRole("option", { name: "默认（跟随部署配置）" }),
    );

    await waitFor(() =>
      expect(authApi.updateMe).toHaveBeenCalledWith({ timezone: "" }),
    );
    await waitFor(() => expect(useAuthStore.getState().user?.timezone).toBe(""));
  });
});
