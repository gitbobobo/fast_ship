import { useState, type Ref } from "react";
import { Link } from "react-router";
import {
  ArrowLeft,
  CheckCircle2,
  ExternalLink,
  ListChecks,
  Sparkles,
  Trash2,
} from "lucide-react";
import { GitHubContent } from "@/components/github-content";
import { CollaborationArea } from "@/components/issues/collaboration-area";
import {
  CopyIssuePromptButton,
  type CopyIssuePromptButtonHandle,
} from "@/components/issues/copy-issue-prompt-button";
import { Button, buttonVariants } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useIssue } from "@/lib/hooks/use-issues";
import { useIssueCollab } from "@/lib/hooks/use-issue-collab";
import { ISSUE_WORKFLOW_STATUS_LABELS } from "@/lib/issue-workflow-status";
import { cn } from "@/lib/utils";

function StatePill({ state }: { state: Issue["state"] }) {
  const open = state === "open";
  return (
    <span
      className={cn(
        "inline-flex items-center rounded-full border px-2 py-0.5 text-[11px] font-medium",
        open
          ? "border-emerald-500/20 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400"
          : "border-rose-500/20 bg-rose-500/10 text-rose-600 dark:text-rose-400",
      )}
    >
      {open ? "Open" : "Closed"}
    </span>
  );
}

function WorkflowStatusPill({ status }: { status?: string | null }) {
  if (!status) return null;
  const label =
    ISSUE_WORKFLOW_STATUS_LABELS[
      status as keyof typeof ISSUE_WORKFLOW_STATUS_LABELS
    ];
  if (!label) return null;
  return (
    <span
      className={cn(
        "inline-flex items-center rounded-full border px-2 py-0.5 text-[11px] font-medium",
        status === "in_progress"
          ? "border-amber-500/20 bg-amber-500/10 text-amber-600 dark:text-amber-400"
          : status === "done"
            ? "border-emerald-500/20 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400"
            : "border-slate-500/20 bg-slate-500/10 text-slate-600 dark:text-slate-400",
      )}
    >
      {label}
    </span>
  );
}

function DependencyChip({
  dep,
  onPeek,
}: {
  dep: RecommendationDependency;
  onPeek: () => void;
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
    <button
      type="button"
      onClick={onPeek}
      title={`查看 ${dep.reference}`}
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
    </button>
  );
}

/** 只读任务清单：勾选态仅展示，不挂编辑交互。 */
function ReadOnlyChecklist({ issue }: { issue: Issue }) {
  const checklist = issue.internal_meta?.checklist ?? [];
  if (checklist.length === 0) return null;
  const done = checklist.filter((item) => item.is_completed).length;
  const percent = Math.round((done * 100) / checklist.length);
  return (
    <section aria-label="任务清单" className="space-y-2">
      <div className="flex items-center justify-between">
        <h3 className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
          <ListChecks className="h-3.5 w-3.5" />
          任务清单
        </h3>
        <span className="text-xs text-muted-foreground tabular-nums">
          {done}/{checklist.length} 项完成
        </span>
      </div>
      <div className="h-1.5 overflow-hidden rounded-full bg-muted">
        <div
          className={cn(
            "h-full rounded-full",
            percent >= 100 ? "bg-emerald-500" : "bg-amber-500",
          )}
          style={{ width: `${percent}%` }}
        />
      </div>
      <ul className="space-y-1.5">
        {checklist.map((item) => (
          <li key={item.id} className="flex items-center gap-2 text-sm">
            <span
              className={cn(
                "inline-flex h-4 w-4 shrink-0 items-center justify-center rounded border",
                item.is_completed
                  ? "border-emerald-500 bg-emerald-500 text-white"
                  : "border-input bg-background text-transparent",
              )}
            >
              <CheckCircle2 className="h-3 w-3" />
            </span>
            <span
              className={cn(
                "min-w-0",
                item.is_completed && "text-muted-foreground line-through",
              )}
            >
              {item.title}
            </span>
          </li>
        ))}
      </ul>
    </section>
  );
}

function IssueLabels({ issue }: { issue: Issue }) {
  const labels =
    issue.source === "github"
      ? (issue.github?.labels ?? [])
      : (issue.internal_meta?.labels ?? []);
  if (labels.length === 0) return null;
  return (
    <div className="flex flex-wrap gap-1.5">
      {labels.map((label) => (
        <span
          key={label.name}
          className="inline-flex items-center rounded-full border px-2 py-0.5 text-xs font-medium"
          style={{
            backgroundColor: `#${label.color}15`,
            borderColor: `#${label.color}30`,
            color: `#${label.color}`,
          }}
        >
          {label.name}
        </span>
      ))}
    </div>
  );
}

interface RecommendationDetailPaneProps {
  /** 当前选中的推荐条目。 */
  item: IssueRecommendation;
  onRemove: () => void;
  /** Enter 键复制入口，转发给当前展示的复制按钮。 */
  copyHandleRef: Ref<CopyIssuePromptButtonHandle>;
}

