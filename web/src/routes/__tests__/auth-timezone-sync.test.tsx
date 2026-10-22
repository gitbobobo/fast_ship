import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import LoginPage from "@/routes/login";
import RegisterPage from "@/routes/register";
import { renderWithRoute } from "@/test/render";
import { authApi } from "@/lib/api/auth";
import { useAuthStore } from "@/lib/store/auth-store";

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

vi.mock("@/lib/api/auth", () => ({
  authApi: {
    login: vi.fn(),
    register: vi.fn(),
    updateMe: vi.fn(),
  },
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

describe("首登写入浏览器时区", () => {
  beforeEach(() => {
    // 浏览器时区固定成 Asia/Shanghai，断言才不依赖跑测试的机器。
    vi.spyOn(
      Intl.DateTimeFormat.prototype,
      "resolvedOptions",
    ).mockReturnValue({
      timeZone: "Asia/Shanghai",
    } as Intl.ResolvedDateTimeFormatOptions);
  });

  afterEach(() => {
    vi.clearAllMocks();
    vi.restoreAllMocks();
    useAuthStore.setState({ token: null, refreshToken: null, user: null });
  });

  it("登录时 user.timezone 为空，写入浏览器时区后再跳转", async () => {
    const loggedIn = fakeUser();
    const synced = fakeUser({ timezone: "Asia/Shanghai" });
    vi.mocked(authApi.login).mockResolvedValue({
      data: { token: "jwt-token", refresh_token: "refresh-token", user: loggedIn },
    } as Awaited<ReturnType<typeof authApi.login>>);
    vi.mocked(authApi.updateMe).mockResolvedValue({
      data: synced,
    } as Awaited<ReturnType<typeof authApi.updateMe>>);

    const user = userEvent.setup();
    renderWithRoute(<LoginPage />, { path: "/login", initialEntry: "/login" });

    await user.type(screen.getByLabelText("用户名或邮箱"), "godbobo");
    await user.type(screen.getByLabelText("密码"), "Password123");
    await user.click(screen.getByRole("button", { name: "登录" }));

    await waitFor(() =>
      expect(authApi.updateMe).toHaveBeenCalledWith({
        timezone: "Asia/Shanghai",
      }),
    );
    expect(useAuthStore.getState().user?.timezone).toBe("Asia/Shanghai");
    expect(mockNavigate).toHaveBeenCalledWith("/dashboard", { replace: true });
  });

  it("登录时 user.timezone 已有值就不再写", async () => {
    const loggedIn = fakeUser({ timezone: "Asia/Tokyo" });
    vi.mocked(authApi.login).mockResolvedValue({
      data: { token: "jwt-token", refresh_token: "refresh-token", user: loggedIn },
    } as Awaited<ReturnType<typeof authApi.login>>);

    const user = userEvent.setup();
    renderWithRoute(<LoginPage />, { path: "/login", initialEntry: "/login" });

    await user.type(screen.getByLabelText("用户名或邮箱"), "godbobo");
    await user.type(screen.getByLabelText("密码"), "Password123");
    await user.click(screen.getByRole("button", { name: "登录" }));

    await waitFor(() =>
      expect(mockNavigate).toHaveBeenCalledWith("/dashboard", { replace: true }),
    );
    expect(authApi.updateMe).not.toHaveBeenCalled();
  });

  it("注册时 user.timezone 为空，写入浏览器时区后再跳转", async () => {
    const registered = fakeUser();
    const synced = fakeUser({ timezone: "Asia/Shanghai" });
    vi.mocked(authApi.register).mockResolvedValue({
      data: {
        token: "jwt-token",
        refresh_token: "refresh-token",
        user: registered,
      },
    } as Awaited<ReturnType<typeof authApi.register>>);
    vi.mocked(authApi.updateMe).mockResolvedValue({
      data: synced,
    } as Awaited<ReturnType<typeof authApi.updateMe>>);

    const user = userEvent.setup();
    renderWithRoute(<RegisterPage />, {
      path: "/register",
      initialEntry: "/register",
    });

    await user.type(screen.getByLabelText("用户名"), "godbobo");
    await user.type(screen.getByLabelText("邮箱"), "godbobo@example.com");
    await user.type(screen.getByLabelText("密码"), "Password123");
    await user.click(screen.getByRole("button", { name: "注册" }));

    await waitFor(() =>
      expect(authApi.updateMe).toHaveBeenCalledWith({
        timezone: "Asia/Shanghai",
      }),
    );
    expect(useAuthStore.getState().user?.timezone).toBe("Asia/Shanghai");
    expect(mockNavigate).toHaveBeenCalledWith("/dashboard", { replace: true });
  });
});
