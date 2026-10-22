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
      screen.getByText("机器人的定时任务与运行时间按此时区计算"),
    ).toBeInTheDocument();
    expect(screen.getByText("Asia/Shanghai")).toBeInTheDocument();
    // 当前时间不写死：按所选时区渲染，形如 2026-10-22 13:05 (UTC+8)。
    expect(
      screen.getByText(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2} \(UTC\+8\)$/),
    ).toBeInTheDocument();
  });

  it("未设置时区时显示跟随部署配置", () => {
    useAuthStore.setState({ user: fakeUser() });

    renderPage();

    expect(screen.getByText("跟随部署配置")).toBeInTheDocument();
    expect(screen.getByText("（未设置）")).toBeInTheDocument();
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
      within(listbox).getByRole("option", { name: "Asia/Tokyo（UTC+9）" }),
    );

    await waitFor(() =>
      expect(authApi.updateMe).toHaveBeenCalledWith({ timezone: "Asia/Tokyo" }),
    );
    await waitFor(() =>
      expect(useAuthStore.getState().user?.timezone).toBe("Asia/Tokyo"),
    );
    expect(screen.getByText("Asia/Tokyo")).toBeInTheDocument();
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
