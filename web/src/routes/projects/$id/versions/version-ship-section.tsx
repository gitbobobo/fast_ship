import { AlertTriangle, Copy, Rocket } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { copyToClipboard } from "@/lib/utils";
import { formatShipHookActionSummary } from "@/lib/issue-ship-hook";
import { toast } from "sonner";

interface ShipCheckItem {
  key: string;
  label: string;
  ok: boolean;
  detail?: string;
}

interface VersionShipSectionProps {
  version: Version;
  isShipping: boolean;
  shipChecks: ShipCheckItem[];
  pendingIssueHooks: PendingIssueHook[];
  canShip: boolean;
  shipCheckLoading: boolean;
  shipDialogOpen: boolean;
  setShipDialogOpen: (open: boolean) => void;
  shipFailureDialogOpen: boolean;
  setShipFailureDialogOpen: (open: boolean) => void;
  shipFailureMessage: string;
  onShip: () => void;
}

/**
 * 发货相关浮层与错误日志容器。
 *
 * 发货进度卡片已迁移到 VersionSidebar；此组件仅保留：
 * - 发货确认对话框
 * - 发货失败对话框
 * - 错误日志展示
 */
export function VersionShipSection({
  version,
  isShipping,
  shipChecks,
  pendingIssueHooks,
  canShip,
  shipCheckLoading,
  shipDialogOpen,
  setShipDialogOpen,
  shipFailureDialogOpen,
  setShipFailureDialogOpen,
  shipFailureMessage,
  onShip,
}: VersionShipSectionProps) {
  return (
    <>
      {/* 错误日志 */}
      {version.error_log && (
        <Card className="border-2 border-destructive/40 shadow-lg shadow-destructive/10">
          <CardHeader className="pb-4 border-b">
            <CardTitle className="flex items-center gap-2 text-base text-destructive">
              <div className="flex h-8 w-8 items-center justify-center rounded-lg bg-destructive/10">
                <AlertTriangle className="h-4 w-4" />
              </div>
              错误日志
            </CardTitle>
          </CardHeader>
          <CardContent className="pt-4">
            <div className="relative">
              <pre className="max-h-48 overflow-auto whitespace-pre-wrap rounded-md bg-destructive/5 p-3 text-xs text-destructive">
                {version.error_log}
              </pre>
              <Button
                variant="ghost"
                size="icon-xs"
                className="absolute top-1.5 right-1.5 bg-destructive/5 hover:bg-destructive/10"
                onClick={() => {
                  void copyToClipboard(version.error_log || "").catch(() => {});
                  toast.success("已复制到剪贴板");
                }}
              >
                <Copy className="h-3 w-3 text-destructive" />
              </Button>
            </div>
          </CardContent>
        </Card>
      )}

      {/* 发货确认对话框 */}
      <Dialog open={shipDialogOpen} onOpenChange={setShipDialogOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>确认发货</DialogTitle>
            <DialogDescription>
              将 {version.version_number} 发布到 GitHub Release
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-2 py-2">
            {shipCheckLoading ? (
              <p className="text-sm text-muted-foreground">正在校验发货条件...</p>
            ) : (
              shipChecks.map((check) => (
                <div
                  key={check.key}
                  className="flex items-start gap-2 rounded-md border px-3 py-2 text-sm"
                >
                  <span>{check.ok ? "✅" : "❌"}</span>
                  <div className="space-y-0.5">
                    <p className={check.ok ? "" : "text-destructive"}>
                      {check.label}
                    </p>
                    {!check.ok && check.detail && (
                      <p className="text-xs text-muted-foreground">
                        {check.detail}
                      </p>
                    )}
                  </div>
                </div>
              ))
            )}
          </div>
          {pendingIssueHooks.length > 0 && (
            <div className="space-y-2">
              <p className="text-sm font-medium">
                将触发 {pendingIssueHooks.length} 个问题钩子
              </p>
              <div className="space-y-2">
                {pendingIssueHooks.map((hook) => (
                  <div
                    key={hook.issue_id}
                    className="rounded-md border px-3 py-2 text-sm"
                  >
                    <p className="font-medium">
                      <span className="text-muted-foreground">
                        {hook.reference}
                      </span>
                      <span className="ml-2 line-clamp-1">{hook.title}</span>
                    </p>
                    <p className="mt-1 text-xs text-muted-foreground">
                      {formatShipHookActionSummary({
                        comment: hook.comment,
                        close: hook.close,
                        workflow_enabled: hook.workflow_enabled,
                        workflow_status: hook.workflow_status,
                      })}
                    </p>
                  </div>
                ))}
              </div>
            </div>
          )}
          {!canShip && (
            <p className="text-sm text-destructive">
              请补充上述缺失项后再发货
            </p>
          )}
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => setShipDialogOpen(false)}
              disabled={isShipping}
            >
              取消
            </Button>
            <Button
              onClick={onShip}
              disabled={!canShip || shipCheckLoading || isShipping}
            >
              <Rocket className="mr-2 h-4 w-4" />
              确认发货
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 发货失败对话框 */}
      <Dialog
        open={shipFailureDialogOpen}
        onOpenChange={setShipFailureDialogOpen}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>发货失败</DialogTitle>
            <DialogDescription>
              GitHub 发货流程未完成，版本状态保持为待发货。
            </DialogDescription>
          </DialogHeader>
          <div className="rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm text-destructive">
            {shipFailureMessage}
          </div>
          <p className="text-sm text-muted-foreground">
            请根据失败原因修复配置或重新上传安装包后，再次发货。
          </p>
          <DialogFooter>
            <Button onClick={() => setShipFailureDialogOpen(false)}>
              我知道了
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
