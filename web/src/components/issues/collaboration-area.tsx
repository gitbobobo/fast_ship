import {
  Check,
  GitCommit,
  HelpCircle,
  Lightbulb,
  ListChecks,
  ShieldCheck,
  Sparkles,
} from "lucide-react";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { Skeleton } from "@/components/ui/skeleton";
import { GitHubContent } from "@/components/github-content";
import { useIssueCollab } from "@/lib/hooks/use-issue-collab";
import { getInitials } from "@/lib/utils";
import { formatRelativeTime } from "@/lib/utils/format";

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

function SuggestionsSection({ suggestions }: { suggestions: IssueCollabSuggestion[] }) {
  return (
    <div className="space-y-3">
      <div className="flex items-center gap-2 text-sm font-semibold">
        <Lightbulb className="h-4 w-4 text-amber-500" />
        实施建议（{suggestions.length}）
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

function PlanSection({ plan }: { plan: IssueCollabPlan }) {
  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-2 text-sm font-semibold">
          <ListChecks className="h-4 w-4 text-sky-500" />
          计划
        </div>
        <CollabActorBadge actor={plan.author} />
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

function ReviewSection({ review }: { review: IssueCollabReview }) {
  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-2 text-sm font-semibold">
          <ShieldCheck className="h-4 w-4 text-emerald-500" />
          审查结果
        </div>
        <CollabActorBadge actor={review.author} />
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
  project,
  summary,
}: {
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
        <CollabActorBadge actor={summary.author} />
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

export function CollaborationArea({ issueId, project }: CollaborationAreaProps) {
  const { data, isLoading } = useIssueCollab(issueId);
  const suggestions = data?.suggestions ?? [];
  const plan = data?.plan ?? null;
  const review = data?.review ?? null;
  const summary = data?.summary ?? null;

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-2">
        <HelpCircle className="h-4 w-4 text-primary" />
        <h2 className="text-sm font-semibold">人机协作区</h2>
      </div>

      {isLoading ? (
        <div className="space-y-3">
          <Skeleton className="h-20 rounded-lg" />
          <Skeleton className="h-20 rounded-lg" />
        </div>
      ) : (
        <>
          {suggestions.length > 0 && <SuggestionsSection suggestions={suggestions} />}
          {plan && <PlanSection plan={plan} />}
          {review && <ReviewSection review={review} />}
          {summary && <SummarySection project={project} summary={summary} />}
        </>
      )}
    </div>
  );
}
