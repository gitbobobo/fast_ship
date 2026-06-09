import { FileArchive } from "lucide-react";
import { CheckCircle2, Circle, Loader2, XCircle } from "lucide-react";

export function PlatformIcon({ platform }: { platform: string | null }) {
  if (!platform) return <FileArchive className="h-5 w-5 text-muted-foreground" />;
  const p = platform.toLowerCase();
  if (p.includes("android")) return <span className="text-lg">🤖</span>;
  if (p.includes("ios")) return <span className="text-lg">🍎</span>;
  if (p.includes("mac")) return <span className="text-lg">🖥️</span>;
  if (p.includes("win")) return <span className="text-lg">🪟</span>;
  if (p.includes("linux")) return <span className="text-lg">🐧</span>;
  return <FileArchive className="h-5 w-5 text-muted-foreground" />;
}

export function ShipStepIcon({ state }: { state: "done" | "doing" | "failed" | "todo" }) {
  if (state === "done") return <CheckCircle2 className="h-5 w-5 text-emerald-600" />;
  if (state === "doing") return <Loader2 className="h-5 w-5 animate-spin text-amber-600" />;
  if (state === "failed") return <XCircle className="h-5 w-5 text-destructive" />;
  return <Circle className="h-5 w-5 text-muted-foreground" />;
}

export const shipSteps = [
  { key: "precheck", label: "发货前校验" },
  { key: "create_tag", label: "创建 Git Tag" },
  { key: "create_release", label: "创建 GitHub Release" },
  { key: "upload_assets", label: "上传安装包" },
  { key: "finalize", label: "更新版本状态" },
] as const;

export function getShipStepState(
  version: { status: string; ship_status: string; ship_stage: string },
  index: number,
) {
  const currentStepIndex = shipSteps.findIndex((step) => step.key === version.ship_stage);

  if (version.status === "shipped" || version.ship_status === "completed") {
    return "done";
  }
  if (version.ship_status === "failed") {
    if (index < currentStepIndex) return "done";
    if (index === currentStepIndex) return "failed";
    return "todo";
  }
  if (version.ship_status === "in_progress") {
    if (index < currentStepIndex) return "done";
    if (index === currentStepIndex) return "doing";
    return "todo";
  }
  return "todo";
}
