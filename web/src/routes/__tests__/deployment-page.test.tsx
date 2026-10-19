import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import DeploymentPage from "@/routes/settings/deployment";
import { signInAs, teardownAuth } from "@/test/auth";
import { renderWithRoute } from "@/test/render";
import { toast } from "sonner";

const { getSettings, updateSettings } = vi.hoisted(() => ({
  getSettings: vi.fn(),
  updateSettings: vi.fn(),
}));

vi.mock("sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));

vi.mock("@/lib/api/deployment", () => ({
  deploymentApi: { getSettings, updateSettings },
}));

// Header 渲染通知铃（INT-90）：与 integrations-page.test.tsx 同一口径 mock。
vi.mock("@/lib/hooks/use-notifications", () => ({
  NOTIFICATION_UNREAD_POLL_MS: 30000,
  useNotifications: vi.fn(() => ({ data: [] })),
  useUnreadNotificationCount: vi.fn(() => ({ data: 0 })),
  useMarkNotificationRead: vi.fn(() => ({ mutate: vi.fn(), mutateAsync: vi.fn() })),
  useMarkAllNotificationsRead: vi.fn(() => ({ mutate: vi.fn(), mutateAsync: vi.fn() })),
}));

function settings(
  overrides: Partial<DeploymentSettings> = {},
): DeploymentSettings {
  return {
    public_url: "https://fs.example.com",
    public_url_source: "database",
    updated_at: "2026-10-18T12:00:00Z",
    ...overrides,
  };
}

function renderPage() {
  return renderWithRoute(<DeploymentPage />, {
    path: "/settings/deployment",
    initialEntry: "/settings/deployment",
  });
}

afterEach(() => {
  teardownAuth();
  vi.clearAllMocks();
});

describe("DeploymentPage admin view", () => {
  beforeEach(() => {
    signInAs("u1", "admin");
  });

  it("shows the database source badge", async () => {
    getSettings.mockResolvedValue({ data: settings() });
    renderPage();

    expect(await screen.findByText("已保存到数据库")).toBeInTheDocument();
    expect(screen.getByLabelText("public_url")).toHaveValue(
      "https://fs.example.com",
    );
  });

  it("shows the config source badge", async () => {
    getSettings.mockResolvedValue({
      data: settings({ public_url_source: "config", updated_at: null }),
    });
    renderPage();

    expect(await screen.findByText("来自部署配置")).toBeInTheDocument();
  });

  it("shows the unconfigured badge and suggests the current origin", async () => {
    getSettings.mockResolvedValue({
      data: settings({
        public_url: "",
        public_url_source: "none",
        updated_at: null,
      }),
    });
    const user = userEvent.setup();
    renderPage();

    expect(await screen.findByText("未配置")).toBeInTheDocument();
    expect(screen.getByLabelText("public_url")).toHaveValue("");

    await user.click(
      screen.getByRole("button", { name: "使用当前地址" }),
    );
    expect(screen.getByLabelText("public_url")).toHaveValue(
      window.location.origin,
    );
  });

  it("saves a normalized url without the trailing slash", async () => {
    getSettings.mockResolvedValue({ data: settings() });
    updateSettings.mockResolvedValue({
      data: settings({ public_url: "https://new.example.com" }),
    });
    const user = userEvent.setup();
    renderPage();

    const input = await screen.findByLabelText("public_url");
    await user.clear(input);
    await user.type(input, "https://new.example.com/");
    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() =>
      expect(updateSettings).toHaveBeenCalledWith({
        public_url: "https://new.example.com",
      }),
    );
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith("已保存"),
    );
  });

  it("rejects non-http(s) urls before submitting", async () => {
    getSettings.mockResolvedValue({ data: settings() });
    const user = userEvent.setup();
    renderPage();

    const input = await screen.findByLabelText("public_url");
    await user.clear(input);
    await user.type(input, "notaurl");
    await user.click(screen.getByRole("button", { name: "保存" }));

    expect(
      await screen.findByText("必须是 http(s) 绝对地址，不能带参数或锚点"),
    ).toBeInTheDocument();
    expect(updateSettings).not.toHaveBeenCalled();
  });
});

describe("DeploymentPage member view", () => {
  it("shows an admin-required empty state for non-admins", async () => {
    signInAs("u2", "member");
    getSettings.mockResolvedValue({ data: settings() });

    renderPage();

    expect(await screen.findByText(/需要管理员权限/)).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "保存" }),
    ).not.toBeInTheDocument();
  });
});
