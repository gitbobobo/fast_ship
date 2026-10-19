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
    startDeviceAuthorization: vi.fn(),
    getDeviceAuthorization: vi.fn(),
    cancelDeviceAuthorization: vi.fn(),
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

function makeWindow(overrides: Partial<QuotaWindow> = {}): QuotaWindow {
  return {
    kind: "five_hour",
    label: "5 小时",
    used_percent: null,
    used: null,
    limit: null,
    unit: "percent",
    reset_at: null,
    ...overrides,
  };
}

function makeAccount(overrides: Partial<QuotaAccount> = {}): QuotaAccount {
  return {
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
    windows: [],
    balance: null,
    note: "",
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
    ...overrides,
  };
}

const ACCOUNT = makeAccount({
  windows: [makeWindow({ used_percent: 38, used: 3.8, limit: 10 })],
});

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
    // 顶部常驻说明移进空态
    expect(
      screen.getByText(/凭据加密保存，任何位置不回显明文/),
    ).toBeInTheDocument();

    // 空态 CTA 打开添加对话框
    const addButtons = await screen.findAllByRole("button", {
      name: /添加账号/,
    });
    await user.click(addButtons[addButtons.length - 1]);
    expect(await screen.findByText("添加额度账号")).toBeInTheDocument();
  });

  it("账号列表渲染卡片网格与计数 chip", async () => {
    vi.mocked(quotaApi.listAccounts).mockResolvedValue({
      code: 0,
      message: "",
      data: [ACCOUNT],
    });
    renderWithRoute(<QuotaPage />, { path: "/quota", initialEntry: "/quota" });

    expect(await screen.findByText("Factory Droid")).toBeInTheDocument();
    expect(screen.getByText("我的 Pro · …ab12")).toBeInTheDocument();
    // 顶部常驻说明不再出现；「正常」徽标已移除，大号剩余数字也已移除
    expect(screen.queryByText(/任何位置不回显明文/)).toBeNull();
    expect(screen.queryByText("正常")).toBeNull();
    expect(screen.queryByTestId("quota-remaining")).toBeNull();
    expect(screen.getByTestId("quota-window")).toHaveAttribute(
      "data-tone",
      "normal",
    );
    expect(screen.getByTestId("quota-window-percent")).toHaveTextContent(
      "38%",
    );

    expect(screen.getByTestId("quota-chip-usable")).toHaveTextContent("可用1");
    expect(screen.getByTestId("quota-chip-exhausted")).toHaveTextContent(
      "已用尽0",
    );
    expect(screen.getByTestId("quota-chip-attention")).toHaveTextContent(
      "需处理0",
    );
  });

  it("三段分组：需处理横幅、可用卡片按剩余降序、已用尽单行放最后", async () => {
    const attention = makeAccount({
      id: "acc-bad",
      label: "失效号",
      status: "credential_invalid",
      last_error: "凭据已过期",
    });
    const usableLow = makeAccount({
      id: "acc-low",
      label: "紧张号",
      windows: [makeWindow({ used_percent: 90 })],
    });
    const usableHigh = makeAccount({
      id: "acc-high",
      label: "富余号",
      windows: [makeWindow({ used_percent: 10 })],
    });
    const exhausted = makeAccount({
      id: "acc-out",
      label: "备用号",
      windows: [
        makeWindow({
          used_percent: 100,
          reset_at: new Date(
            Date.now() + 3 * 24 * 3600_000 + 30 * 60_000,
          ).toISOString(),
        }),
      ],
    });
    vi.mocked(quotaApi.listAccounts).mockResolvedValue({
      code: 0,
      message: "",
      // 有意乱序：验证排序在前端完成
      data: [exhausted, usableLow, attention, usableHigh],
    });
    renderWithRoute(<QuotaPage />, { path: "/quota", initialEntry: "/quota" });

    // 需处理收成横幅，只给「重新连接」
    const banner = await screen.findByTestId("quota-attention");
    expect(banner).toHaveTextContent("凭据失效");
    expect(banner).toHaveTextContent("凭据已过期");
    expect(banner).toHaveTextContent("失效号");

    // 可用段按有效剩余降序：富余号(已用 10%) 在 紧张号(已用 90%) 之前
    const percents = screen.getAllByTestId("quota-window-percent");
    expect(percents[0]).toHaveTextContent("10%");
    expect(percents[1]).toHaveTextContent("90%");

    // 已用尽收成单行，显示恢复时间
    const exhaustedRow = screen.getByTestId("quota-exhausted-row");
    expect(exhaustedRow).toHaveTextContent("备用号");
    expect(exhaustedRow).toHaveTextContent("3 天后恢复");

    // 视觉顺序：横幅 → 可用卡片 → 已用尽行
    const firstCard = percents[0].closest('[data-slot="card"]');
    expect(firstCard).toHaveTextContent("富余号");
    expect(
      banner.compareDocumentPosition(firstCard as Element) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    expect(
      (firstCard as Element).compareDocumentPosition(exhaustedRow) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  it("「重新连接」直接打开编辑对话框", async () => {
    vi.mocked(quotaApi.listAccounts).mockResolvedValue({
      code: 0,
      message: "",
      data: [
        makeAccount({
          id: "acc-bad",
          status: "provider_error",
          last_error: "HTTP 502",
        }),
      ],
    });
    const user = userEvent.setup();
    renderWithRoute(<QuotaPage />, { path: "/quota", initialEntry: "/quota" });

    const banner = await screen.findByTestId("quota-attention");
    expect(banner).toHaveTextContent("接口异常");
    await user.click(
      await screen.findByRole("button", { name: "重新连接" }),
    );
    expect(await screen.findByText("编辑额度账号")).toBeInTheDocument();
  });

  it("计数 chip 兼做筛选：只看已用尽", async () => {
    vi.mocked(quotaApi.listAccounts).mockResolvedValue({
      code: 0,
      message: "",
      data: [
        ACCOUNT,
        makeAccount({
          id: "acc-out",
          label: "备用号",
          windows: [makeWindow({ used_percent: 100 })],
        }),
      ],
    });
    const user = userEvent.setup();
    renderWithRoute(<QuotaPage />, { path: "/quota", initialEntry: "/quota" });

    const chip = await screen.findByTestId("quota-chip-exhausted");
    await user.click(chip);
    expect(chip).toHaveAttribute("aria-pressed", "true");
    // 只显示已用尽段
    expect(screen.getByTestId("quota-exhausted-row")).toHaveTextContent(
      "备用号",
    );
    expect(screen.queryByTestId("quota-window")).toBeNull();

    // 再点一次回到全部
    await user.click(chip);
    expect(screen.getByTestId("quota-window")).toBeInTheDocument();
  });

  it("筛选到空分组时给出提示", async () => {
    vi.mocked(quotaApi.listAccounts).mockResolvedValue({
      code: 0,
      message: "",
      data: [ACCOUNT],
    });
    const user = userEvent.setup();
    renderWithRoute(<QuotaPage />, { path: "/quota", initialEntry: "/quota" });

    await user.click(await screen.findByTestId("quota-chip-attention"));
    expect(
      await screen.findByText("没有需处理的账号"),
    ).toBeInTheDocument();
  });

  it("加载失败时显示错误态与重试", async () => {
    vi.mocked(quotaApi.listAccounts).mockRejectedValue(new Error("boom"));
    renderWithRoute(<QuotaPage />, { path: "/quota", initialEntry: "/quota" });

    expect(await screen.findByText("额度数据加载失败")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "重试" })).toBeInTheDocument();
  });
});
