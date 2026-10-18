import { useEffect, useRef } from "react";
import { useQuery } from "@tanstack/react-query";
import { Check, Copy, ExternalLinkIcon, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { ApiRequestError } from "@/lib/api/client";
import { quotaApi } from "@/lib/api/quota";
import { copyWithToast } from "@/lib/copy";
import { useNow } from "@/lib/hooks/use-now";
import { errorText } from "@/lib/quota";
import { useAuthStore } from "@/lib/store/auth-store";

/** status 为终态但 error 为空时的兜底文案（与服务端口径一致）。 */
const TERMINAL_FALLBACK: Record<
  Exclude<QuotaDeviceAuthStatus, "pending" | "approved">,
  string
> = {
  expired: "验证码已过期，请重新发起授权。",
  denied: "授权已被拒绝，请重新发起授权。",
  invalid: "设备授权已失效，请重新发起授权。",
};

/**
 * 404 / 40431 = 服务端会话已不存在（会话只存内存，进程重启即丢），
 * 对用户来说就是「设备授权已失效」，按 invalid 展示并停轮。
 */
function isSessionGone(error: unknown): boolean {
  return (
    error instanceof ApiRequestError &&
    (error.status === 404 || error.code === 40431)
  );
}

/**
 * 会话状态轮询：用 useQuery 而不是自建 setInterval——
 * 1. initialData 直接复用 POST 返回的 pending 视图 + staleTime 无限，
 *    首个 GET 严格在 poll_interval_seconds 后才发出；
 * 2. refetchInterval 按最新数据决定下一次：approved/终态/会话失效自动停轮，
 *    普通网络错误（数据仍 pending）继续轮，抖动自愈；
 * 3. refetchIntervalInBackground：批准天然发生在另一个标签页，React Query v5
 *    默认在 document hidden 时跳过 interval，这里必须在后台也继续轮；
 * 4. 卸载时 React Query 自己清理观察者，天然不泄漏轮询，无需手写 clearInterval。
 */
function useDeviceAuthorizationQuery(authorization: QuotaDeviceAuthorization) {
  const userId = useAuthStore((state) => state.user?.id ?? null);
  // upstream 给的轮询节奏（秒），缺省/异常值兜底 2s（与契约一致）
  const pollMs = Math.max(1, authorization.poll_interval_seconds || 2) * 1000;
  return useQuery({
    // 独立 root：账号列表 invalidate(["quota", userId]) 不应触碰会话轮询
    queryKey: ["quota-device-authorization", userId, authorization.id],
    queryFn: async () =>
      (await quotaApi.getDeviceAuthorization(authorization.id)).data,
    enabled: Boolean(userId),
    initialData: authorization,
    staleTime: Number.POSITIVE_INFINITY,
    retry: false,
    refetchInterval: (query) =>
      query.state.data?.status === "pending" &&
      !isSessionGone(query.state.error)
        ? pollMs
        : false,
    refetchIntervalInBackground: true,
  });
}

/** 「剩余 9:58」式倒计时；已过期待服务端判定，本地同样显示「已过期」。 */
function formatAuthCountdown(expiresAt: string, now: number): string {
  const remainMs = new Date(expiresAt).getTime() - now;
  if (Number.isNaN(remainMs) || remainMs <= 0) return "已过期";
  const totalSeconds = Math.floor(remainMs / 1000);
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = String(totalSeconds % 60).padStart(2, "0");
  return `剩余 ${minutes}:${seconds}`;
}

/**
 * 设备授权面板（INT-117）：验证码 + 批准链接 + 倒计时 + 状态轮询。
 * approved → onApproved（父组件负责 toast、关对话框、刷新账号列表）；
 * expired/denied/invalid → error + 「重新发起」（父组件重新 POST 换会话）+「取消」。
 * 面板只做展示与轮询：会话收尾（pending 会话的 DELETE）由父组件统一负责，
 * 轮询随卸载自动停止。
 */
export function QuotaDeviceAuthPanel({
  authorization,
  restarting = false,
  onApproved,
  onCancel,
  onRestart,
}: {
  authorization: QuotaDeviceAuthorization;
  /** 父组件正在重新 POST 起新会话（「重新发起」加载态） */
  restarting?: boolean;
  /** 服务端已用授权到的密钥自动建号/换凭据 */
  onApproved: () => void;
  /** 放弃授权回到表单（父组件负责收尾会话） */
  onCancel: () => void;
  /** 终态后重新 POST；父组件用新会话替换 authorization prop */
  onRestart: () => void;
}) {
  const query = useDeviceAuthorizationQuery(authorization);

  const gone = isSessionGone(query.error);
  const status: QuotaDeviceAuthStatus = gone ? "invalid" : query.data.status;
  const pending = status === "pending";
  const terminalError = gone
    ? errorText(query.error, TERMINAL_FALLBACK.invalid)
    : query.data.error;

  // approved 只通知一次；重新发起换来新会话后允许再次通知
  const approvedRef = useRef(false);
  useEffect(() => {
    approvedRef.current = false;
  }, [authorization.id]);
  useEffect(() => {
    if (status !== "approved" || approvedRef.current) return;
    approvedRef.current = true;
    onApproved();
  }, [status, onApproved]);

  // 倒计时每秒自走（仅 pending 时走表）
  const now = useNow(1000, pending);
  const countdown = pending
    ? formatAuthCountdown(authorization.expires_at, now)
    : null;

  if (status === "approved") {
    return (
      <p
        data-testid="device-auth-panel"
        data-status="approved"
        className="flex items-center gap-2 py-6 text-sm text-emerald-700 dark:text-emerald-300"
      >
        <Check className="size-4" />
        授权成功，正在完成…
      </p>
    );
  }

  if (!pending) {
    return (
      <div
        data-testid="device-auth-panel"
        data-status={status}
        className="space-y-4"
      >
        <div className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive">
          <p className="font-medium">授权未完成</p>
          <p className="mt-0.5">
            {terminalError || TERMINAL_FALLBACK[status]}
          </p>
        </div>
        <div className="flex justify-end gap-2">
          <Button type="button" variant="outline" onClick={onCancel}>
            取消
          </Button>
          <Button type="button" onClick={onRestart} disabled={restarting}>
            {restarting ? "重新发起中..." : "重新发起"}
          </Button>
        </div>
      </div>
    );
  }

  return (
    <div
      data-testid="device-auth-panel"
      data-status="pending"
      className="space-y-4"
    >
      <div className="space-y-3 rounded-md border bg-muted/40 px-4 py-5 text-center">
        <p className="text-xs text-muted-foreground">
          打开批准页面并输入此验证码
        </p>
        <div className="flex items-center justify-center gap-1.5">
          <span
            data-testid="device-auth-user-code"
            className="font-mono text-2xl font-semibold tracking-widest"
          >
            {authorization.user_code}
          </span>
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            aria-label="复制验证码"
            title="复制验证码"
            onClick={() =>
              void copyWithToast(authorization.user_code, "验证码已复制")
            }
          >
            <Copy className="h-3.5 w-3.5" />
          </Button>
        </div>
        <div>
          <Button
            render={
              <a
                href={authorization.activation_url}
                target="_blank"
                rel="noreferrer"
              />
            }
            variant="outline"
            size="sm"
          >
            打开批准页面
            <ExternalLinkIcon className="h-3.5 w-3.5" />
          </Button>
        </div>
      </div>
      <div className="flex items-center gap-2 text-xs text-muted-foreground">
        <Loader2 className="size-3.5 animate-spin" />
        <span>等待在浏览器批准…</span>
        <span
          data-testid="device-auth-countdown"
          className="ml-auto tabular-nums"
        >
          {countdown}
        </span>
      </div>
      <div className="flex justify-end">
        <Button type="button" variant="outline" onClick={onCancel}>
          取消
        </Button>
      </div>
    </div>
  );
}
