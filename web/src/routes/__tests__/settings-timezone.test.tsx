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
  authApi: { updateMe: vi.fn() },
}));

function fakeUser(overrides: Partial<User> = {}): User {
  return {
    id: "user-1",
    username: "godbobo",
    email: "godbobo@example.com",
    avatar_url: "",
    role: "member",
    timezone: "",
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

  it("未设置时区时显示跟随部署配置", () => {
    useAuthStore.setState({ user: fakeUser() });

    renderPage();

    expect(screen.getByText("跟随部署配置")).toBeInTheDocument();
    // 未设置时当前时间按本机时区展示，并标注来源，避免被当成本地时间出错。
    expect(screen.getByText("（按本机时区显示）")).toBeInTheDocument();
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
