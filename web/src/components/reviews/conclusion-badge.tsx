import { Badge } from "@/components/ui/badge";
import { reviewConclusionLabel } from "@/lib/api/reviews";

// 最近一次审查结论徽标（INT-135）：success=通过、failure=发现问题、
// error=未完成、pending=审查中；未配置（null）不渲染。
export function ConclusionBadge({
  label,
  status,
}: {
  label: string;
  status: string | null | undefined;
}) {
  if (!status) return null;
  const variant =
    status === "failure" || status === "error"
      ? "destructive"
      : status === "pending"
        ? "outline"
        : "secondary";
  return (
    <Badge variant={variant}>
      {label} {reviewConclusionLabel(status)}
    </Badge>
  );
}
