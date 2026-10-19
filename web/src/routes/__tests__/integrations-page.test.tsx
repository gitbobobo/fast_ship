import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import IntegrationsPage from "@/routes/settings/integrations";
import { signInAs, teardownAuth } from "@/test/auth";
import { renderWithRoute } from "@/test/render";
import { toast } from "sonner";

const { getGitHubApp, updateGitHubApp, testGitHubApp, createManifest } =
  vi.hoisted(() => ({
    getGitHubApp: vi.fn(),
    updateGitHubApp: vi.fn(),
    testGitHubApp: vi.fn(),
    createManifest: vi.fn(),
  }));

vi.mock("sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));

vi.mock("@/lib/api/deployment", () => ({
  deploymentApi: {
    getGitHubApp,
    updateGitHubApp,
    testGitHubApp,
    createManifest,
  },
}));

// Header 渲染通知铃（INT-90）：与 settings-page.test.tsx 同一口径 mock 数据钩子。
vi.mock("@/lib/hooks/use-notifications", () => ({
  NOTIFICATION_UNREAD_POLL_MS: 30000,
  useNotifications: vi.fn(() => ({ data: [] })),
  useUnreadNotificationCount: vi.fn(() => ({ data: 0 })),
  useMarkNotificationRead: vi.fn(() => ({ mutate: vi.fn(), mutateAsync: vi.fn() })),
  useMarkAllNotificationsRead: vi.fn(() => ({ mutate: vi.fn(), mutateAsync: vi.fn() })),
}));

function deployment(
  overrides: Partial<GitHubAppDeployment> = {},
): GitHubAppDeployment {
  return {
    configured: true,
    source: "database",
    app_id: "123456",
    app_slug: "fast-ship",
    private_key_set: true,
    webhook_secret_set: true,
    webhook_url: "https://fs.example.com/api/github/webhooks",
    install_url: "https://github.com/apps/fast-ship/installations/new",
    detection_status: "ok",
    detection_detail: "已连接 GitHub App：fast-ship",
    detected_at: "2026-10-18T12:00:00Z",
    updated_at: "2026-10-18T12:00:00Z",
    ...overrides,
  };
}

function unconfigured(): GitHubAppDeployment {
  return deployment({
    configured: false,
    source: "none",
    app_id: "",
    app_slug: "",
    private_key_set: false,
    webhook_secret_set: false,
    webhook_url: "",
    install_url: "",
    detection_status: "unchecked",
    detection_detail: "",
    detected_at: null,
    updated_at: null,
  });
}

function renderPage() {
  return renderWithRoute(<IntegrationsPage />, {
    path: "/settings/integrations",
    initialEntry: "/settings/integrations",
  });
}

afterEach(() => {
  teardownAuth();
  vi.clearAllMocks();
  vi.restoreAllMocks();
});

describe("IntegrationsPage admin view", () => {
  beforeEach(() => {
    signInAs("u1", "admin");
    getGitHubApp.mockResolvedValue({ data: deployment() });
    updateGitHubApp.mockResolvedValue({ data: deployment() });
    testGitHubApp.mockResolvedValue({ data: deployment() });
  });

  it("shows the one-click create action, org input and collapsed manual form", async () => {
    renderPage();

    expect(
      await screen.findByRole("button", { name: "在 GitHub 上创建 App" }),
    ).toBeEnabled();
    expect(
      screen.getByRole("button", { name: "测试连接" }),
    ).toBeInTheDocument();
    expect(screen.getByLabelText(/组织名/)).toBeInTheDocument();
    expect(screen.getByText("手动配置（已有 App）")).toBeInTheDocument();
    expect(screen.getByLabelText("App ID")).toBeInTheDocument();
    expect(
      screen.getByText("https://fs.example.com/api/github/webhooks"),
    ).toBeInTheDocument();
  });

  it("creates a manifest and submits it to GitHub via a hidden POST form", async () => {
    const submitSpy = vi
      .spyOn(HTMLFormElement.prototype, "submit")
      .mockImplementation(() => {});
    const manifestResponse: GitHubAppManifestResponse = {
      action_url: "https://github.com/organizations/fast-ship/settings/apps/new",
      state: "state-abc",
      manifest: {
        name: "fast-ship",
        url: "https://fs.example.com",
        hook_attributes: {
          url: "https://fs.example.com/api/github/webhooks",
          active: true,
        },
        redirect_url:
          "https://fs.example.com/settings/integrations/github/callback",
        setup_url: "https://fs.example.com/settings/integrations",
        public: false,
        default_events: ["pull_request"],
        default_permissions: {
          pull_requests: "write",
          issues: "write",
          checks: "write",
          contents: "read",
          metadata: "read",
        },
      },
    };
    createManifest.mockResolvedValue({ data: manifestResponse });
    const user = userEvent.setup();
    renderPage();

    await user.type(await screen.findByLabelText(/组织名/), "fast-ship");
    await user.click(
      screen.getByRole("button", { name: "在 GitHub 上创建 App" }),
    );

    await waitFor(() =>
      expect(createManifest).toHaveBeenCalledWith({ org: "fast-ship" }),
    );
    await waitFor(() => expect(submitSpy).toHaveBeenCalledTimes(1));

    const form = submitSpy.mock.contexts[0] as HTMLFormElement;
    expect(form.method).toBe("post");
    expect(form.action).toBe(manifestResponse.action_url);
    const manifestInput = form.querySelector<HTMLInputElement>(
      'input[name="manifest"]',
    );
    expect(manifestInput?.type).toBe("hidden");
    expect(JSON.parse(manifestInput!.value)).toEqual(manifestResponse.manifest);
    const stateInput = form.querySelector<HTMLInputElement>(
      'input[name="state"]',
    );
    expect(stateInput?.type).toBe("hidden");
    expect(stateInput?.value).toBe("state-abc");
  });

  it("sends an empty org payload when no organization is given", async () => {
    vi.spyOn(HTMLFormElement.prototype, "submit").mockImplementation(() => {});
    createManifest.mockResolvedValue({
      data: {
        action_url: "https://github.com/settings/apps/new",
        state: "s",
        manifest: {} as GitHubAppManifest,
      },
    });
    const user = userEvent.setup();
    renderPage();

    await screen.findByRole("button", { name: "在 GitHub 上创建 App" });
    await user.click(
      screen.getByRole("button", { name: "在 GitHub 上创建 App" }),
    );

    await waitFor(() => expect(createManifest).toHaveBeenCalledWith({}));
  });

  it("disables the create button when public_url is not configured", async () => {
    getGitHubApp.mockResolvedValue({
      data: deployment({ webhook_url: "" }),
    });
    renderPage();

    expect(
      await screen.findByRole("button", { name: "在 GitHub 上创建 App" }),
    ).toBeDisabled();
    expect(
      screen.getByRole("link", { name: "设置 → 部署" }),
    ).toHaveAttribute("href", "/settings/deployment");
  });

  it("still saves the manual configuration form", async () => {
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

  it("updates the status badge from the test response", async () => {
    testGitHubApp.mockResolvedValue({
      data: deployment({
        detection_status: "invalid",
        detection_detail: "GitHub 拒绝该 App 身份（401）",
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
});

describe("IntegrationsPage member view", () => {
  beforeEach(() => {
    signInAs("u2", "member");
    getGitHubApp.mockResolvedValue({ data: deployment() });
  });

  it("shows only the status and the install button", async () => {
    renderPage();

    expect(await screen.findByText("已连接")).toBeInTheDocument();
    expect(
      screen.getByText("已连接 GitHub App：fast-ship"),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "安装到仓库" }),
    ).toBeInTheDocument();

    // 没有任何创建/保存/测试入口
    expect(
      screen.queryByRole("button", { name: "在 GitHub 上创建 App" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "测试连接" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "保存" }),
    ).not.toBeInTheDocument();
    expect(screen.queryByLabelText("App ID")).not.toBeInTheDocument();
    expect(screen.queryByLabelText(/组织名/)).not.toBeInTheDocument();
  });

  it("opens the install page in a new window", async () => {
    const openSpy = vi.spyOn(window, "open").mockImplementation(() => null);
    const user = userEvent.setup();
    renderPage();

    await user.click(
      await screen.findByRole("button", { name: "安装到仓库" }),
    );

    expect(openSpy).toHaveBeenCalledWith(
      "https://github.com/apps/fast-ship/installations/new",
      "_blank",
      "noopener",
    );
  });

  it("tells members to contact an admin when nothing is configured", async () => {
    getGitHubApp.mockResolvedValue({ data: unconfigured() });
    renderPage();

    expect(await screen.findByText("未配置")).toBeInTheDocument();
    expect(screen.getByText(/请联系管理员/)).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "安装到仓库" }),
    ).not.toBeInTheDocument();
  });
});
