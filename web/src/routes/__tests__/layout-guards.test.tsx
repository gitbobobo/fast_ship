import { screen, waitFor } from "@testing-library/react";
import { render } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { vi } from "vitest";
import AppLayout from "@/routes/_layout";
import AuthLayout from "@/routes/_auth-layout";
import { authApi } from "@/lib/api/auth";
import { useAuthStore } from "@/lib/store/auth-store";

vi.mock("@/lib/api/auth", () => ({
  authApi: {
    me: vi.fn(),
    logout: vi.fn(),
  },
}));

vi.mock("@/lib/store/auth-store", () => ({
  useAuthStore: vi.fn(),
}));

vi.mock("@/components/layout/sidebar", () => ({
  Sidebar: () => <div>侧边栏</div>,
}));

describe("LayoutGuards", () => {
  const setUser = vi.fn();
  const logout = vi.fn();
  const authState = {
    token: null as string | null,
    user: null as User | null,
    setUser,
    logout,
  };

  beforeEach(() => {
    vi.mocked(useAuthStore).mockImplementation(((selector?: (state: typeof authState) => unknown) =>
      selector ? selector(authState) : authState) as typeof useAuthStore);
  });

  afterEach(() => {
    vi.clearAllMocks();
    authState.token = null;
    authState.user = null;
  });

  it("redirects unauthenticated users from app layout to login", async () => {
    render(
      <MemoryRouter initialEntries={["/projects"]}>
        <Routes>
          <Route path="/login" element={<div>登录页</div>} />
          <Route element={<AppLayout />}>
            <Route path="/projects" element={<div>项目页</div>} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    expect(await screen.findByText("登录页")).toBeInTheDocument();
    expect(screen.queryByText("项目页")).not.toBeInTheDocument();
  });

  it("redirects authenticated users away from auth layout", async () => {
    authState.token = "jwt-token";

    render(
      <MemoryRouter initialEntries={["/login"]}>
        <Routes>
          <Route element={<AuthLayout />}>
            <Route path="/login" element={<div>登录表单</div>} />
          </Route>
          <Route path="/projects" element={<div>项目页</div>} />
        </Routes>
      </MemoryRouter>,
    );

    expect(await screen.findByText("项目页")).toBeInTheDocument();
    expect(screen.queryByText("登录表单")).not.toBeInTheDocument();
  });

  it("bootstraps current user when token exists but user is missing", async () => {
    authState.token = "jwt-token";
    vi.mocked(authApi.me).mockResolvedValue({
      data: {
        id: "user-1",
        username: "godbobo",
        email: "godbobo@example.com",
        created_at: "2026-04-06T10:00:00Z",
        updated_at: "2026-04-06T10:00:00Z",
      },
    } as Awaited<ReturnType<typeof authApi.me>>);

    render(
      <MemoryRouter initialEntries={["/projects"]}>
        <Routes>
          <Route path="/login" element={<div>登录页</div>} />
          <Route
            element={
              <AppLayout />
            }
          >
            <Route path="/projects" element={<div>项目页</div>} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    expect(screen.getByText("加载用户信息中...")).toBeInTheDocument();

    await waitFor(() => expect(authApi.me).toHaveBeenCalled());
    await waitFor(() =>
      expect(setUser).toHaveBeenCalledWith({
        id: "user-1",
        username: "godbobo",
        email: "godbobo@example.com",
        created_at: "2026-04-06T10:00:00Z",
        updated_at: "2026-04-06T10:00:00Z",
      }),
    );
    expect(await screen.findByText("项目页")).toBeInTheDocument();
  });

  it("logs out when bootstrap request fails", async () => {
    authState.token = "jwt-token";
    vi.mocked(authApi.me).mockRejectedValue(new Error("unauthorized"));

    render(
      <MemoryRouter initialEntries={["/projects"]}>
        <Routes>
          <Route path="/login" element={<div>登录页</div>} />
          <Route element={<AppLayout />}>
            <Route path="/projects" element={<div>项目页</div>} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );

    await waitFor(() => expect(authApi.me).toHaveBeenCalled());
    await waitFor(() => expect(logout).toHaveBeenCalled());
  });
});
