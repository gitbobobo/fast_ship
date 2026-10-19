import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { QuotaAccountMenu } from "./account-menu";
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

describe("QuotaAccountMenu", () => {
  const deleteMutate = vi.fn();

  beforeEach(() => {
    deleteMutate.mockReset().mockResolvedValue({});
    vi.mocked(useRefreshQuotaAccount).mockReturnValue({
      mutateAsync: vi.fn(),
      isPending: false,
    } as unknown as ReturnType<typeof useRefreshQuotaAccount>);
    vi.mocked(useDeleteQuotaAccount).mockReturnValue({
      mutateAsync: deleteMutate,
      isPending: false,
    } as unknown as ReturnType<typeof useDeleteQuotaAccount>);
  });

  it("确认删除调用删除接口并关闭确认框", async () => {
    const user = userEvent.setup();
    render(
      <QuotaAccountMenu
        account={makeAccount()}
        onEdit={vi.fn()}
        onTest={vi.fn()}
      />,
    );

    await user.click(
      screen.getByRole("button", { name: "更多操作 我的 Pro" }),
    );
    await user.click(await screen.findByRole("menuitem", { name: "删除" }));
    expect(await screen.findByText("删除额度账号？")).toBeInTheDocument();

    // AlertDialogAction 是纯 Button 不会自动关框，handleDelete 的 finally 关
    await user.click(screen.getByRole("button", { name: "确认删除" }));
    expect(deleteMutate).toHaveBeenCalledWith("acc-1");
    await waitFor(() =>
      expect(screen.queryByText("删除额度账号？")).toBeNull(),
    );
  });

  it("删除失败也关闭确认框，不留在打开状态", async () => {
    deleteMutate.mockRejectedValue(new Error("boom"));
    const user = userEvent.setup();
    render(
      <QuotaAccountMenu
        account={makeAccount()}
        onEdit={vi.fn()}
        onTest={vi.fn()}
      />,
    );

    await user.click(
      screen.getByRole("button", { name: "更多操作 我的 Pro" }),
    );
    await user.click(await screen.findByRole("menuitem", { name: "删除" }));
    await user.click(
      await screen.findByRole("button", { name: "确认删除" }),
    );

    expect(deleteMutate).toHaveBeenCalledWith("acc-1");
    await waitFor(() =>
      expect(screen.queryByText("删除额度账号？")).toBeNull(),
    );
  });
});
