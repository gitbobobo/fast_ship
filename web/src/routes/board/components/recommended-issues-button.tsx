import { useState } from "react";
import { Link } from "react-router";
import { Sparkles, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
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
import { CopyIssuePromptButton } from "@/components/issues/copy-issue-prompt-button";
import {
  useRecommendations,
  useRemoveRecommendation,
} from "@/lib/hooks/use-recommendations";
import { ISSUE_WORKFLOW_STATUS_LABELS } from "@/lib/issue-workflow-status";
import { formatRelativeTime } from "@/lib/utils/format";
import { cn } from "@/lib/utils";

type Priority = IssueRecommendation["priority"];

const PRIORITY_GROUPS: { priority: Priority; label: string; dot: string }[] = [
  { priority: "high", label: "高优先级", dot: "bg-destructive" },
  { priority: "medium", label: "中优先级", dot: "bg-amber-500" },
  { priority: "low", label: "低优先级", dot: "bg-muted-foreground/50" },
];

/**
 * 看板筛选行的「推荐」按钮与推荐弹框。
 * 列表为空（含加载中、加载失败）时整体不渲染；弹框按优先级分组展示，每条可复制提示词或移除。
 * 传入 projectId 时列表只含该项目，不再逐条显示项目名。
 */
export function RecommendedIssuesButton({
  projectId,
}: {
  projectId?: string;
}) {
  const [open, setOpen] = useState(false);
  const [pendingRemove, setPendingRemove] =
    useState<IssueRecommendation | null>(null);
  const { data } = useRecommendations(projectId);
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
        <DialogContent className="gap-0 overflow-hidden p-0 sm:max-w-2xl">
          <DialogHeader className="gap-1.5 border-b px-5 pt-4 pb-3">
            <DialogTitle>
              推荐任务
              <span className="ml-2 text-sm font-normal text-muted-foreground tabular-nums">
                {items.length}
              </span>
            </DialogTitle>
            <DialogDescription className="text-xs">
              按优先级排列，开始开发或关闭后自动移出。
            </DialogDescription>
          </DialogHeader>
          <div className="max-h-[70vh] overflow-y-auto pb-2">
            {PRIORITY_GROUPS.map((group) => {
              const groupItems = items.filter(
                (item) => item.priority === group.priority,
              );
              if (groupItems.length === 0) return null;
              return (
                <section key={group.priority} aria-label={group.label}>
                  <h3 className="sticky top-0 z-10 flex items-center gap-2 bg-popover px-5 pt-3 pb-1.5 text-xs font-medium text-muted-foreground">
                    <span className={cn("h-1.5 w-1.5 rounded-full", group.dot)} />
                    {group.label}
                    <span className="tabular-nums">{groupItems.length}</span>
                  </h3>
                  <ul className="divide-y divide-border/60">
                    {groupItems.map((item) => (
                      <RecommendationRow
                        key={item.issue.id}
                        item={item}
                        showProject={!projectId}
                        onNavigate={() => setOpen(false)}
                        onRemove={() => setPendingRemove(item)}
                      />
                    ))}
                  </ul>
                </section>
              );
            })}
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

function RecommendationRow({
  item,
  showProject,
  onNavigate,
  onRemove,
}: {
  item: IssueRecommendation;
  showProject: boolean;
  onNavigate: () => void;
  onRemove: () => void;
}) {
  const { issue } = item;
  return (
    <li className="px-5 py-3">
      <Link
        to={`/projects/${issue.project_id}/issues/${issue.id}`}
        onClick={onNavigate}
        className="line-clamp-2 text-sm leading-snug font-medium hover:text-primary"
      >
        <span className="mr-1.5 font-mono text-xs font-normal text-muted-foreground">
          {issue.reference}
        </span>
        <span>{issue.title}</span>
      </Link>

      <p className="mt-1.5 text-[13px] leading-relaxed whitespace-pre-wrap text-foreground/80">
        {item.reason}
      </p>

      {item.dependencies.length > 0 && (
        <div className="mt-2 flex items-start gap-2">
          <span className="shrink-0 border border-transparent py-0.5 text-xs text-muted-foreground">
            前置
          </span>
          <div className="flex min-w-0 flex-wrap gap-1.5">
            {item.dependencies.map((dep) => (
              <DependencyChip
                key={dep.issue_id}
                dep={dep}
                onNavigate={onNavigate}
              />
            ))}
          </div>
        </div>
      )}

      <div className="mt-1.5 flex items-center gap-2">
        <p className="min-w-0 flex-1 truncate text-[11px] text-muted-foreground">
          {showProject && issue.project_name && <>{issue.project_name} · </>}
          {formatRelativeTime(item.updated_at)}
          {item.created_by && <> · {item.created_by}</>}
        </p>
        <div className="-mr-2 flex shrink-0 items-center">
          <CopyIssuePromptButton
            projectId={issue.project_id}
            issueId={issue.id}
            reason={item.reason}
          />
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label="移除推荐"
            title="移除推荐"
            onClick={onRemove}
            className="text-muted-foreground hover:text-destructive"
          >
            <Trash2 className="h-3.5 w-3.5" />
          </Button>
        </div>
      </div>
    </li>
  );
}

function DependencyChip({
  dep,
  onNavigate,
}: {
  dep: RecommendationDependency;
  onNavigate: () => void;
}) {
  const closed = dep.state === "closed";
  const done = closed || dep.workflow_status === "done";
  const label = closed
    ? "已关闭"
    : ISSUE_WORKFLOW_STATUS_LABELS[dep.workflow_status];

  const statusClass = done
    ? "text-muted-foreground"
    : dep.workflow_status === "in_progress"
      ? "text-amber-600 dark:text-amber-400"
      : "text-slate-600 dark:text-slate-400";

  return (
    <Link
      to={`/projects/${dep.project_id}/issues/${dep.issue_id}`}
      onClick={onNavigate}
      className="inline-flex max-w-full items-center gap-1.5 rounded-md border bg-muted/40 px-1.5 py-0.5 text-xs hover:bg-muted"
    >
      <span className="shrink-0 font-mono text-[11px] text-muted-foreground">
        {dep.reference}
      </span>
      <span
        className={cn(
          "min-w-0 truncate",
          done && "text-muted-foreground line-through",
        )}
      >
        {dep.title}
      </span>
      <span className={cn("shrink-0", statusClass)}>{label}</span>
    </Link>
  );
}
