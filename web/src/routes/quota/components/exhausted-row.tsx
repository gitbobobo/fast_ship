import { QuotaProviderIcon } from "@/components/quota/provider-icons";
import { useNow } from "@/lib/hooks/use-now";
import {
  accountRecoveryAt,
  formatRecoveryCountdown,
} from "@/lib/quota";
import { QuotaAccountMenu } from "./account-menu";

/**
 * 已用尽账号收成一行：图标 + 名称 + 恢复倒计时（等受限窗口重置），
 * 编辑 / 测试凭据 / 刷新 / 删除在 ··· 菜单里。整段按恢复时间升序排在最后。
 */
export function QuotaExhaustedRow({
  account,
  onEdit,
  onTest,
}: {
  account: QuotaAccount;
  onEdit: (account: QuotaAccount) => void;
  onTest: (account: QuotaAccount) => void;
}) {
  const recoveryAt = accountRecoveryAt(account);
  const now = useNow(30_000, Boolean(recoveryAt));

  return (
    <div
      data-testid="quota-exhausted-row"
      className="flex items-center gap-2.5 rounded-lg border bg-muted/30 px-3 py-2"
    >
      <QuotaProviderIcon
        provider={account.provider}
        className="size-4 shrink-0 text-muted-foreground"
      />
      <span className="min-w-0 truncate text-sm text-muted-foreground">
        {account.provider_name}
        {account.plan_name && ` · ${account.plan_name}`}
        {" · "}
        {account.label}
      </span>
      <span
        data-testid="quota-recovery"
        className="ml-auto shrink-0 text-xs text-muted-foreground tabular-nums"
      >
        {recoveryAt
          ? formatRecoveryCountdown(recoveryAt, now)
          : "恢复时间未知"}
      </span>
      <QuotaAccountMenu
        account={account}
        onEdit={onEdit}
        onTest={onTest}
        withRefresh
      />
    </div>
  );
}
