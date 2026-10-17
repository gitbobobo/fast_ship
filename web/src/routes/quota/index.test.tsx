import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import QuotaPage from "./index";
import { renderWithRoute } from "@/test/render";
import { signInAs, teardownAuth } from "@/test/auth";
import { quotaApi } from "@/lib/api/quota";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
vi.mock("@/lib/api/quota", () => ({
  quotaApi: {
    listProviders: vi.fn(),
    listAccounts: vi.fn(),
    createAccount: vi.fn(),
    updateAccount: vi.fn(),
    deleteAccount: vi.fn(),
    refreshAccount: vi.fn(),
    probe: vi.fn(),
  },
}));

// Header 渲染通知铃，与本仓库其它页面测试口径一致 mock 其数据钩子。
vi.mock("@/lib/hooks/use-notifications", () => ({
  NOTIFICATION_UNREAD_POLL_MS: 30000,
  useNotifications: vi.fn(() => ({ data: [] })),
  useUnreadNotificationCount: vi.fn(() => ({ data: 0 })),
  useMarkNotificationRead: vi.fn(() => ({ mutate: vi.fn(), mutateAsync: vi.fn() })),
  useMarkAllNotificationsRead: vi.fn(() => ({ mutate: vi.fn(), mutateAsync: vi.fn() })),
}));

const ACCOUNT: QuotaAccount = {
  id: "acc-1",
  provider: "factory",
  provider_name: "Factory Droid",
  label: "我的 Pro",
  credential_hint: "…ab12",
  plan_name: "Pro",
  status: "ok",
  last_error: "",
  fetched_at: "2026-10-17T04:00:00Z",
  last_success_at: "2026-10-17T04:00:00Z",
  windows: [
    {
      kind: "five_hour",
      label: "5 小时",
      used_percent: 38,
      used: 3.8,
      limit: 10,
      unit: "count",
      reset_at: null,
    },
  ],
  balance: null,
  created_at: "2026-10-01T00:00:00Z",
  updated_at: "2026-10-01T00:00:00Z",
};

beforeEach(() => {
  signInAs("A");
  vi.mocked(quotaApi.listProviders).mockResolvedValue({
    code: 0,
    message: "",
    data: [],
  });
});

afterEach(() => {
  teardownAuth();
  vi.clearAllMocks();
});

describe("QuotaPage", () => {
  it("空态引导添加第一个账号", async () => {
    vi.mocked(quotaApi.listAccounts).mockResolvedValue({
      code: 0,
      message: "",
      data: [],
    });
    const user = userEvent.setup();
    renderWithRoute(<QuotaPage />, { path: "/quota", initialEntry: "/quota" });

    expect(await screen.findByText("还没有额度账号")).toBeInTheDocument();

    // 空态 CTA 打开添加对话框
    const addButtons = await screen.findAllByRole("button", {
      name: /添加账号/,
    });
    await user.click(addButtons[addButtons.length - 1]);
    expect(await screen.findByText("添加额度账号")).toBeInTheDocument();
  });

  it("账号列表渲染卡片网格", async () => {
    vi.mocked(quotaApi.listAccounts).mockResolvedValue({
      code: 0,
      message: "",
      data: [ACCOUNT],
    });
    renderWithRoute(<QuotaPage />, { path: "/quota", initialEntry: "/quota" });

    expect(await screen.findByText("Factory Droid")).toBeInTheDocument();
    expect(screen.getByText("我的 Pro · …ab12")).toBeInTheDocument();
    expect(screen.getByText("正常")).toBeInTheDocument();
    expect(screen.getByTestId("quota-window")).toHaveAttribute(
      "data-tone",
      "normal",
    );
  });

  it("加载失败时显示错误态与重试", async () => {
    vi.mocked(quotaApi.listAccounts).mockRejectedValue(new Error("boom"));
    renderWithRoute(<QuotaPage />, { path: "/quota", initialEntry: "/quota" });

    expect(await screen.findByText("额度数据加载失败")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "重试" })).toBeInTheDocument();
  });
});
