import { StrictMode } from "react";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import OAuthAuthorizePage from "@/routes/oauth/authorize";
import { withTestQueryClient } from "@/test/render";
import { signInAs, signOut, teardownAuth } from "@/test/auth";
import { useAuthStore } from "@/lib/store/auth-store";
import type { components } from "@/types/api.gen";

type OAuthAuthorizePreview = components["schemas"]["OAuthAuthorizePreview"];

const { preview, authorize, authLogout } = vi.hoisted(() => ({
  preview: vi.fn(),
  authorize: vi.fn(),
  authLogout: vi.fn(),
}));

vi.mock("@/lib/api/oauth", () => ({
  oauthApi: { preview, authorize },
}));

vi.mock("@/lib/api/auth", () => ({
  authApi: { logout: authLogout },
}));

const previewFixture: OAuthAuthorizePreview = {
  username: "verifybot",
  email: "verifybot@example.com",
  client_name: "Claude Code MCP",
  projects: [
    { id: "p-1", name: "Fast Ship" },
    { id: "p-2", name: "Mobile App" },
    { id: "p-3", name: "内部工具" },
  ],
};

const fullQuery =
  "?client_id=client-1&redirect_uri=http://localhost:7777/callback" +
  "&code_challenge=abc&code_challenge_method=S256&state=st-1";

// 同意页要同时断言 /oauth/authorize 与登录回跳落点，这里自带双路由渲染。
function renderPage(entry: string) {
  return render(
    withTestQueryClient(
      <StrictMode>
        <MemoryRouter initialEntries={[entry]}>
          <Routes>
            <Route path="/oauth/authorize" element={<OAuthAuthorizePage />} />
            <Route
              path="/login"
              element={<LoginProbe />}
            />
          </Routes>
        </MemoryRouter>
      </StrictMode>,
    ),
  );
}

// 登录回跳探针：把实际到达 /login 的 search 渲染出来供断言。
function LoginProbe() {
  const { search } = useLocation();
  return <div data-testid="login-page">{search}</div>;
}

beforeEach(() => {
  vi.clearAllMocks();
  signInAs("u1");
});

afterEach(() => {
  teardownAuth();
});

describe("OAuthAuthorizePage", () => {
  it("未登录跳登录页并携带完整 query 作为 next", () => {
    signOut();
    renderPage(`/oauth/authorize${fullQuery}`);

    const expectedNext = encodeURIComponent(`/oauth/authorize${fullQuery}`);
    expect(screen.getByTestId("login-page")).toHaveTextContent(
      `?next=${expectedNext}`,
    );
  });

  it("预览成功：显示当前账号、客户端名与项目清单", async () => {
    preview.mockResolvedValue({ data: previewFixture });
    renderPage(`/oauth/authorize${fullQuery}`);

    expect(await screen.findByText("Claude Code MCP")).toBeInTheDocument();
    expect(screen.getByText("verifybot")).toBeInTheDocument();
    expect(screen.getByText(/verifybot@example.com/)).toBeInTheDocument();
    // 品牌文案也叫 Fast Ship，项目用 checkbox 角色断言避免撞名。
    expect(
      screen.getByRole("checkbox", { name: "Fast Ship" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("checkbox", { name: "内部工具" }),
    ).toBeInTheDocument();
    expect(preview).toHaveBeenCalledWith({
      client_id: "client-1",
      redirect_uri: "http://localhost:7777/callback",
      code_challenge: "abc",
      code_challenge_method: "S256",
    });
  });

  it("预览失败（客户端不存在/参数非法）：展示错误态", async () => {
    preview.mockRejectedValue(new Error("该应用不存在或授权参数非法"));
    renderPage(`/oauth/authorize${fullQuery}`);

    expect(await screen.findByText("无法发起授权")).toBeInTheDocument();
    expect(screen.getByText(/该应用不存在或授权参数非法/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "同意授权" })).toBeNull();
  });

  it("缺少必要参数：提示授权链接无效，不发起预览", () => {
    renderPage("/oauth/authorize?client_id=client-1");
    expect(screen.getByText("授权链接无效")).toBeInTheDocument();
    expect(preview).not.toHaveBeenCalled();
  });

  it("未选项目时禁止提交，选后提交成功跳转 redirect", async () => {
    preview.mockResolvedValue({ data: previewFixture });
    authorize.mockResolvedValue({
      data: { redirect: "http://localhost:7777/callback?code=c1&state=st-1" },
    });
    const user = userEvent.setup();
    renderPage(`/oauth/authorize${fullQuery}`);

    const submit = await screen.findByRole("button", { name: "同意授权" });
    expect(submit).toBeDisabled();
    expect(screen.getByText("至少选择 1 个项目后才能授权")).toBeInTheDocument();

    await user.click(screen.getByRole("checkbox", { name: "Fast Ship" }));
    expect(submit).toBeEnabled();

    await user.click(submit);
    await waitFor(() =>
      expect(authorize).toHaveBeenCalledWith({
        client_id: "client-1",
        redirect_uri: "http://localhost:7777/callback",
        code_challenge: "abc",
        code_challenge_method: "S256",
        state: "st-1",
        project_ids: ["p-1"],
        write: false,
      }),
    );
    await waitFor(() =>
      expect(window.location.href).toBe(
        "http://localhost:7777/callback?code=c1&state=st-1",
      ),
    );
  });

  it("取消授权：以 access_denied 回调并保留 state", async () => {
    preview.mockResolvedValue({ data: previewFixture });
    const user = userEvent.setup();
    renderPage(`/oauth/authorize${fullQuery}`);

    await screen.findByRole("button", { name: "同意授权" });
    await user.click(screen.getByRole("button", { name: "取消" }));

    await waitFor(() =>
      expect(window.location.href).toBe(
        "http://localhost:7777/callback?error=access_denied&state=st-1",
      ),
    );
    expect(authorize).not.toHaveBeenCalled();
  });

  it("切换账号：吊销服务端会话、清本地凭证并带 next 进登录页", async () => {
    preview.mockResolvedValue({ data: previewFixture });
    const user = userEvent.setup();
    renderPage(`/oauth/authorize${fullQuery}`);

    await screen.findByRole("button", { name: "同意授权" });
    await user.click(screen.getByRole("button", { name: "切换账号" }));

    // 服务端会话吊销被调用（与 UserNav 同语义）
    await waitFor(() => expect(authLogout).toHaveBeenCalled());
    // 本地凭证已清（触发未登录 <Navigate> 进登录页，next 保留完整授权参数）
    const expectedNext = encodeURIComponent(`/oauth/authorize${fullQuery}`);
    expect(screen.getByTestId("login-page")).toHaveTextContent(
      `?next=${expectedNext}`,
    );
    expect(useAuthStore.getState().token).toBeNull();
  });

  it("选读写档后提交带 write: true", async () => {
    preview.mockResolvedValue({ data: previewFixture });
    authorize.mockResolvedValue({ data: { redirect: "http://cb/?code=c" } });
    const user = userEvent.setup();
    renderPage(`/oauth/authorize${fullQuery}`);

    await screen.findByRole("button", { name: "同意授权" });
    await user.click(screen.getByRole("checkbox", { name: "Mobile App" }));
    await user.click(screen.getByRole("radio", { name: /读写/ }));
    await user.click(screen.getByRole("button", { name: "同意授权" }));

    await waitFor(() =>
      expect(authorize).toHaveBeenCalledWith(
        expect.objectContaining({ write: true, project_ids: ["p-2"] }),
      ),
    );
  });
});
