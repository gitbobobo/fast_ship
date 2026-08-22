import { Badge } from "@/components/ui/badge";
import { shipHookBadge } from "@/lib/issue-ship-hook";
import { cn } from "@/lib/utils";

export function IssueShipHookBadge({
  hook,
  className,
}: {
  hook?: IssueShipHook | null;
  className?: string;
}) {
  const badge = shipHookBadge(hook);

  if (!badge) {
    return null;
  }

  if (badge === "pending") {
    return (
      <Badge
        variant="outline"
        className={cn(
          "border-sky-500/30 bg-sky-500/10 text-sky-700 dark:text-sky-300",
          className,
        )}
      >
        发货后
      </Badge>
    );
  }

  return (
    <Badge variant="destructive" className={cn("bg-destructive/10", className)}>
      钩子失败
    </Badge>
  );
}
