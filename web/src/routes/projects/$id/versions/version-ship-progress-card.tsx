import { Rocket } from "lucide-react";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { cn } from "@/lib/utils";
import { ShipStepIcon, shipSteps, getShipStepState } from "./version-shared";

interface VersionShipProgressCardProps {
  version: Version;
  isPending: boolean;
  isShipping: boolean;
}

export function VersionShipProgressCard({
  version,
  isPending,
  isShipping,
}: VersionShipProgressCardProps) {
  const shouldShow =
    isPending &&
    (isShipping ||
      version.ship_status === "failed" ||
      version.ship_status === "completed");

  if (!shouldShow) return null;

  return (
    <Card
      className="shadow-md border-2 border-primary/20"
      data-testid="ship-progress-card"
    >
      <CardHeader className="items-center gap-3 space-y-0 border-b px-5 pb-4 pt-5">
        <div className="flex items-center gap-2.5">
          <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-primary/10">
            <Rocket className="h-4 w-4 text-primary" />
          </div>
          <CardTitle className="text-base">发货进度</CardTitle>
        </div>
      </CardHeader>
      <CardContent className="space-y-4 px-5 pb-6 pt-5">
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
          <div className="max-h-48 overflow-auto rounded-md bg-muted/60 px-3 py-2 text-xs text-muted-foreground">
            {version.ship_message}
          </div>
        )}
      </CardContent>
    </Card>
  );
}
