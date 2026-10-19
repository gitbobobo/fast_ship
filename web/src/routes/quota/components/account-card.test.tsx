import { render, screen, within } from "@testing-library/react";
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

/** 正文触发区 = 单行额度区块（进度条或余额行；池选择器在 header，不在其中）。 */
function bodyTrigger(label = "我的 Pro") {
  return screen.getByLabelText(`查看 ${label} 的额度详情`);
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

  it("renders identity, fetched time and only the limiting window row", () => {
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

    // 正文没有大号剩余数字与「剩余 · 受…限制」文案——它和进度条的
    // 「已用%」是同一件事的两种说法，只保留进度条一行
    const trigger = bodyTrigger();
    expect(screen.queryByTestId("quota-remaining")).toBeNull();
    expect(trigger.querySelector(".text-3xl")).toBeNull();
    expect(trigger.textContent).not.toContain("剩余");

    // 正文只渲染受限窗口一根进度条，其余窗口不进正文
    const windows = screen.getAllByTestId("quota-window");
    expect(windows).toHaveLength(1);
    expect(windows[0]).toHaveAttribute("data-tone", "danger");
    expect(screen.getByText("5 小时")).toBeInTheDocument();
    expect(screen.queryByText("每周")).toBeNull();
    expect(screen.queryByText("每月")).toBeNull();
    expect(screen.getAllByRole("progressbar")).toHaveLength(1);

    // 97% 还没用尽 → 琥珀色提示；只有 100%（等重置）才转中性色，红只留给需处理
    expect(screen.getByText("97%")).toHaveClass("text-amber-700");
    expect(screen.getByText("3 小时 12 分后重置")).toBeInTheDocument();

    // used/limit 明细、余额不进正文——都在悬浮面板里
    expect(screen.queryByText("9.7 / 10")).toBeNull();
    expect(screen.queryByText("剩余额度：")).toBeNull();
  });

  it("悬浮正文打开详情面板：列出全部窗口、used/limit、余额与备注", async () => {
    const user = userEvent.setup();
    const account = makeAccount({
      balance: { label: "剩余额度", amount: 12.5, currency: "USD" },
      note: "窗口用尽后扣钱包",
      windows: [
        makeWindow({
          kind: "five_hour",
          label: "5 小时",
          used_percent: 97,
          used: 9.7,
          limit: 10,
          unit: "count",
        }),
        makeWindow({ kind: "weekly", label: "每周", used_percent: 82 }),
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

    await user.hover(bodyTrigger());
    const panel = await screen.findByTestId("quota-account-detail-panel");

    // 正文没显示的两个窗口也要在面板里；受限窗口标出
    expect(panel.textContent).toContain("每周");
    expect(panel.textContent).toContain("每月");
    expect(panel.textContent).toContain("5 小时");
    expect(panel.textContent).toContain("受限");
    expect(panel.textContent).toContain("9.7 / 10");
    expect(panel.textContent).toContain("$38 / $100");
    expect(panel.textContent).toContain("剩余额度：");
    expect(panel.textContent).toContain("12.5 USD");
    expect(panel.textContent).toContain("窗口用尽后扣钱包");
  });

  it("键盘聚焦正文触发区同样打开详情面板", async () => {
    const user = userEvent.setup();
    const account = makeAccount({
      windows: [
        makeWindow({ label: "每月", used_percent: 38 }),
        makeWindow({ kind: "weekly", label: "每周", used_percent: 82 }),
      ],
    });
    render(
      <QuotaAccountCard account={account} onTest={vi.fn()} onEdit={vi.fn()} />,
    );

    // 真实键盘路径：Tab 依次经过刷新、··· 菜单，第三站是正文触发区
    const trigger = bodyTrigger();
    expect(trigger).toHaveAttribute("tabindex", "0");
    await user.tab();
    await user.tab();
    await user.tab();
    expect(document.activeElement).toBe(trigger);

    const panel = await screen.findByTestId("quota-account-detail-panel");
    expect(panel.textContent).toContain("每月");
    expect(panel.textContent).toContain("每周");
  });

  it("正文结构与窗口数无关：一根受限进度条 + 一个触发区", () => {
    const { container, rerender } = render(
      <QuotaAccountCard
        account={makeAccount({
          windows: [
            makeWindow({ kind: "daily", label: "每天", used_percent: 30 }),
          ],
        })}
        onTest={vi.fn()}
        onEdit={vi.fn()}
      />,
    );
    const content = container.querySelector('[data-slot="card-content"]');
    expect(screen.getAllByRole("progressbar")).toHaveLength(1);
    // 触发区是 content 下唯一的布局块（面板走 portal，不占正文高度）
    expect(content?.children).toHaveLength(1);

    rerender(
      <QuotaAccountCard
        account={makeAccount({
          windows: [
            makeWindow({ kind: "daily", label: "每天", used_percent: 30 }),
            makeWindow({ kind: "weekly", label: "每周", used_percent: 82 }),
            makeWindow({ label: "每月", used_percent: 97 }),
          ],
        })}
        onTest={vi.fn()}
        onEdit={vi.fn()}
      />,
    );
    expect(screen.getAllByRole("progressbar")).toHaveLength(1);
    expect(content?.children).toHaveLength(1);
    // 受限窗口随数据换成 97% 的每月
    expect(screen.getByText("每月")).toBeInTheDocument();
    expect(screen.getByTestId("quota-window")).toHaveAttribute(
      "data-tone",
      "danger",
    );
  });

  it("多池账号在「数据时间」行右侧显示池选择器，默认选中消耗中的池", async () => {
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
    const { container } = render(
      <QuotaAccountCard account={account} onTest={vi.fn()} onEdit={vi.fn()} />,
    );

    const standardTab = screen.getByRole("tab", { name: "标准（已用尽）" });
    const coreTab = screen.getByRole("tab", { name: "Core" });
    // 选择器只显示池名，用尽状态进 aria-label/title
    expect(standardTab).toHaveTextContent("标准");
    expect(standardTab).not.toHaveTextContent("用尽");
    expect(standardTab).toHaveAttribute("title", "标准（已用尽）");
    expect(coreTab).toHaveTextContent("Core");
    // 选择器与「数据时间」同一行（同一父容器），整行固定 h-7——双池卡片因此
    // 与单池卡片等高；选择器在 header，不属于正文悬浮触发区
    const timeRow = screen.getByText(/数据时间/).parentElement;
    expect(timeRow).toHaveClass("h-7");
    expect(timeRow).toContainElement(standardTab);
    expect(timeRow).toContainElement(coreTab);
    expect(bodyTrigger()).not.toContainElement(standardTab);
    // 正文仍是单行区块，多一个选择器没有让正文多出行
    expect(
      container.querySelector('[data-slot="card-content"]')?.children,
    ).toHaveLength(1);

    // 标准池用尽 → 默认打开正在消耗的 Core 池，正文行是它的受限窗口
    expect(coreTab).toHaveAttribute("aria-selected", "true");
    expect(screen.getByText("5 小时")).toBeInTheDocument();
    expect(screen.getByText("87%")).toBeInTheDocument();
    expect(screen.getAllByTestId("quota-window")).toHaveLength(1);

    await user.click(standardTab);
    expect(standardTab).toHaveAttribute("aria-selected", "true");
    // 切到已用尽的标准池后，正文那一行换成该池的受限窗口
    expect(screen.getByText("每月")).toBeInTheDocument();
    expect(screen.getByText("100%")).toBeInTheDocument();
    expect(screen.queryByText("5 小时")).toBeNull();
    expect(screen.getAllByTestId("quota-window")).toHaveLength(1);
  });

  it("悬浮面板按池分组：池名标题带用尽/消耗中状态，含全部池的窗口", async () => {
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

    await user.hover(bodyTrigger());
    const panel = await screen.findByTestId("quota-account-detail-panel");

    expect(panel.textContent).toContain("标准");
    expect(panel.textContent).toContain("已用尽");
    expect(panel.textContent).toContain("Core");
    expect(panel.textContent).toContain("消耗中");
    // 两池窗口都在：正文只显示 Core 的 5 小时，标准池的每月只出现在面板
    expect(panel.textContent).toContain("每月");
    expect(panel.textContent).toContain("5 小时");
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

    await user.click(screen.getByRole("tab", { name: "标准（已用尽）" }));
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

    expect(
      screen.getByRole("tab", { name: "标准（已用尽）" }),
    ).toHaveAttribute("aria-selected", "true");
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

    await user.click(screen.getByRole("tab", { name: "标准（已用尽）" }));
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
    expect(screen.getByText("87%")).toBeInTheDocument();
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

  it("pending 或没有窗口数据时显示等待抓取提示，不渲染窗口结构", () => {
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
    expect(
      screen.getByText("还没有额度数据，等待后台抓取"),
    ).toBeInTheDocument();
    // 这种卡片本来无法等高，不渲染单行额度结构
    expect(screen.queryByRole("progressbar")).toBeNull();
    expect(
      screen.queryByLabelText("查看 我的 Pro 的额度详情"),
    ).toBeNull();
  });

  // OpenDesign 无 Design Plan 额度时只会给钱包余额 + 「按钱包计费」说明，没有窗口。
  // 这种卡片不能因为窗口为空就只剩「还没有额度数据」，否则余额与计费说明完全看不到。
  it("没有窗口但有余额时，正文一行显示余额，悬浮面板仍可看到说明", async () => {
    const user = userEvent.setup();
    const account = makeAccount({
      provider: "opendesign",
      provider_name: "OpenDesign",
      label: "设计稿账号",
      status: "ok",
      windows: [],
      balance: { label: "钱包余额", amount: 1.03, currency: "USD" },
      note: "无 Design Plan 额度，按钱包计费",
    });
    render(
      <QuotaAccountCard account={account} onTest={vi.fn()} onEdit={vi.fn()} />,
    );

    expect(screen.queryByText(/还没有额度数据/)).toBeNull();
    // 正文唯一一行是纯文本余额：「钱包余额  $1.03」，没有进度条
    const trigger = bodyTrigger("设计稿账号");
    expect(within(trigger).getByText("钱包余额")).toBeInTheDocument();
    expect(within(trigger).getByText("$1.03")).toBeInTheDocument();
    expect(screen.queryByRole("progressbar")).toBeNull();

    await user.hover(trigger);
    const panel = await screen.findByTestId("quota-account-detail-panel");
    expect(within(panel).getByText(/无 Design Plan 额度/)).toBeInTheDocument();
    expect(within(panel).getByText(/1\.03/)).toBeInTheDocument();
  });

  it("余额与备注只出现在悬浮面板，不进正文", async () => {
    const user = userEvent.setup();
    const account = makeAccount({
      balance: { label: "钱包余额", amount: 1.5, currency: "USD" },
      note: "窗口用尽后扣钱包",
      windows: [makeWindow({ used_percent: 38 })],
    });
    render(
      <QuotaAccountCard account={account} onTest={vi.fn()} onEdit={vi.fn()} />,
    );

    expect(screen.queryByTestId("quota-account-note")).toBeNull();
    expect(screen.queryByText("1.5 USD")).toBeNull();

    await user.hover(bodyTrigger());
    const panel = await screen.findByTestId("quota-account-detail-panel");
    const note = within(panel).getByTestId("quota-account-note");
    expect(note).toHaveTextContent("窗口用尽后扣钱包");
    expect(note).toHaveClass("text-muted-foreground");
    expect(panel.textContent).toContain("1.5 USD");
    // 余额在备注之前
    const text = panel.textContent ?? "";
    expect(text.indexOf("1.5 USD")).toBeLessThan(
      text.indexOf("窗口用尽后扣钱包"),
    );
  });

  it("note 为空串时面板里不渲染补充说明", async () => {
    const user = userEvent.setup();
    const account = makeAccount({
      note: "",
      windows: [makeWindow({ used_percent: 38 })],
    });
    render(
      <QuotaAccountCard account={account} onTest={vi.fn()} onEdit={vi.fn()} />,
    );

    await user.hover(bodyTrigger());
    await screen.findByTestId("quota-account-detail-panel");
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
