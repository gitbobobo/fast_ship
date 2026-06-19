import {
  Rocket,
  AlertTriangle,
  Copy,
} from "lucide-react";
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
import { cn, copyToClipboard } from "@/lib/utils";
import { toast } from "sonner";
import { ShipStepIcon, shipSteps, getShipStepState } from "./version-shared";

interface ShipCheckItem {
  key: string;
  label: string;
  ok: boolean;
  detail?: string;
}

interface VersionShipSectionProps {
  version: Version;
  isPending: boolean;
  isShipping: boolean;
  shipChecks: ShipCheckItem[];
  canShip: boolean;
  shipCheckLoading: boolean;
  shipDialogOpen: boolean;
  setShipDialogOpen: (open: boolean) => void;
  shipFailureDialogOpen: boolean;
  setShipFailureDialogOpen: (open: boolean) => void;
  shipFailureMessage: string;
  onShip: () => void;
}

export function VersionShipSection({
  version,
  isPending,
  isShipping,
  shipChecks,
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
      {/* 发货进度 */}
      {isPending &&
        (isShipping ||
          version.ship_status === "failed" ||
          version.ship_status === "completed") && (
          <Card className="shadow-md border-2 border-primary/20">
            <CardHeader className="pb-4 border-b">
              <div className="flex items-center gap-2">
                <div className="flex h-8 w-8 items-center justify-center rounded-lg bg-primary/10">
                  <Rocket className="h-4 w-4 text-primary" />
                </div>
                <CardTitle className="text-base">发货进度</CardTitle>
              </div>
            </CardHeader>
            <CardContent className="pt-4">
              <div className="relative space-y-0">
                {shipSteps.map((step, index) => {
                  const state = getShipStepState(version, index);
                  const isLast = index === shipSteps.length - 1;
                  return (
                    <div key={step.key} className="flex gap-3">
                      <div className="flex flex-col items-center">
                        <ShipStepIcon state={state} />
                        {!isLast && (
                          <div
                            className={cn(
                              "my-1 h-full w-px min-h-[20px]",
                              state === "done"
                                ? "bg-emerald-600/40"
                                : "bg-border",
                            )}
                          />
                        )}
                      </div>
                      <div className="pb-5">
                        <p
                          className={cn(
                            "text-sm font-medium",
                            state === "failed" && "text-destructive",
                            state === "doing" && "text-amber-600",
                            state === "todo" && "text-muted-foreground",
                          )}
                        >
                          {step.label}
                        </p>
                        <p className="text-xs text-muted-foreground mt-0.5">
                          {state === "done"
                            ? "已完成"
                            : state === "doing"
                              ? "进行中"
                              : state === "failed"
                                ? "失败"
                                : "等待中"}
                        </p>
                      </div>
                    </div>
                  );
                })}
              </div>
              {version.ship_message && (
                <div className="mt-2 rounded-md bg-muted/60 px-3 py-2 text-xs text-muted-foreground">
                  {version.ship_message}
                </div>
              )}
            </CardContent>
          </Card>
        )}

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
