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

  it("renders identity, plan, status badge and windows with tone", () => {
    // 多留 45s 缓冲，渲染到断言之间的一两秒漂移不会把分钟降 1
    const resetAt = new Date(
      Date.now() + 3 * 3600_000 + 12 * 60_000 + 45_000,
    ).toISOString();
    const account = makeAccount({
      balance: { label: "剩余额度", amount: 12.5, currency: "USD" },
      windows: [
        {
          kind: "five_hour",
          label: "5 小时",
          used_percent: 97,
          used: 9.7,
          limit: 10,
          unit: "count",
          reset_at: resetAt,
        },
        {
          kind: "weekly",
          label: "每周",
          used_percent: 82,
          used: null,
          limit: null,
          unit: "percent",
          reset_at: null,
        },
        {
          kind: "monthly",
          label: "每月",
          used_percent: 38,
          used: 38,
          limit: 100,
          unit: "usd",
          reset_at: null,
        },
      ],
    });
    render(
      <QuotaAccountCard account={account} onTest={vi.fn()} onEdit={vi.fn()} />,
    );

    expect(screen.getByText("Factory Droid")).toBeInTheDocument();
    expect(screen.getByText("· Pro")).toBeInTheDocument();
    expect(screen.getByText("我的 Pro · …ab12")).toBeInTheDocument();
    expect(screen.getByText("正常")).toBeInTheDocument();
    expect(screen.getByText("剩余额度：")).toBeInTheDocument();
    expect(screen.getByText("12.5 USD")).toBeInTheDocument();
    expect(screen.getByText(/数据时间/)).toBeInTheDocument();

    const windows = screen.getAllByTestId("quota-window");
    expect(windows).toHaveLength(3);
    expect(windows[0]).toHaveAttribute("data-tone", "danger");
    expect(windows[1]).toHaveAttribute("data-tone", "warning");
    expect(windows[2]).toHaveAttribute("data-tone", "normal");

    expect(screen.getByText("97%")).toHaveClass("text-destructive");
    expect(screen.getByText("82%")).toHaveClass("text-amber-700");
    expect(screen.getByText("9.7 / 10")).toBeInTheDocument();
    expect(screen.getByText("3 小时 12 分后重置")).toBeInTheDocument();
  });

  it("shows pending copy and imminent reset wording", () => {
    const account = makeAccount({
      plan_name: "",
      status: "pending",
      fetched_at: null,
      last_success_at: null,
      windows: [
        {
          kind: "daily",
          label: "每天",
          used_percent: null,
          used: null,
          limit: null,
          unit: "percent",
          reset_at: "2000-01-01T00:00:00Z",
        },
      ],
    });
    render(
      <QuotaAccountCard account={account} onTest={vi.fn()} onEdit={vi.fn()} />,
    );

    expect(screen.getByText("暂未抓取")).toBeInTheDocument();
    expect(screen.getByText("—")).toBeInTheDocument();
    expect(screen.getByTestId("quota-window-reset")).toHaveTextContent(
      "即将重置",
    );
    expect(screen.getByText("尚未抓取")).toBeInTheDocument();
  });

  it("surfaces provider_error with last_error and destructive badge", () => {
    const account = makeAccount({
      status: "provider_error",
      last_error: "HTTP 502 上游服务异常",
    });
    render(
      <QuotaAccountCard account={account} onTest={vi.fn()} onEdit={vi.fn()} />,
    );

    expect(screen.getByText("接口异常")).toBeInTheDocument();
    expect(screen.getByText("HTTP 502 上游服务异常")).toBeInTheDocument();
    // windows 为空时不渲染兜底空态文案以外的数据
    expect(screen.getByText("还没有额度数据")).toBeInTheDocument();
  });

  it("note 非空时在窗口/余额下方展示一行补充说明", () => {
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
    // 位置在余额行之后、底部「数据时间」行之前
    const text = container.textContent ?? "";
    expect(text.indexOf("1.5 USD")).toBeLessThan(
      text.indexOf("窗口用尽后扣钱包"),
    );
    expect(text.indexOf("窗口用尽后扣钱包")).toBeLessThan(
      text.indexOf("数据时间"),
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

  it("invokes test and edit callbacks", async () => {
    const user = userEvent.setup();
    const onTest = vi.fn();
    const onEdit = vi.fn();
    const account = makeAccount();
    render(
      <QuotaAccountCard account={account} onTest={onTest} onEdit={onEdit} />,
    );

    await user.click(screen.getByRole("button", { name: "测试 我的 Pro" }));
    expect(onTest).toHaveBeenCalledWith(account);
    await user.click(screen.getByRole("button", { name: "编辑 我的 Pro" }));
    expect(onEdit).toHaveBeenCalledWith(account);
  });
});
