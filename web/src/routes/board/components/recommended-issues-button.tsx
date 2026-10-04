import { useState } from "react";
import { Link } from "react-router";
import { Sparkles, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
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
import { Skeleton } from "@/components/ui/skeleton";
import {
  useRecommendations,
  useRemoveRecommendation,
} from "@/lib/hooks/use-recommendations";
import { ISSUE_WORKFLOW_STATUS_LABELS } from "@/lib/issue-workflow-status";
import { formatRelativeTime } from "@/lib/utils/format";
import { cn } from "@/lib/utils";

const PRIORITY_LABELS = {
  high: "高",
  medium: "中",
  low: "低",
} as const;

export function RecommendedIssuesButton({
  projectId,
}: {
  projectId?: string;
}) {
  const [open, setOpen] = useState(false);
  const [pendingRemove, setPendingRemove] =
    useState<IssueRecommendation | null>(null);
  const { data, isLoading } = useRecommendations(projectId);
  const removeRecommendation = useRemoveRecommendation();
  const items = data?.items ?? [];

  if (items.length === 0) {
    return null;
  }

  const handleConfirmRemove = async () => {
    if (!pendingRemove) return;
    try {
      await removeRecommendation.mutateAsync(pendingRemove.issue.id);
      toast.success("已移除推荐");
    } catch {
      toast.error("移除推荐失败");
    } finally {
      setPendingRemove(null);
    }
  };

  return (
    <>
      <Button
        variant="outline"
        size="sm"
        className="ml-auto h-7 text-xs"
        onClick={() => setOpen(true)}
      >
        <Sparkles className="mr-1 h-3 w-3" />
        推荐
        <span className="ml-1 rounded-full bg-muted px-1.5 text-xs font-medium text-muted-foreground">
          {items.length}
        </span>
      </Button>

      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="sm:max-w-3xl">
          <DialogHeader>
            <DialogTitle>推荐任务</DialogTitle>
          </DialogHeader>
          <div className="grid max-h-[70vh] grid-cols-1 gap-2 overflow-y-auto sm:grid-cols-2">
            {isLoading ? (
              Array.from({ length: 4 }).map((_, i) => (
                <Skeleton key={i} className="h-24 rounded-md" />
              ))
            ) : items.length === 0 ? (
              <p className="py-10 text-center text-sm text-muted-foreground">
                暂无推荐任务
              </p>
            ) : (
              items.map((item) => (
                <RecommendationCard
                  key={item.issue.id}
                  item={item}
                  onClose={() => setOpen(false)}
                  onRemove={() => setPendingRemove(item)}
                />
              ))
            )}
          </div>
        </DialogContent>
      </Dialog>

      <AlertDialog
        open={pendingRemove !== null}
        onOpenChange={(next) => {
          if (!next) setPendingRemove(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>确认移除推荐？</AlertDialogTitle>
            <AlertDialogDescription>
              将移除「{pendingRemove?.issue.title}
              」的推荐标记，此操作不可撤销。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>取消</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => void handleConfirmRemove()}
              disabled={removeRecommendation.isPending}
            >
              {removeRecommendation.isPending ? "移除中..." : "确认移除"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

function RecommendationCard({
  item,
  onClose,
  onRemove,
}: {
  item: IssueRecommendation;
  onClose: () => void;
  onRemove: () => void;
}) {
  const { issue } = item;
  return (
    <div className="rounded-md border bg-card p-2.5 shadow-xs">
      <div className="flex items-center gap-1.5">
        <Link
          to={`/projects/${issue.project_id}/issues/${issue.id}`}
          onClick={onClose}
          className="min-w-0 truncate text-sm font-medium hover:underline"
        >
          {issue.title}
        </Link>
        {issue.project_name && (
          <Badge variant="secondary" className="shrink-0">
            {issue.project_name}
          </Badge>
        )}
        <PriorityBadge priority={item.priority} />
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label="移除推荐"
          onClick={onRemove}
          className="ml-auto -my-1 shrink-0"
        >
          <Trash2 className="h-3.5 w-3.5" />
        </Button>
      </div>

      <p className="mt-1.5 whitespace-pre-wrap text-sm leading-snug">
        {item.reason}
      </p>

      {item.dependencies.length > 0 && (
        <div className="mt-1.5 flex flex-wrap items-center gap-1">
          <span className="text-xs text-muted-foreground">前置</span>
          {item.dependencies.map((dep) => (
            <DependencyChip key={dep.issue_id} dep={dep} />
          ))}
        </div>
      )}

      <div className="mt-1.5 text-[11px] text-muted-foreground">
        {formatRelativeTime(item.updated_at)}
        {item.created_by && <> · {item.created_by}</>}
      </div>
    </div>
  );
}

function DependencyChip({ dep }: { dep: RecommendationDependency }) {
  const closed = dep.state === "closed";
  const done = closed || dep.workflow_status === "done";
  const label = closed
    ? "已关闭"
    : ISSUE_WORKFLOW_STATUS_LABELS[
        dep.workflow_status as keyof typeof ISSUE_WORKFLOW_STATUS_LABELS
      ];

  const statusClass = done
    ? "text-muted-foreground"
    : dep.workflow_status === "in_progress"
      ? "text-amber-600 dark:text-amber-400"
      : "text-slate-600 dark:text-slate-400";

  return (
    <span className="inline-flex max-w-full items-center gap-1 rounded-full border bg-muted/40 px-1.5 py-0.5 text-xs">
      <span
        className={cn(
          "min-w-0 truncate",
          done && "text-muted-foreground line-through",
        )}
      >
        {dep.title}
      </span>
      {label && (
        <span className={cn("shrink-0", statusClass)}>{label}</span>
      )}
    </span>
  );
}

function PriorityBadge({
  priority,
}: {
  priority: IssueRecommendation["priority"];
}) {
  if (priority === "high") {
    return <Badge variant="destructive">{PRIORITY_LABELS.high}</Badge>;
  }
  if (priority === "medium") {
    return (
      <Badge
        variant="outline"
        className="border-amber-500/20 bg-amber-500/10 text-amber-600 dark:text-amber-400"
      >
        {PRIORITY_LABELS.medium}
      </Badge>
    );
  }
  return (
    <Badge className="bg-muted text-muted-foreground">
      {PRIORITY_LABELS.low}
    </Badge>
  );
}
