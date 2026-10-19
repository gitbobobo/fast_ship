import { useState } from "react";
import { MoreHorizontal, Pencil, RefreshCw, Trash2, Zap } from "lucide-react";
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
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  useDeleteQuotaAccount,
  useRefreshQuotaAccount,
} from "@/lib/hooks/use-quota";
import { errorText } from "@/lib/quota";

/**
 * 账号卡片的 ··· 菜单：编辑 / 测试凭据 / 删除（带二次确认）；
 * 已用尽的单行账号没有主按钮，菜单里额外给「刷新」（withRefresh）。
 * onEdit / onTest 语义与卡片原图标按钮一致（编辑对话框 / 测试凭据对话框）。
 */
export function QuotaAccountMenu({
  account,
  onEdit,
  onTest,
  withRefresh = false,
}: {
  account: QuotaAccount;
  onEdit: (account: QuotaAccount) => void;
  onTest: (account: QuotaAccount) => void;
  /** 菜单项里带「刷新」——卡片有独立刷新按钮，只有单行账号用 */
  withRefresh?: boolean;
}) {
  const refresh = useRefreshQuotaAccount();
  const deleteAccount = useDeleteQuotaAccount();
  const [confirmOpen, setConfirmOpen] = useState(false);

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
    } finally {
      // AlertDialogAction 是纯 Button 不会自动关框，成功失败都显式关
      setConfirmOpen(false);
    }
  };

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger
          render={
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={`更多操作 ${account.label}`}
              title="更多操作"
            />
          }
        >
          <MoreHorizontal className="h-3.5 w-3.5" />
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          {withRefresh && (
            <DropdownMenuItem
              disabled={refresh.isPending}
              onClick={() => void handleRefresh()}
            >
              <RefreshCw />
              刷新
            </DropdownMenuItem>
          )}
          <DropdownMenuItem onClick={() => onEdit(account)}>
            <Pencil />
            编辑
          </DropdownMenuItem>
          <DropdownMenuItem onClick={() => onTest(account)}>
            <Zap />
            测试凭据
          </DropdownMenuItem>
          <DropdownMenuItem
            variant="destructive"
            onClick={() => setConfirmOpen(true)}
          >
            <Trash2 />
            删除
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>

      <AlertDialog open={confirmOpen} onOpenChange={setConfirmOpen}>
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
    </>
  );
}
