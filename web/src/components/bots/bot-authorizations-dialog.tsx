import { KeyRound, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Skeleton } from "@/components/ui/skeleton";
import { useBotAuthorizations, useRevokeAuthorization } from "@/lib/hooks/use-bots";
import { ApiRequestError } from "@/lib/api/client";
import { formatDate } from "@/lib/utils/format";

/** 授权范围的可读标签；未知 scope_kind 原样展示，不猜语义。 */
function scopeLabel(a: {
  scope_kind: string;
  scope_id: string;
  environment_id?: string | null;
}) {
  const parts = [`${a.scope_kind}：${a.scope_id}`];
  if (a.environment_id) parts.push(`环境 ${a.environment_id}`);
  return parts.join(" · ");
}

/** constraints_json 的字段约束展示：键为字段名；空对象不显示该行。 */
function constraintKeys(constraints?: Record<string, unknown>) {
  const keys = Object.keys(constraints ?? {});
  return keys.length > 0 ? keys.join("、") : null;
}

/**
 * 授权管理弹窗：展示每条「始终允许」的 action、范围与字段约束；
 * 撤销写 revoked_at 保留审计关联，对已开始的操作不回滚。
 */
export function BotAuthorizationsDialog({
  botId,
  open,
  onClose,
}: {
  botId: string;
  open: boolean;
  onClose: () => void;
}) {
  const { data: authorizations, isLoading } = useBotAuthorizations(botId);
  const revoke = useRevokeAuthorization(botId);

  const handleRevoke = async (id: string) => {
    try {
      await revoke.mutateAsync(id);
      toast.success("授权已撤销");
    } catch (err) {
      toast.error(
        err instanceof ApiRequestError && err.message ? err.message : "撤销失败",
      );
    }
  };

  const active = (authorizations ?? []).filter((a) => !a.revoked_at);

  return (
    <Dialog open={open} onOpenChange={(next) => !next && onClose()}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>授权管理</DialogTitle>
          <DialogDescription>
            「始终允许」建立的精确授权。撤销后未开始的调用回到待确认，
            已开始的操作不回滚。
          </DialogDescription>
        </DialogHeader>
        {isLoading ? (
          <div className="space-y-2 py-2">
            <Skeleton className="h-12 rounded-lg" />
            <Skeleton className="h-12 rounded-lg" />
          </div>
        ) : active.length === 0 ? (
          <p className="flex items-center justify-center gap-2 py-8 text-center text-sm text-muted-foreground">
            <KeyRound className="h-4 w-4" />
            暂无有效授权
          </p>
        ) : (
          <ul className="space-y-2">
            {active.map((a) => {
              const fields = constraintKeys(a.constraints);
              return (
                <li
                  key={a.id}
                  data-testid="bot-authorization-item"
                  className="space-y-1 rounded-lg border px-3 py-2.5 text-sm"
                >
                  <div className="flex items-center gap-2">
                    <span className="font-medium">
                      {a.tool_name}.{a.action}
                    </span>
                    <Button
                      type="button"
                      size="xs"
                      variant="ghost"
                      className="ml-auto text-destructive"
                      disabled={revoke.isPending}
                      aria-label={`撤销 ${a.tool_name}.${a.action} 授权`}
                      onClick={() => void handleRevoke(a.id)}
                    >
                      <Trash2 className="h-3.5 w-3.5" />
                      撤销
                    </Button>
                  </div>
                  <p className="text-xs text-muted-foreground">
                    {scopeLabel(a)}
                  </p>
                  {fields && (
                    <p className="text-xs text-muted-foreground">
                      允许写入字段：{fields}
                    </p>
                  )}
                  <p className="text-xs text-muted-foreground">
                    创建于 {formatDate(a.created_at)}
                  </p>
                </li>
              );
            })}
          </ul>
        )}
      </DialogContent>
    </Dialog>
  );
}
