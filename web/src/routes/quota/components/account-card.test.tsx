import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { QuotaAccountCard } from "./account-card";
import {
  useDeleteQuotaAccount,
  useRefreshQuotaAccount,
} from "@/lib/hooks/use-quota";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
vi.mock("@/lib/hooks/use-quota", () => ({
  useDeleteQuotaAccount: vi.fn(),
  useRefreshQuotaAccount: vi.fn(),
}));

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

function makeWindow(overrides: Partial<QuotaWindow> = {}): QuotaWindow {
  return {
    kind: "monthly",
    label: "每月",
    used_percent: null,
    used: null,
    limit: null,
    unit: "percent",
    reset_at: null,
    ...overrides,
  };
}

describe("QuotaAccountCard", () => {
  beforeEach(() => {
    vi.mocked(useRefreshQuotaAccount).mockReturnValue({
      mutateAsync: vi.fn(),
      isPending: false,
    } as unknown as ReturnType<typeof useRefreshQuotaAccount>);
    vi.mocked(useDeleteQuotaAccount).mockReturnValue({
      mutateAsync: vi.fn(),
      isPending: false,
    } as unknown as ReturnType<typeof useDeleteQuotaAccount>);
  });

  it("renders identity, fetched time, remaining number and window rows", () => {
    // 多留 45s 缓冲，渲染到断言之间的一两秒漂移不会把分钟降 1
    const resetAt = new Date(
      Date.now() + 3 * 3600_000 + 12 * 60_000 + 45_000,
    ).toISOString();
    const account = makeAccount({
      balance: { label: "剩余额度", amount: 12.5, currency: "USD" },
      windows: [
        makeWindow({
          kind: "five_hour",
          label: "5 小时",
          used_percent: 97,
          used: 9.7,
          limit: 10,
          unit: "count",
          reset_at: resetAt,
        }),
        makeWindow({
          kind: "weekly",
          label: "每周",
          used_percent: 82,
          reset_at: null,
        }),
        makeWindow({
          label: "每月",
          used_percent: 38,
          used: 38,
          limit: 100,
          unit: "usd",
        }),
      ],
    });
    render(
      <QuotaAccountCard account={account} onTest={vi.fn()} onEdit={vi.fn()} />,
    );

    expect(screen.getByText("Factory Droid")).toBeInTheDocument();
    expect(screen.getByText("· Pro")).toBeInTheDocument();
    expect(screen.getByText("我的 Pro · …ab12")).toBeInTheDocument();
    // 「正常」徽标已移除，数据时间在标题副行
    expect(screen.queryByText("正常")).toBeNull();
    expect(screen.getByText(/数据时间/)).toBeInTheDocument();

    // 有效剩余 = 100 − max(97) = 3%；受限窗口是 5 小时
    expect(screen.getByTestId("quota-remaining")).toHaveTextContent("3%");
    expect(screen.getByText(/受「5 小时」限制/)).toBeInTheDocument();

    const windows = screen.getAllByTestId("quota-window");
    expect(windows).toHaveLength(3);
    expect(windows[0]).toHaveAttribute("data-tone", "danger");
    expect(windows[1]).toHaveAttribute("data-tone", "warning");
    expect(windows[2]).toHaveAttribute("data-tone", "normal");

    // >=95 的窗口进度条/百分比用中性色，红色只留给需处理
    expect(screen.getByText("97%")).toHaveClass("text-muted-foreground");
    expect(screen.getByText("82%")).toHaveClass("text-amber-700");
    // used/limit 明细收进 tooltip，正文不再直接出现
    expect(screen.queryByText("9.7 / 10")).toBeNull();
    expect(screen.getByText("3 小时 12 分后重置")).toBeInTheDocument();

    expect(screen.getByText("剩余额度：")).toBeInTheDocument();
    expect(screen.getByText("12.5 USD")).toBeInTheDocument();
  });

  it("marks the limiting window row in medium weight", () => {
    const account = makeAccount({
      windows: [
        makeWindow({ kind: "daily", label: "每天", used_percent: 30 }),
        makeWindow({ label: "每月", used_percent: 70 }),
      ],
    });
    const { container } = render(
      <QuotaAccountCard account={account} onTest={vi.fn()} onEdit={vi.fn()} />,
    );

    const rows = container.querySelectorAll(
      '[data-testid="quota-window"] > div',
    );
    expect(rows[0]).not.toHaveClass("font-medium");
    expect(rows[1]).toHaveClass("font-medium");
  });

  it("shows pool tabs for multi-pool accounts, defaulting to the pool still being consumed", async () => {
    const user = userEvent.setup();
    const account = makeAccount({
      windows: [
        makeWindow({ label: "每月", pool: "标准", used_percent: 100 }),
        makeWindow({
          kind: "five_hour",
          label: "5 小时",
          pool: "Core",
          used_percent: 87,
        }),
      ],
    });
    render(
      <QuotaAccountCard account={account} onTest={vi.fn()} onEdit={vi.fn()} />,
    );

    const standardTab = screen.getByRole("tab", { name: /标准/ });
    const coreTab = screen.getByRole("tab", { name: /Core/ });
    expect(standardTab).toHaveTextContent("用尽");
    expect(coreTab).toHaveTextContent("消耗中");
    // 标准池用尽 → 默认打开正在消耗的 Core 池，主数字 = 13%
    expect(coreTab).toHaveAttribute("aria-selected", "true");
    expect(screen.getByTestId("quota-remaining")).toHaveTextContent("13%");
    expect(screen.queryByText("每月")).toBeNull();
    expect(screen.getByText("5 小时")).toBeInTheDocument();

    await user.click(standardTab);
    expect(standardTab).toHaveAttribute("aria-selected", "true");
    expect(screen.getByTestId("quota-remaining")).toHaveTextContent("0%");
    expect(screen.getByText("每月")).toBeInTheDocument();
  });

  it("池集合变化后按池名保持选中，不随下标漂移", async () => {
    const user = userEvent.setup();
    const account = makeAccount({
      windows: [
        makeWindow({ label: "每月", pool: "标准", used_percent: 100 }),
        makeWindow({
          kind: "five_hour",
          label: "5 小时",
          pool: "Core",
          used_percent: 87,
        }),
      ],
    });
    const { rerender } = render(
      <QuotaAccountCard account={account} onTest={vi.fn()} onEdit={vi.fn()} />,
    );

    await user.click(screen.getByRole("tab", { name: /标准/ }));
    expect(screen.getByText("每月")).toBeInTheDocument();

    // 刷新后池顺序变了：Core 排到标准前面，标准的下标从 0 变 1
    rerender(
      <QuotaAccountCard
        account={makeAccount({
          windows: [
            makeWindow({
              kind: "five_hour",
              label: "5 小时",
              pool: "Core",
              used_percent: 87,
            }),
            makeWindow({ label: "每月", pool: "标准", used_percent: 100 }),
          ],
        })}
        onTest={vi.fn()}
        onEdit={vi.fn()}
      />,
    );

    expect(screen.getByRole("tab", { name: /标准/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.getByText("每月")).toBeInTheDocument();
    expect(screen.queryByText("5 小时")).toBeNull();
  });

  it("选中的池消失时回退到默认池", async () => {
    const user = userEvent.setup();
    const account = makeAccount({
      windows: [
        makeWindow({ label: "每月", pool: "标准", used_percent: 100 }),
        makeWindow({
          kind: "five_hour",
          label: "5 小时",
          pool: "Core",
          used_percent: 87,
        }),
      ],
    });
    const { rerender } = render(
      <QuotaAccountCard account={account} onTest={vi.fn()} onEdit={vi.fn()} />,
    );

    await user.click(screen.getByRole("tab", { name: /标准/ }));
    expect(screen.getByText("每月")).toBeInTheDocument();

    // 标准池的窗口不再下发，只剩 Core 单池
    rerender(
      <QuotaAccountCard
        account={makeAccount({
          windows: [
            makeWindow({
              kind: "five_hour",
              label: "5 小时",
              pool: "Core",
              used_percent: 87,
            }),
          ],
        })}
        onTest={vi.fn()}
        onEdit={vi.fn()}
      />,
    );

    expect(screen.queryByRole("tab")).toBeNull();
    expect(screen.getByText("5 小时")).toBeInTheDocument();
    expect(screen.getByTestId("quota-remaining")).toHaveTextContent("13%");
  });

  it("hides pool tabs when the account has a single pool", () => {
    const account = makeAccount({
      windows: [makeWindow({ used_percent: 38 })],
    });
    render(
      <QuotaAccountCard account={account} onTest={vi.fn()} onEdit={vi.fn()} />,
    );
    expect(screen.queryByRole("tab")).toBeNull();
  });

  it("shows pending copy and imminent reset wording", () => {
    const account = makeAccount({
      plan_name: "",
      status: "pending",
      fetched_at: null,
      last_success_at: null,
      windows: [
        makeWindow({
          kind: "daily",
          label: "每天",
          reset_at: "2000-01-01T00:00:00Z",
        }),
      ],
    });
    render(
      <QuotaAccountCard account={account} onTest={vi.fn()} onEdit={vi.fn()} />,
    );

    expect(screen.getByText("尚未抓取")).toBeInTheDocument();
    expect(screen.getByText("—")).toBeInTheDocument();
    expect(screen.getByTestId("quota-window-reset")).toHaveTextContent(
      "即将重置",
    );
    // 无 used_percent 时算不出有效剩余，不渲染主数字
    expect(screen.queryByTestId("quota-remaining")).toBeNull();
  });

  it("note 非空时在余额下方展示一行补充说明", () => {
    const account = makeAccount({
      balance: { label: "钱包余额", amount: 1.5, currency: "USD" },
      note: "窗口用尽后扣钱包",
    });
    const { container } = render(
      <QuotaAccountCard account={account} onTest={vi.fn()} onEdit={vi.fn()} />,
    );

    const note = screen.getByTestId("quota-account-note");
    expect(note).toHaveTextContent("窗口用尽后扣钱包");
    expect(note).toHaveClass("text-muted-foreground");
    const text = container.textContent ?? "";
    expect(text.indexOf("1.5 USD")).toBeLessThan(
      text.indexOf("窗口用尽后扣钱包"),
    );
  });

  it("note 为空串时不渲染补充说明", () => {
    render(
      <QuotaAccountCard
        account={makeAccount({ note: "" })}
        onTest={vi.fn()}
        onEdit={vi.fn()}
      />,
    );
    expect(screen.queryByTestId("quota-account-note")).toBeNull();
  });

  it("never renders credential plaintext", () => {
    const { container } = render(
      <QuotaAccountCard
        account={makeAccount()}
        onTest={vi.fn()}
        onEdit={vi.fn()}
      />,
    );
    expect(container.textContent).toContain("…ab12");
    expect(container.textContent).not.toContain("fk-secret-value");
  });

  it("invokes edit and test callbacks from the ··· menu", async () => {
    const user = userEvent.setup();
    const onTest = vi.fn();
    const onEdit = vi.fn();
    const account = makeAccount();
    render(
      <QuotaAccountCard account={account} onTest={onTest} onEdit={onEdit} />,
    );

    await user.click(
      screen.getByRole("button", { name: "更多操作 我的 Pro" }),
    );
    await user.click(await screen.findByRole("menuitem", { name: "编辑" }));
    expect(onEdit).toHaveBeenCalledWith(account);

    await user.click(
      screen.getByRole("button", { name: "更多操作 我的 Pro" }),
    );
    await user.click(
      await screen.findByRole("menuitem", { name: "测试凭据" }),
    );
    expect(onTest).toHaveBeenCalledWith(account);
  });
});
