import { StrictMode } from "react";
import { screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import GitHubCallbackPage from "@/routes/settings/github-callback";
import { signInAs, teardownAuth } from "@/test/auth";
import { renderWithRoute } from "@/test/render";

const { completeManifest } = vi.hoisted(() => ({
  completeManifest: vi.fn(),
}));

vi.mock("@/lib/api/deployment", () => ({
  deploymentApi: { completeManifest },
}));

// Header 渲染通知铃（INT-90）：与 integrations-page.test.tsx 同一口径 mock。
vi.mock("@/lib/hooks/use-notifications", () => ({
  NOTIFICATION_UNREAD_POLL_MS: 30000,
  useNotifications: vi.fn(() => ({ data: [] })),
  useUnreadNotificationCount: vi.fn(() => ({ data: 0 })),
  useMarkNotificationRead: vi.fn(() => ({ mutate: vi.fn(), mutateAsync: vi.fn() })),
  useMarkAllNotificationsRead: vi.fn(() => ({ mutate: vi.fn(), mutateAsync: vi.fn() })),
}));

// 与生产 main.tsx 同口径：页面在 StrictMode 下也必须只发一次请求且能收到结果。
function renderPageStrict(entry: string) {
  return renderWithRoute(
    <StrictMode>
      <GitHubCallbackPage />
    </StrictMode>,
    {
      path: "/settings/integrations/github/callback",
      initialEntry: entry,
    },
  );
}

afterEach(() => {
  teardownAuth();
  vi.clearAllMocks();
});

describe("GitHubCallbackPage", () => {
  beforeEach(() => {
    signInAs("u1", "admin");
  });

  it("completes the manifest once under StrictMode and redirects to the install url", async () => {
    const installUrl = "https://github.com/apps/fast-ship/installations/new";
    completeManifest.mockResolvedValue({
      data: { install_url: installUrl },
    });

    renderPageStrict(
      "/settings/integrations/github/callback?code=abc&state=s1",
    );

    await waitFor(() =>
      expect(completeManifest).toHaveBeenCalledWith({
        code: "abc",
        state: "s1",
      }),
    );
    // StrictMode 双执行：请求只发一次（code 一次性），但第二次 effect 的
    // 处理器要能收到结果完成跳转，不能永久停在「正在完成」。
    await waitFor(() => expect(window.location.href).toBe(installUrl));
    expect(completeManifest).toHaveBeenCalledTimes(1);
  });

  it("shows the backend error and a recreate button on failure", async () => {
    completeManifest.mockRejectedValue(
      new Error("授权已过期或被使用过，请重新创建"),
    );

    renderPageStrict(
      "/settings/integrations/github/callback?code=abc&state=s1",
    );

    expect(
      await screen.findByText("授权已过期或被使用过，请重新创建"),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "重新创建" }),
    ).toHaveAttribute("href", "/settings/integrations");
  });

  it("shows an error without calling the api when params are missing", async () => {
    renderPageStrict("/settings/integrations/github/callback");

    expect(
      await screen.findByText("GitHub 回调缺少必要参数，请重新创建 App。"),
    ).toBeInTheDocument();
    expect(completeManifest).not.toHaveBeenCalled();
  });
});
