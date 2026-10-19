import { useMemo, useState } from "react";
import { RefreshCw } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { QuotaProviderIcon } from "@/components/quota/provider-icons";
import { QuotaWindowProgress } from "./window-progress";
import { QuotaAccountMenu } from "./account-menu";
import { useRefreshQuotaAccount } from "@/lib/hooks/use-quota";
import {
  defaultQuotaPool,
  errorText,
  formatQuotaAmount,
  quotaPools,
} from "@/lib/quota";
import { formatRelativeTime } from "@/lib/utils/format";
import { cn } from "@/lib/utils";

/**
 * 可用账号卡片：主视觉是「有效剩余」大号数字（= 100 − 受限窗口已用%），
 * 窗口明细压成单行；多池账号先经池 tab 选定池。
 * 操作只留「刷新」主按钮，编辑 / 测试凭据 / 删除收进 ··· 菜单。
 */
export function QuotaAccountCard({
  account,
  onTest,
  onEdit,
}: {
  account: QuotaAccount;
  onTest: (account: QuotaAccount) => void;
  onEdit: (account: QuotaAccount) => void;
}) {
  const refresh = useRefreshQuotaAccount();
  const pools = useMemo(() => quotaPools(account.windows), [account.windows]);
  // 用户切过的池（按池名，匿名池为 ""）；null = 自动（第一个未用尽 = 正在消耗的池）。
  // 不用下标：窗口增减会改变池顺序，下标仍合法时会静默切到另一个池。
  const [pickedName, setPickedName] = useState<string | null>(null);
  const active =
    (pickedName != null
      ? pools.find((pool) => (pool.name ?? "") === pickedName)
      : undefined) ?? defaultQuotaPool(pools);

  const handleRefresh = async () => {
    try {
      const res = await refresh.mutateAsync(account.id);
      const next = res.data;
      if (next.status === "ok") {
        toast.success("已刷新");
      } else {
        toast.error(next.last_error || "刷新失败");
      }
    } catch (err) {
      toast.error(errorText(err, "刷新失败，请稍后重试"));
    }
  };

  const hasQuotaData = account.windows.length > 0 || account.balance != null;

  return (
    <Card>
      <CardHeader className="pb-0 pt-4">
        <div className="flex items-start justify-between gap-2">
          <div className="flex min-w-0 items-center gap-2.5">
            <QuotaProviderIcon
              provider={account.provider}
              className="size-6 text-foreground"
            />
            <div className="min-w-0">
              <CardTitle className="truncate text-sm">
                {account.provider_name}
                {account.plan_name && (
                  <span className="font-normal text-muted-foreground">
                    {" "}
                    · {account.plan_name}
                  </span>
                )}
              </CardTitle>
              <p className="truncate text-xs text-muted-foreground">
                {account.label}
                {account.credential_hint && ` · ${account.credential_hint}`}
              </p>
              <p className="truncate text-xs text-muted-foreground">
                {account.fetched_at
                  ? `数据时间 ${formatRelativeTime(account.fetched_at)}`
                  : "尚未抓取"}
              </p>
            </div>
          </div>
          <div className="flex shrink-0 gap-0.5">
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={`刷新 ${account.label}`}
              title="立即抓取一次"
              disabled={refresh.isPending}
              onClick={() => void handleRefresh()}
            >
              <RefreshCw
                className={cn("h-3.5 w-3.5", refresh.isPending && "animate-spin")}
              />
            </Button>
            <QuotaAccountMenu
              account={account}
              onEdit={onEdit}
              onTest={onTest}
            />
          </div>
        </div>
      </CardHeader>
      <CardContent className="space-y-3 p-4 pt-3">
        {!hasQuotaData ? (
          <p className="text-xs text-muted-foreground">
            还没有额度数据
            {account.status === "pending" && "，等待后台抓取"}
          </p>
        ) : (
          <>
            {pools.length > 1 && (
              <Tabs
                value={active?.name ?? ""}
                onValueChange={(value) => setPickedName(value)}
              >
                <TabsList>
                  {pools.map((pool) => (
                    <TabsTrigger
                      key={pool.name ?? ""}
                      value={pool.name ?? ""}
                      className="px-2"
                    >
                      {pool.name ?? "默认"}
                      <span
                        className={cn(
                          "text-[11px]",
                          pool.exhausted
                            ? "text-muted-foreground"
                            : "text-emerald-600 dark:text-emerald-400",
                        )}
                      >
                        {pool.exhausted ? "用尽" : "消耗中"}
                      </span>
                    </TabsTrigger>
                  ))}
                </TabsList>
              </Tabs>
            )}

            {active?.remaining != null && (
              <div className="flex items-baseline gap-2">
                <span
                  data-testid="quota-remaining"
                  className={cn(
                    "text-3xl leading-none font-semibold tabular-nums",
                    active.remaining === 0 && "text-muted-foreground",
                  )}
                >
                  {formatQuotaAmount(active.remaining, "percent")}
                </span>
                <span className="text-xs text-muted-foreground">
                  剩余
                  {active.limiting && ` · 受「${active.limiting.label}」限制`}
                </span>
              </div>
            )}

            {active && active.windows.length > 0 && (
              <div className="space-y-1.5">
                {active.windows.map((w, index) => (
                  <QuotaWindowProgress
                    key={`${w.kind}-${w.label}-${index}`}
                    window={w}
                    limited={w === active.limiting}
                  />
                ))}
              </div>
            )}
          </>
        )}

        {account.balance && (
          <p className="text-xs">
            <span className="text-muted-foreground">
              {account.balance.label}：
            </span>
            <span className="font-medium tabular-nums">
              {account.balance.amount} {account.balance.currency}
            </span>
          </p>
        )}

        {account.note && (
          <p
            data-testid="quota-account-note"
            className="text-xs text-muted-foreground"
          >
            {account.note}
          </p>
        )}
      </CardContent>
    </Card>
  );
}
