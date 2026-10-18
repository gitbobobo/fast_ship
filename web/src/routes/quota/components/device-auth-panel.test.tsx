import { useState } from "react";
import { act, fireEvent, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QuotaDeviceAuthPanel } from "./device-auth-panel";
import { renderWithRoute } from "@/test/render";
import { signInAs, teardownAuth } from "@/test/auth";
import { ApiRequestError } from "@/lib/api/client";
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

const NOW = new Date("2026-10-18T12:00:00Z").getTime();
const POLL_MS = 2_000;

function makeAuth(
  overrides: Partial<QuotaDeviceAuthorization> = {},
): QuotaDeviceAuthorization {
  return {
    id: "da-1",
    provider: "opendesign",
    status: "pending",
    user_code: "ABCD-EFGH",
    activation_url: "https://open-design.ai/activate",
    expires_at: new Date(NOW + 598_000).toISOString(),
    poll_interval_seconds: POLL_MS / 1000,
    account_id: "",
    error: "",
    ...overrides,
  };
}

const res = (data: QuotaDeviceAuthorization) => ({
  code: 0,
  message: "",
  data,
});

const NULL_RES = { code: 0, message: "", data: null };

/** 与真实对话框一致：onCancel/onApproved 时把面板卸载（收尾由父组件负责）。 */
function Harness({
  authorization,
  restarting = false,
  onApproved = vi.fn(),
  onCancel = vi.fn(),
  onRestart = vi.fn(),
}: {
  authorization: QuotaDeviceAuthorization;
  restarting?: boolean;
  onApproved?: () => void;
  onCancel?: () => void;
  onRestart?: () => void;
}) {
  const [gone, setGone] = useState(false);
  if (gone) return <p>已回到表单</p>;
  return (
    <QuotaDeviceAuthPanel
      authorization={authorization}
      restarting={restarting}
      onApproved={() => {
        onApproved();
        setGone(true);
      }}
      onCancel={() => {
        onCancel();
        setGone(true);
      }}
      onRestart={onRestart}
    />
  );
}

function renderPanel(
  props: Partial<Parameters<typeof Harness>[0]> = {},
) {
  return renderWithRoute(
    <Harness authorization={makeAuth()} {...props} />,
    { path: "/", initialEntry: "/" },
  );
}

beforeEach(() => {
  signInAs("A");
  vi.useFakeTimers();
  vi.setSystemTime(NOW);
  vi.mocked(quotaApi.getDeviceAuthorization).mockResolvedValue(
    res(makeAuth()),
  );
  vi.mocked(quotaApi.cancelDeviceAuthorization).mockResolvedValue(NULL_RES);
});

afterEach(() => {
  teardownAuth();
  vi.useRealTimers();
  vi.clearAllMocks();
});

