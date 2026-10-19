import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { QuotaAttentionBanner } from "./attention-banner";
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
    id: "acc-bad",
    provider: "factory",
    provider_name: "Factory Droid",
    label: "失效号",
    credential_hint: "…ab12",
    plan_name: "Pro",
    status: "credential_invalid",
    last_error: "凭据已过期",
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

describe("QuotaAttentionBanner", () => {
  const refreshMutate = vi.fn();
  const deleteMutate = vi.fn();

  beforeEach(() => {
    refreshMutate.mockReset().mockResolvedValue({ data: { status: "ok" } });
    deleteMutate.mockReset().mockResolvedValue({});
    vi.mocked(useRefreshQuotaAccount).mockReturnValue({
      mutateAsync: refreshMutate,
      isPending: false,
    } as unknown as ReturnType<typeof useRefreshQuotaAccount>);
    vi.mocked(useDeleteQuotaAccount).mockReturnValue({
      mutateAsync: deleteMutate,
      isPending: false,
    } as unknown as ReturnType<typeof useDeleteQuotaAccount>);
  });

  it("保留「重新连接」主按钮，··· 菜单里刷新/编辑/测试凭据/删除都可达", async () => {
    const user = userEvent.setup();
    const onEdit = vi.fn();
    const onTest = vi.fn();
    const account = makeAccount();
    render(
      <QuotaAttentionBanner
        accounts={[account]}
        onEdit={onEdit}
        onTest={onTest}
      />,
    );

    // 主按钮仍是重新连接（打开编辑对话框重贴凭据）
    await user.click(screen.getByRole("button", { name: "重新连接" }));
    expect(onEdit).toHaveBeenCalledWith(account);

    // 菜单带刷新（withRefresh）：临时接口异常可手动重试
    await user.click(
      screen.getByRole("button", { name: "更多操作 失效号" }),
    );
    await user.click(await screen.findByRole("menuitem", { name: "刷新" }));
    expect(refreshMutate).toHaveBeenCalledWith("acc-bad");

    // 测试凭据
    await user.click(
      screen.getByRole("button", { name: "更多操作 失效号" }),
    );
    await user.click(
      await screen.findByRole("menuitem", { name: "测试凭据" }),
    );
    expect(onTest).toHaveBeenCalledWith(account);

    // 删除可达确认框，失效账号可以删掉
    await user.click(
      screen.getByRole("button", { name: "更多操作 失效号" }),
    );
    await user.click(await screen.findByRole("menuitem", { name: "删除" }));
    await user.click(
      await screen.findByRole("button", { name: "确认删除" }),
    );
    expect(deleteMutate).toHaveBeenCalledWith("acc-bad");
  });

  it("provider_error 账号同样在横幅里给出菜单", () => {
    render(
      <QuotaAttentionBanner
        accounts={[makeAccount({ status: "provider_error" })]}
        onEdit={vi.fn()}
        onTest={vi.fn()}
      />,
    );
    expect(screen.getByText("接口异常")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "更多操作 失效号" }),
    ).toBeInTheDocument();
  });
});
