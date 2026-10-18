import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import IntegrationsPage from "@/routes/settings/integrations";
import { signInAs, teardownAuth } from "@/test/auth";
import { renderWithRoute } from "@/test/render";
import { toast } from "sonner";

const { getGitHubApp, updateGitHubApp, testGitHubApp } = vi.hoisted(() => ({
  getGitHubApp: vi.fn(),
  updateGitHubApp: vi.fn(),
  testGitHubApp: vi.fn(),
}));

vi.mock("sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));

vi.mock("@/lib/api/integrations", () => ({
  integrationApi: { getGitHubApp, updateGitHubApp, testGitHubApp },
}));

// Header 渲染通知铃（INT-90）：与 settings-page.test.tsx 同一口径 mock 数据钩子。
vi.mock("@/lib/hooks/use-notifications", () => ({
  NOTIFICATION_UNREAD_POLL_MS: 30000,
  useNotifications: vi.fn(() => ({ data: [] })),
  useUnreadNotificationCount: vi.fn(() => ({ data: 0 })),
  useMarkNotificationRead: vi.fn(() => ({ mutate: vi.fn(), mutateAsync: vi.fn() })),
  useMarkAllNotificationsRead: vi.fn(() => ({ mutate: vi.fn(), mutateAsync: vi.fn() })),
}));

function integration(
  overrides: Partial<GitHubAppIntegration> = {},
): GitHubAppIntegration {
  return {
    provider: "github_app",
    configured: true,
    source: "database",
    app_id: "123456",
    private_key_set: true,
    webhook_secret_set: true,
    webhook_url: "https://fs.example.com/api/github/webhooks",
    detection_status: "ok",
    detection_detail: "已连接 GitHub App：fast-ship",
    detected_at: "2026-10-18T12:00:00Z",
    updated_at: "2026-10-18T12:00:00Z",
    ...overrides,
  };
}

function renderPage() {
  return renderWithRoute(<IntegrationsPage />, {
    path: "/settings/integrations",
    initialEntry: "/settings/integrations",
  });
}

beforeEach(() => {
  // 集成缓存按登录用户隔离（enabled 依赖 userId）：页面查询需要已登录用户。
  signInAs("u1");
  getGitHubApp.mockResolvedValue({ data: integration() });
  updateGitHubApp.mockResolvedValue({ data: integration() });
  testGitHubApp.mockResolvedValue({ data: integration() });
});

afterEach(() => {
  teardownAuth();
  vi.clearAllMocks();
});

describe("IntegrationsPage", () => {
  it("renders the unconfigured empty state", async () => {
    getGitHubApp.mockResolvedValue({
      data: integration({
        configured: false,
        source: "none",
        app_id: "",
        private_key_set: false,
        webhook_secret_set: false,
        webhook_url: "",
        detection_status: "unchecked",
        detection_detail: "",
        detected_at: null,
        updated_at: null,
      }),
    });

    renderPage();

    expect(await screen.findByText("未配置")).toBeInTheDocument();
    expect(
      screen.getByText(
        "尚未配置，填写后保存即可用你自己的 GitHub App 参与 PR 评审与 webhook。",
      ),
    ).toBeInTheDocument();
    expect(screen.getByText("/api/github/webhooks")).toBeInTheDocument();
    expect(
      screen.getByText("部署未配置 server.public_url，请自行拼接对外地址"),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "测试连接" }),
    ).toBeDisabled();
  });

  it("shows the config-source notice when deployment config is in effect", async () => {
    getGitHubApp.mockResolvedValue({
      data: integration({ source: "config", updated_at: null }),
    });

    renderPage();

    expect(
      await screen.findByText(
        "当前生效的是部署配置（config.yaml 兜底），保存后将改用你自己的配置。",
      ),
    ).toBeInTheDocument();
    expect(screen.getByText("已连接")).toBeInTheDocument();
  });

  it("submits only the fields the user filled in", async () => {
    const user = userEvent.setup();
    renderPage();

    const appIdInput = await screen.findByLabelText("App ID");
    await waitFor(() => expect(appIdInput).toHaveValue("123456"));

    await user.clear(appIdInput);
    await user.type(appIdInput, "654321");
    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() =>
      expect(updateGitHubApp).toHaveBeenCalledWith({ app_id: "654321" }),
    );
    expect(toast.success).toHaveBeenCalledWith("已保存");
  });

  it("sends webhook_secret as empty string when clearing the saved secret", async () => {
    const user = userEvent.setup();
    renderPage();

    await screen.findByLabelText("App ID");
    await user.click(
      screen.getByRole("checkbox", { name: "清空已存密钥" }),
    );
    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() =>
      expect(updateGitHubApp).toHaveBeenCalledWith({
        app_id: "123456",
        webhook_secret: "",
      }),
    );
  });

  it("keeps the clear-secret selection across a test-connection refresh", async () => {
    const user = userEvent.setup();
    renderPage();

    await screen.findByLabelText("App ID");
    await user.click(
      screen.getByRole("checkbox", { name: "清空已存密钥" }),
    );

    // 「测试连接」写回新的缓存对象（检测时间前进），不应取消清空意图
    testGitHubApp.mockResolvedValue({
      data: integration({ detected_at: "2026-10-18T13:00:00Z" }),
    });
    await user.click(screen.getByRole("button", { name: "测试连接" }));
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith(
        "已连接 GitHub App：fast-ship",
      ),
    );
    expect(
      screen.getByRole("checkbox", { name: "清空已存密钥" }),
    ).toBeChecked();

    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() =>
      expect(updateGitHubApp).toHaveBeenCalledWith({
        app_id: "123456",
        webhook_secret: "",
      }),
    );
  });

  it("keeps the saved private key when the field is left blank, sends it when filled", async () => {
    const user = userEvent.setup();
    renderPage();

    const appIdInput = await screen.findByLabelText("App ID");
    await waitFor(() => expect(appIdInput).toHaveValue("123456"));

    // 私钥留空 → 请求体不带 private_key_pem
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() =>
      expect(updateGitHubApp).toHaveBeenCalledWith({ app_id: "123456" }),
    );

    updateGitHubApp.mockClear();
    await user.type(
      screen.getByLabelText("App 私钥（PEM）"),
      "-----BEGIN RSA PRIVATE KEY-----",
    );
    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() =>
      expect(updateGitHubApp).toHaveBeenCalledWith({
        app_id: "123456",
        private_key_pem: "-----BEGIN RSA PRIVATE KEY-----",
      }),
    );
  });

  it("updates the status badge from the test response", async () => {
    testGitHubApp.mockResolvedValue({
      data: integration({
        detection_status: "invalid",
        detection_detail: "GitHub 拒绝该 App 身份（401）",
        detected_at: "2026-10-18T13:00:00Z",
      }),
    });
    const user = userEvent.setup();
    renderPage();

    expect(await screen.findByText("已连接")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "测试连接" }));

    await waitFor(() =>
      expect(screen.getByText("凭证无效")).toBeInTheDocument(),
    );
    expect(toast.error).toHaveBeenCalledWith("GitHub 拒绝该 App 身份（401）");
  });

  it("validates that App ID is numeric before submitting", async () => {
    const user = userEvent.setup();
    renderPage();

    const appIdInput = await screen.findByLabelText("App ID");
    await waitFor(() => expect(appIdInput).toHaveValue("123456"));
    await user.clear(appIdInput);
    await user.type(appIdInput, "abc123");
    await user.click(screen.getByRole("button", { name: "保存" }));

    expect(
      await screen.findByText("App ID 必须是数字"),
    ).toBeInTheDocument();
    expect(updateGitHubApp).not.toHaveBeenCalled();
  });

  it("requires the private key on first configuration", async () => {
    getGitHubApp.mockResolvedValue({
      data: integration({
        configured: false,
        source: "none",
        app_id: "",
        private_key_set: false,
        webhook_secret_set: false,
        webhook_url: "",
        detection_status: "unchecked",
        detection_detail: "",
        detected_at: null,
        updated_at: null,
      }),
    });
    const user = userEvent.setup();
    renderPage();

    const appIdInput = await screen.findByLabelText("App ID");
    await user.type(appIdInput, "123456");
    await user.click(screen.getByRole("button", { name: "保存" }));

    expect(
      await screen.findByText("首次配置必须提供 App 私钥"),
    ).toBeInTheDocument();
    expect(updateGitHubApp).not.toHaveBeenCalled();
  });

  it("copies the webhook URL to the clipboard", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });
    renderPage();

    await screen.findByText("已连接");
    fireEvent.click(screen.getByRole("button", { name: "复制" }));

    await waitFor(() =>
      expect(writeText).toHaveBeenCalledWith(
        "https://fs.example.com/api/github/webhooks",
      ),
    );
  });
});