describe("QuotaDeviceAuthPanel", () => {
  it("pending：验证码、批准链接、倒计时与「等待批准」", async () => {
    renderPanel();

    expect(screen.getByTestId("device-auth-panel")).toHaveAttribute(
      "data-status",
      "pending",
    );
    expect(screen.getByTestId("device-auth-user-code")).toHaveTextContent(
      "ABCD-EFGH",
    );
    const link = screen.getByRole("link", { name: /打开批准页面/ });
    expect(link).toHaveAttribute("href", "https://open-design.ai/activate");
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noreferrer");
    expect(screen.getByText("等待在浏览器批准…")).toBeInTheDocument();
    expect(screen.getByTestId("device-auth-countdown")).toHaveTextContent(
      "剩余 9:58",
    );

    // 倒计时每秒自走
    await act(() => vi.advanceTimersByTimeAsync(1000));
    expect(screen.getByTestId("device-auth-countdown")).toHaveTextContent(
      "剩余 9:57",
    );

    // 越过有效期后显示「已过期」
    vi.setSystemTime(NOW + 599_000);
    await act(() => vi.advanceTimersByTimeAsync(1000));
    expect(screen.getByTestId("device-auth-countdown")).toHaveTextContent(
      "已过期",
    );
  });

  it("按 poll_interval_seconds 轮询，approved 后回调一次并停轮", async () => {
    const onApproved = vi.fn();
    vi.mocked(quotaApi.getDeviceAuthorization)
      .mockResolvedValueOnce(res(makeAuth()))
      .mockResolvedValue(
        res(makeAuth({ status: "approved", account_id: "acc-9" })),
      );
    renderPanel({ onApproved });

    // initialData 复用 POST 视图，首个 GET 在一个轮询周期后才发出
    expect(quotaApi.getDeviceAuthorization).not.toHaveBeenCalled();

    await act(() => vi.advanceTimersByTimeAsync(POLL_MS));
    expect(quotaApi.getDeviceAuthorization).toHaveBeenCalledTimes(1);
    expect(quotaApi.getDeviceAuthorization).toHaveBeenCalledWith("da-1");

    await act(() => vi.advanceTimersByTimeAsync(POLL_MS));
    expect(quotaApi.getDeviceAuthorization).toHaveBeenCalledTimes(2);
    // approved 回调不带参数（B1）：父组件只负责收尾与关对话框
    expect(onApproved).toHaveBeenCalledExactlyOnceWith();

    // 面板随 approved 卸载（Harness），不再轮询；面板自身不发 DELETE
    await act(() => vi.advanceTimersByTimeAsync(POLL_MS * 5));
    expect(quotaApi.getDeviceAuthorization).toHaveBeenCalledTimes(2);
    expect(quotaApi.cancelDeviceAuthorization).not.toHaveBeenCalled();
  });

  it.each([
    ["expired", "验证码已过期，请重新发起授权。"],
    ["denied", "授权已被拒绝，请重新发起授权。"],
    ["invalid", "设备授权已失效，请重新发起授权。"],
  ] as const)(
    "%s：展示服务端原因并停轮，「重新发起」「取消」可用",
    async (status, message) => {
      const onRestart = vi.fn();
      const onCancel = vi.fn();
      vi.mocked(quotaApi.getDeviceAuthorization).mockResolvedValue(
        res(makeAuth({ status, error: message })),
      );
      renderPanel({ onRestart, onCancel });

      await act(() => vi.advanceTimersByTimeAsync(POLL_MS));
      // findBy/waitFor 的内部轮询依赖 setInterval，fake timers 下不走，
      // act 里推进时钟后状态已同步渲染，直接用同步查询。
      expect(screen.getByText(message)).toBeInTheDocument();
      expect(screen.getByTestId("device-auth-panel")).toHaveAttribute(
        "data-status",
        status,
      );

      // 终态停轮
      await act(() => vi.advanceTimersByTimeAsync(POLL_MS * 5));
      expect(quotaApi.getDeviceAuthorization).toHaveBeenCalledTimes(1);

      fireEvent.click(screen.getByRole("button", { name: "重新发起" }));
      expect(onRestart).toHaveBeenCalledTimes(1);

      fireEvent.click(screen.getByRole("button", { name: "取消" }));
      expect(onCancel).toHaveBeenCalledTimes(1);
      // 取消只经父组件回调（B1）：面板自身不发 DELETE
      await act(() => vi.advanceTimersByTimeAsync(0));
      expect(quotaApi.cancelDeviceAuthorization).not.toHaveBeenCalled();
    },
  );

  it("GET 返回 404/40431 视为会话失效（invalid）并停轮", async () => {
    vi.mocked(quotaApi.getDeviceAuthorization).mockRejectedValue(
      new ApiRequestError(404, 40431, "授权会话不存在或已过期"),
    );
    renderPanel();

    await act(() => vi.advanceTimersByTimeAsync(POLL_MS));
    expect(screen.getByText("授权会话不存在或已过期")).toBeInTheDocument();
    expect(screen.getByTestId("device-auth-panel")).toHaveAttribute(
      "data-status",
      "invalid",
    );

    await act(() => vi.advanceTimersByTimeAsync(POLL_MS * 5));
    expect(quotaApi.getDeviceAuthorization).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("button", { name: "重新发起" })).toBeEnabled();
  });

  it("普通网络错误保持 pending 继续轮询（抖动自愈）", async () => {
    vi.mocked(quotaApi.getDeviceAuthorization)
      .mockRejectedValueOnce(new Error("network down"))
      .mockResolvedValue(res(makeAuth()));
    renderPanel();

    await act(() => vi.advanceTimersByTimeAsync(POLL_MS));
    // 第一次 GET 失败，视图仍是 pending
    expect(screen.getByTestId("device-auth-panel")).toHaveAttribute(
      "data-status",
      "pending",
    );

    await act(() => vi.advanceTimersByTimeAsync(POLL_MS));
    expect(quotaApi.getDeviceAuthorization).toHaveBeenCalledTimes(2);
  });

  it("「取消」通知父组件；面板卸载后不再轮询也不发 DELETE", async () => {
    const onCancel = vi.fn();
    renderPanel({ onCancel });

    // 先把倒计时跑一下，确认轮询在走
    await act(() => vi.advanceTimersByTimeAsync(POLL_MS));
    expect(quotaApi.getDeviceAuthorization).toHaveBeenCalledTimes(1);

    fireEvent.click(screen.getByRole("button", { name: "取消" }));
    expect(onCancel).toHaveBeenCalledTimes(1);
    expect(screen.getByText("已回到表单")).toBeInTheDocument();

    // 卸载后不再轮询；会话收尾是父组件 discardSession 的职责（B1），
    // 面板自身在任何路径都不直接调 cancelDeviceAuthorization
    await act(() => vi.advanceTimersByTimeAsync(POLL_MS * 5));
    expect(quotaApi.getDeviceAuthorization).toHaveBeenCalledTimes(1);
    expect(quotaApi.cancelDeviceAuthorization).not.toHaveBeenCalled();
  });

  it("重新发起换来新会话：轮询新会话，旧会话收尾由父组件负责", async () => {
    const onRestart = vi.fn();
    vi.mocked(quotaApi.getDeviceAuthorization).mockResolvedValue(
      res(makeAuth({ status: "expired", error: "验证码已过期，请重新发起授权。" })),
    );

    function RestartHarness() {
      const [auth, setAuth] = useState(makeAuth());
      return (
        <QuotaDeviceAuthPanel
          authorization={auth}
          onApproved={vi.fn()}
          onCancel={vi.fn()}
          onRestart={() => {
            onRestart();
            setAuth(makeAuth({ id: "da-2", user_code: "WXYZ-1234" }));
          }}
        />
      );
    }
    renderWithRoute(<RestartHarness />, { path: "/", initialEntry: "/" });

    await act(() => vi.advanceTimersByTimeAsync(POLL_MS));
    expect(
      screen.getByText("验证码已过期，请重新发起授权。"),
    ).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "重新发起" }));
    expect(onRestart).toHaveBeenCalledTimes(1);
    // 换会话后立刻回到 pending 视图
    expect(screen.getByTestId("device-auth-user-code")).toHaveTextContent(
      "WXYZ-1234",
    );
    expect(screen.getByTestId("device-auth-panel")).toHaveAttribute(
      "data-status",
      "pending",
    );

    // 换会话后旧会话的 DELETE 不再由面板发（B1）
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(quotaApi.cancelDeviceAuthorization).not.toHaveBeenCalled();

    // 新会话按自己的 queryKey 轮询
    vi.mocked(quotaApi.getDeviceAuthorization).mockResolvedValue(
      res(makeAuth({ id: "da-2" })),
    );
    await act(() => vi.advanceTimersByTimeAsync(POLL_MS));
    expect(quotaApi.getDeviceAuthorization).toHaveBeenCalledWith("da-2");
  });

  it("后台标签页（document hidden）也继续轮询（B3）", async () => {
    renderPanel();
    await act(() => vi.advanceTimersByTimeAsync(POLL_MS));
    expect(quotaApi.getDeviceAuthorization).toHaveBeenCalledTimes(1);

    // 渲染层行为验证 refetchIntervalInBackground：React Query v5 的 interval
    // 每次到点时检查 refetchIntervalInBackground || focusManager.isFocused()，
    // 后者实时读 document.visibilityState。把 document 置 hidden 后推进时钟，
    // interval 仍应继续打 GET（批准天然发生在另一个标签页）。
    const descriptor = Object.getOwnPropertyDescriptor(
      document,
      "visibilityState",
    );
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => "hidden",
    });
    try {
      window.dispatchEvent(new Event("visibilitychange"));
      await act(() => vi.advanceTimersByTimeAsync(POLL_MS * 3));
      expect(
        vi.mocked(quotaApi.getDeviceAuthorization).mock.calls.length,
      ).toBeGreaterThan(1);
    } finally {
      if (descriptor) {
        Object.defineProperty(document, "visibilityState", descriptor);
      } else {
        delete (document as unknown as Record<string, unknown>)
          .visibilityState;
      }
      window.dispatchEvent(new Event("visibilitychange"));
    }
  });
});
