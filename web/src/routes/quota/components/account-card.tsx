import { Pencil, RefreshCw, Trash2, Zap } from "lucide-react";
import { toast } from "sonner";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { QuotaProviderIcon } from "@/components/quota/provider-icons";
import { QuotaWindowProgress } from "./window-progress";
import {
  useDeleteQuotaAccount,
  useRefreshQuotaAccount,
} from "@/lib/hooks/use-quota";
import { errorText } from "@/lib/quota";
import { formatRelativeTime } from "@/lib/utils/format";
import { cn } from "@/lib/utils";

// 状态徽标与 GitHub Token 检测状态同套配色约定。
const STATUS_META: Record<
  QuotaAccountStatus,
  { label: string; variant: "default" | "secondary" | "destructive" | "outline" }
> = {
  ok: { label: "正常", variant: "default" },
  credential_invalid: { label: "凭据失效", variant: "destructive" },
  provider_error: { label: "接口异常", variant: "secondary" },
  pending: { label: "暂未抓取", variant: "outline" },
};

/**
 * 单个额度账号卡片：窗口进度条 + 余额 + 最近错误 + 数据时间；
 * 测试（重贴凭据探测）/ 刷新（立即抓取）/ 编辑 / 删除。
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
  const deleteAccount = useDeleteQuotaAccount();
  const statusMeta = STATUS_META[account.status] ?? STATUS_META.pending;

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

  const handleDelete = async () => {
    try {
      await deleteAccount.mutateAsync(account.id);
      toast.success("账号已删除");
    } catch (err) {
      toast.error(errorText(err, "删除失败，请稍后重试"));
    }
  };

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
            </div>
          </div>
          <Badge variant={statusMeta.variant}>{statusMeta.label}</Badge>
        </div>
      </CardHeader>
      <CardContent className="space-y-3 p-4 pt-3">
        {account.windows.length === 0 && !account.balance ? (
          <p className="text-xs text-muted-foreground">
            还没有额度数据
            {account.status === "pending" && "，等待后台抓取"}
          </p>
        ) : (
          <div className="space-y-2.5">
            {account.windows.map((w, index) => (
              <QuotaWindowProgress
                key={`${w.kind}-${w.label}-${index}`}
                window={w}
              />
            ))}
          </div>
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

        {account.last_error && (
          <p className="text-xs text-destructive">{account.last_error}</p>
        )}

        <div className="flex items-center justify-between gap-2 pt-1">
          <span className="text-xs text-muted-foreground">
            {account.fetched_at
              ? `数据时间 ${formatRelativeTime(account.fetched_at)}`
              : "尚未抓取"}
          </span>
          <div className="flex shrink-0 gap-0.5">
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={`测试 ${account.label}`}
              title="重新粘贴凭据测试连通性"
              onClick={() => onTest(account)}
            >
              <Zap className="h-3.5 w-3.5" />
            </Button>
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
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={`编辑 ${account.label}`}
              onClick={() => onEdit(account)}
            >
              <Pencil className="h-3.5 w-3.5" />
            </Button>
            <AlertDialog>
              <AlertDialogTrigger
                render={
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label={`删除 ${account.label}`}
                  />
                }
              >
                <Trash2 className="h-3.5 w-3.5" />
              </AlertDialogTrigger>
              <AlertDialogContent>
                <AlertDialogHeader>
                  <AlertDialogTitle>删除额度账号？</AlertDialogTitle>
                  <AlertDialogDescription>
                    将删除「{account.label}」及其已存凭据与额度快照，无法恢复。
                  </AlertDialogDescription>
                </AlertDialogHeader>
                <AlertDialogFooter>
                  <AlertDialogCancel>取消</AlertDialogCancel>
                  <AlertDialogAction
                    onClick={() => void handleDelete()}
                    disabled={deleteAccount.isPending}
                  >
                    确认删除
                  </AlertDialogAction>
                </AlertDialogFooter>
              </AlertDialogContent>
            </AlertDialog>
          </div>
        </div>
      </CardContent>
    </Card>
  );
}
