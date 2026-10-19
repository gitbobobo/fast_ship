import { Button } from "@/components/ui/button";
import { QuotaProviderIcon } from "@/components/quota/provider-icons";
import { QuotaAccountMenu } from "./account-menu";

const STATUS_LABEL: Partial<Record<QuotaAccountStatus, string>> = {
  credential_invalid: "凭据失效",
  provider_error: "接口异常",
};

/**
 * 「需处理」横幅：凭据失效或接口异常的账号收在页面顶部。
 * 每行保留「重新连接」主按钮（打开编辑对话框重贴凭据），
 * 刷新 / 编辑 / 测试凭据 / 删除收进 ··· 菜单；不展示过期的额度数据——
 * 这是页面上唯一用红色的区块。
 */
export function QuotaAttentionBanner({
  accounts,
  onEdit,
  onTest,
}: {
  accounts: QuotaAccount[];
  onEdit: (account: QuotaAccount) => void;
  onTest: (account: QuotaAccount) => void;
}) {
  if (accounts.length === 0) return null;

  return (
    <div
      data-testid="quota-attention"
      className="space-y-0.5 rounded-lg border border-destructive/30 bg-destructive/5 p-2"
    >
      {accounts.map((account) => (
        <div
          key={account.id}
          className="flex flex-wrap items-center gap-x-2.5 gap-y-1 px-1 py-1"
        >
          <QuotaProviderIcon
            provider={account.provider}
            className="size-4 shrink-0 text-destructive"
          />
          <span className="min-w-0 truncate text-sm font-medium">
            {account.provider_name}
            {" · "}
            {account.label}
          </span>
          <span className="shrink-0 text-xs text-destructive">
            {STATUS_LABEL[account.status] ?? "需处理"}
          </span>
          {account.last_error && (
            <span className="min-w-0 flex-1 truncate text-xs text-muted-foreground">
              {account.last_error}
            </span>
          )}
          <Button
            variant="outline"
            size="sm"
            className="ml-auto shrink-0"
            onClick={() => onEdit(account)}
          >
            重新连接
          </Button>
          <QuotaAccountMenu
            account={account}
            onEdit={onEdit}
            onTest={onTest}
            withRefresh
          />
        </div>
      ))}
    </div>
  );
}
