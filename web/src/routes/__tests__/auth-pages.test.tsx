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
  },
}));

vi.mock("@/lib/store/auth-store", () => ({
  useAuthStore: vi.fn(),
}));

describe("AuthPages", () => {
  const setAuth = vi.fn();
  const fakeUser = {
    id: "user-1",
    username: "godbobo",
    email: "godbobo@example.com",
    created_at: "2026-04-06T10:00:00Z",
    updated_at: "2026-04-06T10:00:00Z",
  };

  beforeEach(() => {
    vi.mocked(useAuthStore).mockImplementation(((selector: (state: {
      token: string | null;
      user: User | null;
      setAuth: typeof setAuth;
    }) => unknown) =>
      selector({
        token: null,
        user: null,
        setAuth,
      })) as typeof useAuthStore);
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it("logs in and stores auth state before navigating", async () => {
    vi.mocked(authApi.login).mockResolvedValue({
      data: {
        token: "jwt-token",
        refresh_token: "refresh-token",
        user: fakeUser,
      },
    } as Awaited<ReturnType<typeof authApi.login>>);

    const user = userEvent.setup();

    renderWithRoute(<LoginPage />, {
      path: "/login",
      initialEntry: "/login",
    });

    await user.type(screen.getByLabelText("用户名或邮箱"), "godbobo");
    await user.type(screen.getByLabelText("密码"), "Password123");
    await user.click(screen.getByRole("button", { name: "登录" }));

    await waitFor(() =>
      expect(authApi.login).toHaveBeenCalledWith({
        login: "godbobo",
        password: "Password123",
      }),
    );
    expect(setAuth).toHaveBeenCalledWith("jwt-token", "refresh-token", fakeUser);
    expect(mockNavigate).toHaveBeenCalledWith("/dashboard", { replace: true });
  });

  it("shows login error when credentials are invalid", async () => {
    vi.mocked(authApi.login).mockRejectedValue(new Error("login failed"));

    const user = userEvent.setup();

    renderWithRoute(<LoginPage />, {
      path: "/login",
      initialEntry: "/login",
    });

    await user.type(screen.getByLabelText("用户名或邮箱"), "godbobo");
    await user.type(screen.getByLabelText("密码"), "wrong-password");
    await user.click(screen.getByRole("button", { name: "登录" }));

    await waitFor(() =>
      expect(
        screen.getByText("用户名/邮箱或密码错误"),
      ).toBeInTheDocument(),
    );
    expect(setAuth).not.toHaveBeenCalled();
  });

  it("registers and stores auth state before navigating", async () => {
    vi.mocked(authApi.register).mockResolvedValue({
      data: {
        token: "jwt-token",
        refresh_token: "refresh-token",
        user: fakeUser,
      },
    } as Awaited<ReturnType<typeof authApi.register>>);

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
      expect(authApi.register).toHaveBeenCalledWith({
        username: "godbobo",
        email: "godbobo@example.com",
        password: "Password123",
      }),
    );
    expect(setAuth).toHaveBeenCalledWith("jwt-token", "refresh-token", fakeUser);
    expect(mockNavigate).toHaveBeenCalledWith("/dashboard", { replace: true });
  });

  it("shows register error when backend rejects the request", async () => {
    vi.mocked(authApi.register).mockRejectedValue(new Error("register failed"));

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
      expect(
        screen.getByText("注册失败，用户名或邮箱可能已存在"),
      ).toBeInTheDocument(),
    );
    expect(setAuth).not.toHaveBeenCalled();
  });
});
