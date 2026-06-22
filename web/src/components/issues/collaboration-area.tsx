import { useEffect, useState } from "react";
import {
  Check,
  GitCommit,
  HelpCircle,
  Lightbulb,
  ListChecks,
  Loader2,
  ShieldCheck,
  Sparkles,
  Trash2,
} from "lucide-react";
import { toast } from "sonner";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { Button } from "@/components/ui/button";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { GitHubContent } from "@/components/github-content";
import {
  type CollabDeleteSection,
  useDeleteCollabSection,
  useIssueCollab,
} from "@/lib/hooks/use-issue-collab";
import { cn, getInitials } from "@/lib/utils";
import { formatRelativeTime } from "@/lib/utils/format";

type TabValue = "suggestions" | "plan" | "review" | "summary";

const TAB_ORDER: TabValue[] = ["suggestions", "plan", "review", "summary"];

const SECTION_SUCCESS_TOAST: Record<CollabDeleteSection, string> = {
  all: "协作区已清空",
  suggestions: "已清空全部建议",
  plan: "计划已删除",
  review: "审查结果已删除",
  summary: "完成总结已删除",
};

interface CollaborationAreaProps {
  issueId: string;
  project?: Project | null;
}

function buildCommitUrl(project: Project | null | undefined, sha: string): string | null {
  if (project?.github_owner && project?.github_repo) {
    return `https://github.com/${project.github_owner}/${project.github_repo}/commit/${sha}`;
  }
  return null;
}

function CollabActorBadge({ actor }: { actor: IssueCollabActor }) {
  if (actor.kind === "agent") {
    return (
      <span className="inline-flex items-center gap-1 rounded-full border border-violet-500/20 bg-violet-500/10 px-2 py-0.5 text-xs font-medium text-violet-600 dark:text-violet-400">
        <Sparkles className="h-3 w-3" />
        {actor.login}
      </span>
    );
  }
  return (
    <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
      <Avatar size="sm">
        {actor.avatar_url ? <AvatarImage src={actor.avatar_url} alt={actor.login} /> : null}
        <AvatarFallback>{getInitials(actor.login)}</AvatarFallback>
      </Avatar>
      <span className="font-medium text-foreground">@{actor.login}</span>
    </span>
  );
}

interface DeleteCollabButtonProps {
  issueId: string;
  section: CollabDeleteSection;
  ariaLabel: string;
  title: string;
  description: string;
  variant?: "destructive" | "ghost";
  className?: string;
}

