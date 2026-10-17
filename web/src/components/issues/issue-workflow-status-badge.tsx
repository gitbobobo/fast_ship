import {
  ISSUE_WORKFLOW_STATUS_LABELS,
  type IssueWorkflowStatus,
} from "@/lib/issue-workflow-status";
import { cn } from "@/lib/utils";

type IssueWorkflowStatusBadgeProps = {
  status?: string | null;
  /** xs: 看板 pill（px-2 py-0.5 text-[11px] font-medium）
   *  sm: 问题列表徽章（px-2 py-0.5 text-xs font-medium）
   *  md: 详情页头部徽章（px-2.5 py-1 text-xs font-semibold） */
  size?: "xs" | "sm" | "md";
};

const SIZE_CLASSES: Record<NonNullable<IssueWorkflowStatusBadgeProps["size"]>, string> = {
  xs: "px-2 py-0.5 text-[11px] font-medium",
  sm: "px-2 py-0.5 text-xs font-medium",
  md: "px-2.5 py-1 text-xs font-semibold",
};

/** 问题内部状态徽章：todo/in_progress/done 三色，空值或未知状态不渲染。 */
export function IssueWorkflowStatusBadge({
  status,
  size = "sm",
}: IssueWorkflowStatusBadgeProps) {
  if (!status) {
    return null;
  }

  const label = ISSUE_WORKFLOW_STATUS_LABELS[status as IssueWorkflowStatus];
  if (!label) {
    return null;
  }

  const className =
    status === "todo"
      ? "border-slate-500/20 bg-slate-500/10 text-slate-600 dark:text-slate-400"
      : status === "in_progress"
        ? "border-amber-500/20 bg-amber-500/10 text-amber-600 dark:text-amber-400"
        : "border-emerald-500/20 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400";

  return (
    <span
      className={cn(
        "inline-flex items-center rounded-full border",
        SIZE_CLASSES[size],
        className,
      )}
    >
      {label}
    </span>
  );
}