/**
 * 推荐弹框右栏：选中条目的只读详情（无评论/时间线）。
 * 前置依赖 chip 点击切到该依赖的「看一眼」视图（可为推荐列表外的 issue），
 * 顶部「← 返回 <reference>」回链回到选中条目；peek 下隐藏推荐理由与移除入口。
 * 切换选中项时由父组件以 key 重挂载本组件，peek 状态随之重置。
 */
export function RecommendationDetailPane({
  item,
  onRemove,
  copyHandleRef,
}: RecommendationDetailPaneProps) {
  const [peekIssueId, setPeekIssueId] = useState<string | null>(null);
  const peeking = peekIssueId !== null;
  const displayIssueId = peekIssueId ?? item.issue.id;

  const { data: issue, isLoading, isError, refetch } = useIssue(displayIssueId);
  const { data: collab } = useIssueCollab(displayIssueId);
  const hasCollab = !!(collab?.consensus || collab?.summary);

  return (
    <div className="flex min-h-0 min-w-0 flex-col">
      {peeking && (
        <div className="border-b px-5 py-2">
          <button
            type="button"
            onClick={() => setPeekIssueId(null)}
            className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
          >
            <ArrowLeft className="h-3 w-3" />
            返回 {item.issue.reference}
          </button>
        </div>
      )}

      <div className="min-h-0 flex-1 space-y-4 overflow-y-auto px-5 py-4">
        {isLoading ? (
          <div className="space-y-3">
            <Skeleton className="h-6 w-2/3" />
            <Skeleton className="h-24" />
            <Skeleton className="h-24" />
          </div>
        ) : isError || !issue ? (
          <div className="flex min-h-40 flex-col items-center justify-center gap-3 py-8">
            <p className="text-sm text-muted-foreground" role="status">
              详情加载失败
            </p>
            <Button
              variant="outline"
              size="sm"
              onClick={() => void refetch()}
            >
              重试
            </Button>
          </div>
        ) : (
          <>
            <div>
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-mono text-xs text-muted-foreground">
                  {issue.reference}
                </span>
                <StatePill state={issue.state} />
                <WorkflowStatusPill
                  status={issue.internal_meta?.workflow_status}
                />
              </div>
              <h2 className="mt-1.5 text-base font-semibold leading-snug">
                {issue.title}
              </h2>
            </div>

            {!peeking && (
              <div className="rounded-lg border border-violet-500/20 bg-violet-500/5 px-3 py-2.5">
                <p className="flex items-center gap-1.5 text-xs font-medium text-violet-600 dark:text-violet-400">
                  <Sparkles className="h-3 w-3" />
                  推荐理由
                </p>
                <p className="mt-1 text-sm leading-relaxed whitespace-pre-wrap text-foreground/90">
                  {item.reason}
                </p>
              </div>
            )}

            {!peeking && item.dependencies.length > 0 && (
              <div className="flex items-start gap-2">
                <span className="shrink-0 py-0.5 text-xs text-muted-foreground">
                  前置
                </span>
                <div className="flex min-w-0 flex-wrap gap-1.5">
                  {item.dependencies.map((dep) => (
                    <DependencyChip
                      key={dep.issue_id}
                      dep={dep}
                      onPeek={() => setPeekIssueId(dep.issue_id)}
                    />
                  ))}
                </div>
              </div>
            )}

            {issue.body || issue.body_html ? (
              <div className="markdown-body text-sm">
                <GitHubContent html={issue.body_html} markdown={issue.body} />
              </div>
            ) : (
              <p className="text-sm italic text-muted-foreground">暂无描述</p>
            )}

            <ReadOnlyChecklist issue={issue} />
            <IssueLabels issue={issue} />
            {hasCollab && (
              <CollaborationArea issueId={issue.id} readOnly />
            )}
          </>
        )}
      </div>

      <div className="flex items-center gap-1 border-t px-5 py-2.5">
        {issue ? (
          <>
            <CopyIssuePromptButton
              projectId={issue.project_id}
              issueId={issue.id}
              reason={peeking ? undefined : item.reason}
              handleRef={copyHandleRef}
            />
            {!peeking && (
              <Button
                variant="ghost"
                size="sm"
                className="h-7 gap-1.5 text-xs text-muted-foreground hover:text-destructive"
                onClick={onRemove}
              >
                <Trash2 className="h-3.5 w-3.5" />
                移除推荐
              </Button>
            )}
            <Link
              to={`/projects/${issue.project_id}/issues/${issue.id}`}
              className={cn(
                buttonVariants({ variant: "ghost", size: "sm" }),
                "ml-auto h-7 gap-1.5 text-xs text-muted-foreground hover:text-foreground",
              )}
            >
              <ExternalLink className="h-3.5 w-3.5" />
              打开完整详情页
            </Link>
          </>
        ) : null}
      </div>
    </div>
  );
}
