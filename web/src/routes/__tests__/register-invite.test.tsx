import { StrictMode } from "react";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import RegisterPage from "@/routes/register";
import { renderWithRoute } from "@/test/render";
import { authApi } from "@/lib/api/auth";
import { ApiRequestError } from "@/lib/api/client";

const { mockNavigate, setAuth } = vi.hoisted(() => ({
  mockNavigate: vi.fn(),
  setAuth: vi.fn(),
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
    register: vi.fn(),
    validateInvite: vi.fn(),
  },
}));

vi.mock("@/lib/store/auth-store", () => ({
  useAuthStore: vi.fn((selector: (state: { setAuth: typeof setAuth }) => unknown) =>
    selector({ setAuth }),
  ),
}));

const fakeUser = {
  id: "user-2",
  username: "invitee",
  email: "invitee@example.com",
  role: "member" as const,
  created_at: "2026-10-01T00:00:00Z",
  updated_at: "2026-10-01T00:00:00Z",
};

// 与生产 main.tsx 同口径：校验 effect 在 StrictMode 双执行下也不能把页面
// 永久卡在「正在验证」（过期结果由 cancelled 丢弃，活着的请求负责落地状态）。
function renderPage(entry: string) {
  return renderWithRoute(
    <StrictMode>
      <RegisterPage />
    </StrictMode>,
    {
      path: "/register",
      initialEntry: entry,
    },
  );
}

async function fillAndSubmit(user: ReturnType<typeof userEvent.setup>) {
  await user.type(screen.getByLabelText("用户名"), "invitee");
  await user.type(screen.getByLabelText("邮箱"), "invitee@example.com");
  await user.type(screen.getByLabelText("密码"), "Password123");
  await user.click(screen.getByRole("button", { name: "注册" }));
}

afterEach(() => {
  vi.clearAllMocks();
});

describe("RegisterPage invite flow", () => {
  it("validates the invite, then submits with the invite token", async () => {
    vi.mocked(authApi.validateInvite).mockResolvedValue({
      data: { expires_at: "2026-10-22T00:00:00Z" },
    } as Awaited<ReturnType<typeof authApi.validateInvite>>);
    vi.mocked(authApi.register).mockResolvedValue({
      data: { token: "jwt", refresh_token: "refresh", user: fakeUser },
    } as Awaited<ReturnType<typeof authApi.register>>);

    const user = userEvent.setup();
    renderPage("/register?invite=tok-123");

    expect(await screen.findByText("接受邀请加入")).toBeInTheDocument();
    expect(authApi.validateInvite).toHaveBeenCalledWith("tok-123");
    expect(screen.getByText(/邀请有效至/)).toBeInTheDocument();

    await fillAndSubmit(user);

    await waitFor(() =>
      expect(authApi.register).toHaveBeenCalledWith({
        username: "invitee",
        email: "invitee@example.com",
        password: "Password123",
        invite_token: "tok-123",
      }),
    );
    expect(mockNavigate).toHaveBeenCalledWith("/dashboard", { replace: true });
  });

  it("shows the invalid-invite state and a login link", async () => {
    vi.mocked(authApi.validateInvite).mockRejectedValue(
      new ApiRequestError(404, 40432, "邀请链接无效或已过期"),
    );

    renderPage("/register?invite=bad-token");

    expect(
      await screen.findByText("邀请链接无效或已过期"),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "去登录" })).toHaveAttribute(
      "href",
      "/login",
    );
    expect(screen.queryByLabelText("用户名")).not.toBeInTheDocument();
    expect(authApi.register).not.toHaveBeenCalled();
  });

  it("shows the backend message when the invite has already been used (40932)", async () => {
    // 已用/无效的细节由服务端 message 给出，页面原样展示，不写死「无效或已过期」。
    vi.mocked(authApi.validateInvite).mockRejectedValue(
      new ApiRequestError(409, 40932, "邀请链接已被使用"),
    );

    renderPage("/register?invite=used-token");

    expect(await screen.findByText("邀请链接已被使用")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "去登录" })).toHaveAttribute(
      "href",
      "/login",
    );
    expect(screen.queryByLabelText("用户名")).not.toBeInTheDocument();
  });

  it("shows the closed-registration hint without a token, and the backend error verbatim", async () => {
    vi.mocked(authApi.register).mockRejectedValue(
      new ApiRequestError(
        403,
        40306,
        "公开注册已关闭，请联系管理员获取邀请链接",
      ),
    );

    const user = userEvent.setup();
    renderPage("/register");

    // 无 token 不做校验，直接显示表单（首个用户仍可注册为管理员）
    expect(authApi.validateInvite).not.toHaveBeenCalled();
    expect(
      screen.getByText("公开注册已关闭：请联系管理员获取邀请链接"),
    ).toBeInTheDocument();

    await fillAndSubmit(user);

    await waitFor(() =>
      expect(authApi.register).toHaveBeenCalledWith({
        username: "invitee",
        email: "invitee@example.com",
        password: "Password123",
        invite_token: undefined,
      }),
    );
    // 服务端 40306 文案原样展示，不替换成「用户名或邮箱已存在」
    expect(
      await screen.findByText(
        "公开注册已关闭，请联系管理员获取邀请链接",
        { exact: false },
      ),
    ).toBeInTheDocument();
    expect(setAuth).not.toHaveBeenCalled();
  });
});