function DeleteCollabButton({
  issueId,
  section,
  ariaLabel,
  title,
  description,
  variant = "ghost",
  className,
}: DeleteCollabButtonProps) {
  const [open, setOpen] = useState(false);
  const deleteSection = useDeleteCollabSection(issueId, section);

  const handleConfirm = async (event: React.MouseEvent) => {
    event.preventDefault();
    try {
      await deleteSection.mutateAsync();
      toast.success(SECTION_SUCCESS_TOAST[section]);
      setOpen(false);
    } catch {
      toast.error("删除失败，请稍后重试");
    }
  };

  return (
    <AlertDialog open={open} onOpenChange={setOpen}>
      <AlertDialogTrigger asChild>
        <Button
          type="button"
          variant={variant}
          size="icon-sm"
          aria-label={ariaLabel}
          disabled={deleteSection.isPending}
          className={className}
        >
          <Trash2 className="h-4 w-4" />
        </Button>
      </AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{description}</AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={deleteSection.isPending}>取消</AlertDialogCancel>
          <AlertDialogAction
            onClick={(event) => void handleConfirm(event)}
            disabled={deleteSection.isPending}
            className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
          >
            {deleteSection.isPending ? (
              <>
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                删除中…
              </>
            ) : (
              "确认删除"
            )}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

function SuggestionsSection({
  issueId,
  suggestions,
}: {
  issueId: string;
  suggestions: IssueCollabSuggestion[];
}) {
  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-2 text-sm font-semibold">
          <Lightbulb className="h-4 w-4 text-amber-500" />
          实施建议（{suggestions.length}）
        </div>
        <DeleteCollabButton
          issueId={issueId}
          section="suggestions"
          ariaLabel="清空全部建议"
          title="清空全部建议？"
          description="将删除该问题的全部实施建议，不可恢复。"
        />
      </div>
      <div className="space-y-2">
        {suggestions.map((suggestion, index) => (
          <div key={suggestion.id} className="rounded-lg border bg-card p-3">
            <div className="mb-1.5 flex items-start gap-2">
              <span className="mt-0.5 flex h-5 w-5 shrink-0 items-center justify-center rounded-full bg-amber-500/10 text-xs font-medium text-amber-600 dark:text-amber-400">
                {index + 1}
              </span>
              <div className="markdown-body min-w-0 flex-1 text-sm">
                <GitHubContent markdown={suggestion.body} />
              </div>
            </div>
            <div className="flex items-center justify-between gap-2">
              <CollabActorBadge actor={suggestion.author} />
              <span className="text-xs text-muted-foreground">
                {formatRelativeTime(suggestion.created_at)}
              </span>
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}

function PlanSection({ issueId, plan }: { issueId: string; plan: IssueCollabPlan }) {
  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-2 text-sm font-semibold">
          <ListChecks className="h-4 w-4 text-sky-500" />
          计划
        </div>
        <div className="flex items-center gap-2">
          <DeleteCollabButton
            issueId={issueId}
            section="plan"
            ariaLabel="删除计划"
            title="删除计划？"
            description="将删除该问题的计划内容，不可恢复。"
          />
          <CollabActorBadge actor={plan.author} />
        </div>
      </div>
      <div className="rounded-lg border bg-card p-3">
        <div className="markdown-body text-sm">
          <GitHubContent markdown={plan.body} />
        </div>
        <p className="mt-2 text-xs text-muted-foreground">更新于 {formatRelativeTime(plan.updated_at)}</p>
      </div>
    </div>
  );
}

function ReviewSection({ issueId, review }: { issueId: string; review: IssueCollabReview }) {
  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-2 text-sm font-semibold">
          <ShieldCheck className="h-4 w-4 text-emerald-500" />
          审查结果
        </div>
        <div className="flex items-center gap-2">
          <DeleteCollabButton
            issueId={issueId}
            section="review"
            ariaLabel="删除审查结果"
            title="删除审查结果？"
            description="将删除该问题的审查结果，不可恢复。"
          />
          <CollabActorBadge actor={review.author} />
        </div>
      </div>
      <div className="rounded-lg border bg-card p-3">
        <div className="markdown-body text-sm">
          <GitHubContent markdown={review.body} />
        </div>
        <p className="mt-2 text-xs text-muted-foreground">更新于 {formatRelativeTime(review.updated_at)}</p>
      </div>
    </div>
  );
}

function SummarySection({
  issueId,
  project,
  summary,
}: {
  issueId: string;
  project: Project | null | undefined;
  summary: IssueCollabSummary;
}) {
  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-2 text-sm font-semibold">
          <Check className="h-4 w-4 text-emerald-500" />
          完成总结
        </div>
        <div className="flex items-center gap-2">
          <DeleteCollabButton
            issueId={issueId}
            section="summary"
            ariaLabel="删除完成总结"
            title="删除完成总结？"
            description="将删除该问题的完成总结，不可恢复。"
          />
          <CollabActorBadge actor={summary.author} />
        </div>
      </div>
      <div className="rounded-lg border bg-card p-3">
        <div className="markdown-body text-sm">
          <GitHubContent markdown={summary.body} />
        </div>
        {summary.commit_ids.length > 0 && (
          <div className="mt-3 flex flex-wrap items-center gap-2 border-t pt-3">
            <span className="text-xs text-muted-foreground">提交：</span>
            {summary.commit_ids.map((sha) => {
              const url = buildCommitUrl(project, sha);
              return url ? (
                <a
                  key={sha}
                  href={url}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="inline-flex items-center gap-1 rounded-full border bg-muted/40 px-2 py-0.5 font-mono text-xs hover:bg-muted"
                >
                  <GitCommit className="h-3 w-3" />
                  {sha.slice(0, 7)}
                </a>
              ) : (
                <span
                  key={sha}
                  className="inline-flex items-center gap-1 rounded-full border bg-muted/40 px-2 py-0.5 font-mono text-xs"
                >
                  <GitCommit className="h-3 w-3" />
                  {sha.slice(0, 7)}
                </span>
              );
            })}
          </div>
        )}
        <p className="mt-2 text-xs text-muted-foreground">
          更新于 {formatRelativeTime(summary.updated_at)}
        </p>
      </div>
    </div>
  );
}

function EmptyTabPanel() {
  return (
    <div className="flex min-h-[88px] items-center justify-center py-8">
      <p className="text-muted-foreground" role="status">
        暂无内容
      </p>
    </div>
  );
}

export function CollaborationArea({ issueId, project }: CollaborationAreaProps) {
  const { data, isLoading } = useIssueCollab(issueId);
  const suggestions = data?.suggestions ?? [];
  const plan = data?.plan ?? null;
  const review = data?.review ?? null;
  const summary = data?.summary ?? null;

  const hasContent: Record<TabValue, boolean> = {
    suggestions: suggestions.length > 0,
    plan: plan !== null,
    review: review !== null,
    summary: summary !== null,
  };

  const defaultTab = TAB_ORDER.find((tab) => hasContent[tab]) ?? null;
  const hasAnyContent = defaultTab !== null;
  const [activeTab, setActiveTab] = useState<TabValue | null>(null);
  const resolvedTab = activeTab ?? defaultTab;

  useEffect(() => {
    setActiveTab(null);
  }, [issueId]);

  useEffect(() => {
    if (!hasAnyContent) {
      setActiveTab(null);
      return;
    }
    setActiveTab((current) => {
      if (current === null || !hasContent[current]) {
        return defaultTab;
      }
      return current;
    });
  }, [
    issueId,
    defaultTab,
    hasAnyContent,
    hasContent.suggestions,
    hasContent.plan,
    hasContent.review,
    hasContent.summary,
  ]);

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <HelpCircle className="h-4 w-4 text-primary" />
          <h2 className="text-sm font-semibold">人机协作区</h2>
        </div>
        {hasAnyContent ? (
          <DeleteCollabButton
            issueId={issueId}
            section="all"
            ariaLabel="清空协作区"
            title="清空协作区？"
            description="将删除全部建议、计划、审查与总结，不可恢复。"
            variant="destructive"
          />
        ) : null}
      </div>

      {isLoading ? (
        <div className="space-y-3">
          <p className="sr-only">正在加载人机协作区</p>
          <Skeleton className="h-20 rounded-lg" />
          <Skeleton className="h-20 rounded-lg" />
        </div>
      ) : hasAnyContent && resolvedTab ? (
        <Tabs value={resolvedTab} onValueChange={(value) => setActiveTab(value as TabValue)} className="flex flex-col gap-4">
          <TabsList aria-label="人机协作区内容" className="w-full justify-start overflow-x-auto">
            <TabsTrigger
              value="suggestions"
              aria-label={
                hasContent.suggestions
                  ? `实施建议，${suggestions.length} 条`
                  : "实施建议，暂无内容"
              }
              className={cn("shrink-0", !hasContent.suggestions && "text-muted-foreground")}
            >
              <Lightbulb className="h-3.5 w-3.5" aria-hidden="true" />
              {hasContent.suggestions
                ? `实施建议（${suggestions.length}）`
                : "实施建议"}
            </TabsTrigger>
            <TabsTrigger
              value="plan"
              aria-label={hasContent.plan ? "计划，有内容" : "计划，暂无内容"}
              className={cn("shrink-0", !hasContent.plan && "text-muted-foreground")}
            >
              <ListChecks className="h-3.5 w-3.5" aria-hidden="true" />
              计划
              {hasContent.plan ? (
                <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-primary" aria-hidden="true" />
              ) : null}
            </TabsTrigger>
            <TabsTrigger
              value="review"
              aria-label={hasContent.review ? "审查结果，有内容" : "审查结果，暂无内容"}
              className={cn("shrink-0", !hasContent.review && "text-muted-foreground")}
            >
              <ShieldCheck className="h-3.5 w-3.5" aria-hidden="true" />
              审查结果
              {hasContent.review ? (
                <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-primary" aria-hidden="true" />
              ) : null}
            </TabsTrigger>
            <TabsTrigger
              value="summary"
              aria-label={hasContent.summary ? "完成总结，有内容" : "完成总结，暂无内容"}
              className={cn("shrink-0", !hasContent.summary && "text-muted-foreground")}
            >
              <Check className="h-3.5 w-3.5" aria-hidden="true" />
              完成总结
              {hasContent.summary ? (
                <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-primary" aria-hidden="true" />
              ) : null}
            </TabsTrigger>
          </TabsList>

          <TabsContent value="suggestions">
            {hasContent.suggestions ? (
              <SuggestionsSection issueId={issueId} suggestions={suggestions} />
            ) : (
              <EmptyTabPanel />
            )}
          </TabsContent>
          <TabsContent value="plan">
            {plan ? <PlanSection issueId={issueId} plan={plan} /> : <EmptyTabPanel />}
          </TabsContent>
          <TabsContent value="review">
            {review ? <ReviewSection issueId={issueId} review={review} /> : <EmptyTabPanel />}
          </TabsContent>
          <TabsContent value="summary">
            {summary ? (
              <SummarySection issueId={issueId} project={project} summary={summary} />
            ) : (
              <EmptyTabPanel />
            )}
          </TabsContent>
        </Tabs>
      ) : null}
    </div>
  );
}
