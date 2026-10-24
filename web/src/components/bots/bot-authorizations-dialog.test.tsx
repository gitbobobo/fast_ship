import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { toast } from "sonner";
import { BotAuthorizationsDialog } from "./bot-authorizations-dialog";
import { ApiRequestError } from "@/lib/api/client";
import {
  useBotAuthorizations,
  useBotPolicyRules,
} from "@/lib/hooks/use-bots";
import type { BotAuthorization, BotWritePolicyRule } from "@/lib/api/bots";

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

const { revokeMutateAsync, toggleMutateAsync } = vi.hoisted(() => ({
  revokeMutateAsync: vi.fn(),
  toggleMutateAsync: vi.fn(),
}));

vi.mock("@/lib/hooks/use-bots", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/hooks/use-bots")>()),
  useBotAuthorizations: vi.fn(() => ({ data: [], isLoading: false })),
  useRevokeAuthorization: vi.fn(() => ({
    mutateAsync: revokeMutateAsync,
    isPending: false,
  })),
  useBotPolicyRules: vi.fn(() => ({ data: [], isLoading: false })),
  useSetPolicyRuleEnabled: vi.fn(() => ({
    mutateAsync: toggleMutateAsync,
    isPending: false,
  })),
}));

function makeAuthz(
  overrides: Partial<BotAuthorization> = {},
): BotAuthorization {
  return {
    id: "authz-1",
    tool_name: "fs_schedule",
    action: "run",
    scope_kind: "schedule_task",
    scope_id: "task-1",
    environment_id: null,
    constraints: {},
    created_at: "2026-10-20T00:00:00Z",
    ...overrides,
  } as BotAuthorization;
}

function makeRule(
  overrides: Partial<BotWritePolicyRule> = {},
): BotWritePolicyRule {
  return {
    rule_id: "thread_send_continue",
    name: "自动发送「继续」",
    description: "对空闲且有运行痕迹的线程发送精确模板",
    enabled: true,
    false_positive_count_24h: 0,
    ...overrides,
  } as BotWritePolicyRule;
}

function renderDialog() {
  return render(
    <BotAuthorizationsDialog botId="b1" open onClose={vi.fn()} />,
  );
}

// INT-160：授权展示过期时间；分级放行规则区展示清单与开关，开关走
// useSetPolicyRuleEnabled；误判自动降级的规则要说明状态。
describe("BotAuthorizationsDialog 分级放行与限时授权", () => {
  it("授权项展示过期时间；过期授权显示已过期说明", () => {
    vi.mocked(useBotAuthorizations).mockReturnValue({
      data: [
        makeAuthz({
          id: "live",
          expires_at: "2099-01-01T00:00:00Z",
        }),
        makeAuthz({
          id: "stale",
          tool_name: "fs_issue",
          action: "create",
          expires_at: "2020-01-01T00:00:00Z",
        }),
      ],
      isLoading: false,
    } as ReturnType<typeof useBotAuthorizations>);
    renderDialog();
    const items = screen.getAllByTestId("bot-authorization-item");
    expect(
      within(items[0]).getByText(/过期/),
    ).toBeInTheDocument();
    expect(within(items[1]).getByText(/已过期/)).toBeInTheDocument();
  });

  it("分级放行规则区展示清单，开关回调带规则与目标状态", async () => {
    vi.mocked(useBotAuthorizations).mockReturnValue({
      data: [],
      isLoading: false,
    } as unknown as ReturnType<typeof useBotAuthorizations>);
    vi.mocked(useBotPolicyRules).mockReturnValue({
      data: [
        makeRule(),
        makeRule({
          rule_id: "thread_settle_idle",
          name: "自动结算平静线程",
          enabled: false,
          false_positive_count_24h: 2,
          auto_disabled_at: "2026-10-23T00:00:00Z",
        }),
      ],
      isLoading: false,
    } as ReturnType<typeof useBotPolicyRules>);
    toggleMutateAsync.mockResolvedValue({});
    renderDialog();

    const items = screen.getAllByTestId("bot-policy-rule-item");
    expect(items).toHaveLength(2);
    // 开启中的规则：开关 on，误判计数行不出现。
    const sendRule = items[0];
    expect(
      within(sendRule).getByRole("switch", {
        name: "开关规则 自动发送「继续」",
      }),
    ).toHaveAttribute("aria-checked", "true");

    // 自动降级的规则：开关 off + 降级说明。
    const settleRule = items[1];
    const settleSwitch = within(settleRule).getByRole("switch", {
      name: "开关规则 自动结算平静线程",
    });
    expect(settleSwitch).toHaveAttribute("aria-checked", "false");
    expect(
      within(settleRule).getByText(/自动降级/),
    ).toBeInTheDocument();

    // 关闭开启中的规则 → enabled=false 交给 hook。
    await userEvent.click(
      within(sendRule).getByRole("switch", {
        name: "开关规则 自动发送「继续」",
      }),
    );
    await waitFor(() => {
      expect(toggleMutateAsync).toHaveBeenCalledWith({
        ruleId: "thread_send_continue",
        enabled: false,
      });
    });
  });

  it("开关失败时弹错误提示", async () => {
    vi.mocked(useBotAuthorizations).mockReturnValue({
      data: [],
      isLoading: false,
    } as unknown as ReturnType<typeof useBotAuthorizations>);
    vi.mocked(useBotPolicyRules).mockReturnValue({
      data: [makeRule()],
      isLoading: false,
    } as ReturnType<typeof useBotPolicyRules>);
    toggleMutateAsync.mockRejectedValue(
      new ApiRequestError(500, null, "开关失败"),
    );
    renderDialog();
    await userEvent.click(
      screen.getByRole("switch", { name: "开关规则 自动发送「继续」" }),
    );
    await waitFor(() => {
      expect(toast.error).toHaveBeenCalledWith("开关失败");
    });
  });
});
