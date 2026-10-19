import { useMemo, useState } from "react";
import { RefreshCw } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  PreviewCard,
  PreviewCardContent,
  PreviewCardTrigger,
} from "@/components/ui/preview-card";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { QuotaProviderIcon } from "@/components/quota/provider-icons";
import { QuotaWindowProgress } from "./window-progress";
import { QuotaAccountMenu } from "./account-menu";
import { useNow } from "@/lib/hooks/use-now";
import { useRefreshQuotaAccount } from "@/lib/hooks/use-quota";
import {
  defaultQuotaPool,
  errorText,
  formatQuotaAmount,
  formatResetCountdown,
  quotaPools,
  quotaWindowUsageText,
  type QuotaPool,
} from "@/lib/quota";
import { formatRelativeTime } from "@/lib/utils/format";
import { cn } from "@/lib/utils";

/**
 * 悬浮面板里的窗口明细行：名称（受限窗口标出）| 已用% | used/limit（有则显示）
 * | 重置倒计时。只在面板打开时挂载，倒计时按 30s 自走。
 */
function QuotaWindowDetail({
  window: w,
  limiting,
}: {
  window: QuotaWindow;
  limiting: boolean;
}) {
  const now = useNow(30_000, Boolean(w.reset_at));
  const usage = quotaWindowUsageText(w);
  return (
    <div
      className={cn("flex items-baseline gap-2", limiting && "font-medium")}
    >
      <span className="min-w-0 flex-1 truncate">
        {w.label}
        {limiting && (
          <span className="ml-1 font-normal text-muted-foreground">受限</span>
        )}
      </span>
      <span className="shrink-0 tabular-nums">
        {w.used_percent == null
          ? "—"
          : formatQuotaAmount(w.used_percent, "percent")}
      </span>
      {usage && (
        <span className="shrink-0 tabular-nums text-muted-foreground">
          {usage}
        </span>
      )}
      <span className="shrink-0 text-muted-foreground">
        {w.reset_at ? formatResetCountdown(w.reset_at, now) : ""}
      </span>
    </div>
  );
}

/**
 * 卡片悬浮详情：账号全部窗口按池分组（多池时池名作小标题，带用尽/消耗中
 * 状态），受限窗口标「受限」；余额与备注收在最末。
 */
function QuotaAccountDetails({
  account,
  pools,
}: {
  account: QuotaAccount;
  pools: QuotaPool[];
}) {
  return (
    <div className="space-y-2.5 text-xs">
      {pools.map((pool) => (
        <div key={pool.name ?? ""} className="space-y-1">
          {pools.length > 1 && (
            <p className="flex items-center justify-between gap-2 font-medium">
              <span className="truncate">{pool.name ?? "默认"}</span>
              <span className="shrink-0 font-normal text-muted-foreground">
                {pool.exhausted ? "已用尽" : "消耗中"}
              </span>
            </p>
          )}
          {pool.windows.map((w, index) => (
            <QuotaWindowDetail
              key={`${w.kind}-${w.label}-${index}`}
              window={w}
              limiting={w === pool.limiting}
            />
          ))}
        </div>
      ))}
      {(account.balance || account.note) && (
        <div className="space-y-1 border-t border-foreground/10 pt-2">
          {account.balance && (
            <p>
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
              className="text-muted-foreground"
            >
              {account.note}
            </p>
          )}
        </div>
      )}
    </div>
  );
}

/**
 * 可用账号卡片：主视觉是「有效剩余」大号数字（= 100 − 受限窗口已用%），
 * 正文固定两行——剩余数字行（多池时右侧带池选择器）+ 受限窗口进度条，
 * 与窗口数/池数无关以保证卡片等高；全部窗口明细、余额、备注收进悬浮面板。
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
  // 第二行始终只有一根进度条：受限窗口；全窗口算不出百分比时退回首个窗口占位。
  const rowWindow = active?.limiting ?? active?.windows[0] ?? null;

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

  const pending = account.status === "pending";
  const hasWindows = account.windows.length > 0;
  // 没有窗口也可能有可看的东西：OpenDesign 无 Design Plan 额度时只给钱包余额
  // 和「按钱包计费」的说明，不能因为窗口为空就把余额丢掉。
  const hasBalanceOrNote = account.balance != null || account.note !== "";
  const showDetail = !pending && (hasWindows || hasBalanceOrNote);

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
      <CardContent className="p-4 pt-3">
        {!showDetail ? (
          <p className="text-xs text-muted-foreground">
            还没有额度数据
            {pending && "，等待后台抓取"}
          </p>
        ) : (
          <PreviewCard>
            {/* 触发区 = 正文两行（剩余数字行 + 受限窗口进度条），鼠标悬浮或键盘聚焦
                弹出全部明细。池选择器放在第一行右侧而不是另占一行，否则双池卡片
                会比单池卡片高一行，同行卡片底边就对不齐。 */}
            <PreviewCardTrigger
              delay={150}
              render={
                <div
                  tabIndex={0}
                  aria-label={`查看 ${account.label} 的额度详情`}
                  className="space-y-1.5 rounded-md outline-none focus-visible:outline-2 focus-visible:outline-ring"
                />
              }
            >
              {/* 固定行高：双池卡片的池选择器不把这一行撑高，同行卡片底边才能齐平 */}
              <div className="flex h-9 items-center justify-between gap-2">
                <div className="flex min-w-0 items-baseline gap-2">
                  <span
                    data-testid="quota-remaining"
                    className={cn(
                      "text-3xl leading-none font-semibold tabular-nums",
                      active?.remaining === 0 && "text-muted-foreground",
                    )}
                  >
                    {active?.remaining != null
                      ? formatQuotaAmount(active.remaining, "percent")
                      : account.balance
                        ? `$${account.balance.amount}`
                        : "—"}
                  </span>
                  <span className="min-w-0 truncate text-xs text-muted-foreground">
                    {active?.remaining != null ? (
                      <>
                        剩余
                        {active.limiting &&
                          ` · 受「${active.limiting.label}」限制`}
                      </>
                    ) : account.balance ? (
                      account.balance.label
                    ) : (
                      "还没有额度数据"
                    )}
                  </span>
                </div>
                {pools.length > 1 && (
                  <Tabs
                    value={active?.name ?? ""}
                    onValueChange={(value) => setPickedName(value)}
                    className="shrink-0"
                  >
                    <TabsList className="h-7">
                      {pools.map((pool) => {
                        const name = pool.name ?? "默认";
                        return (
                          <TabsTrigger
                            key={name}
                            value={pool.name ?? ""}
                            title={pool.exhausted ? `${name}（已用尽）` : name}
                            aria-label={
                              pool.exhausted ? `${name}（已用尽）` : name
                            }
                            className={cn(
                              "px-1.5 text-xs",
                              pool.exhausted && "text-muted-foreground",
                            )}
                          >
                            {name}
                          </TabsTrigger>
                        );
                      })}
                    </TabsList>
                  </Tabs>
                )}
              </div>
              {rowWindow && (
                <QuotaWindowProgress
                  window={rowWindow}
                  limited={rowWindow === active?.limiting}
                />
              )}
            </PreviewCardTrigger>
            <PreviewCardContent data-testid="quota-account-detail-panel">
              <QuotaAccountDetails account={account} pools={pools} />
            </PreviewCardContent>
          </PreviewCard>
        )}
      </CardContent>
    </Card>
  );
}
