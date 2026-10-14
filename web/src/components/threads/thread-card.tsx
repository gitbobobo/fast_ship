import { Link } from "react-router";
import { GitBranch, GitPullRequest } from "lucide-react";
import type { ThreadSummary } from "@/lib/api/threads";
import {
  projectColorIndex,
  projectInitial,
  threadDetailPath,
} from "@/lib/threads";
import { formatRelativeTime } from "@/lib/utils/format";
import { cn } from "@/lib/utils";
import { ProviderIcon } from "./provider-icon";
import { ThreadStatusBadge } from "./thread-status-badge";

/** 项目徽章的柔和底色调色板，按项目名哈希挑选。 */
const BADGE_TONES = [
  "bg-red-500/15 text-red-600 dark:text-red-400",
  "bg-orange-500/15 text-orange-600 dark:text-orange-400",
  "bg-amber-500/15 text-amber-700 dark:text-amber-400",
  "bg-emerald-500/15 text-emerald-600 dark:text-emerald-400",
  "bg-teal-500/15 text-teal-600 dark:text-teal-400",
  "bg-sky-500/15 text-sky-600 dark:text-sky-400",
  "bg-indigo-500/15 text-indigo-600 dark:text-indigo-400",
  "bg-fuchsia-500/15 text-fuchsia-600 dark:text-fuchsia-400",
];

export function ProjectBadge({
  name,
  className,
}: {
  name: string;
  className?: string;
}) {
  return (
    <span
      aria-hidden
      className={cn(
        "flex size-5 shrink-0 items-center justify-center rounded-md text-[11px] leading-none font-semibold",
        BADGE_TONES[projectColorIndex(name, BADGE_TONES.length)],
        className,
      )}
    >
      {projectInitial(name)}
    </span>
  );
}

export function ThreadCard({ thread }: { thread: ThreadSummary }) {
  return (
    <Link
      to={threadDetailPath(thread)}
      data-testid="thread-card"
      data-settled={thread.settled || undefined}
      className={cn(
        "group flex min-w-0 flex-col gap-1.5 rounded-xl bg-card p-3.5 text-sm ring-1 ring-foreground/10 transition-shadow hover:ring-foreground/25 focus-visible:outline-hidden focus-visible:ring-2 focus-visible:ring-ring",
        thread.status === "waiting" && "ring-amber-500/50 hover:ring-amber-500",
      )}
    >
      <div className="flex items-center justify-between gap-3">
        <span className="flex min-w-0 items-center gap-2">
          <ProjectBadge
            name={thread.project_name}
            className={cn(
              thread.settled &&
                "opacity-40 grayscale transition group-hover:opacity-100 group-hover:grayscale-0",
            )}
          />
          <span
            className={cn(
              "truncate text-xs text-muted-foreground",
              thread.settled &&
                "text-muted-foreground/70 transition-colors group-hover:text-muted-foreground",
            )}
          >
            {thread.project_name}
          </span>
        </span>
        <ThreadStatusBadge
          status={thread.status}
          runStartedAt={thread.run_started_at}
        />
      </div>
      <h2
        className={cn(
          "line-clamp-2 text-sm leading-5 font-medium break-words",
          thread.settled &&
            "text-foreground/60 transition-colors group-hover:text-foreground",
        )}
      >
        {thread.settled && <span className="sr-only">已完结，</span>}
        {thread.title || "未命名线程"}
      </h2>
      <div className="flex min-w-0 items-center justify-between gap-3 text-xs text-muted-foreground">
        <span className="flex min-w-0 items-center gap-1.5">
          {thread.branch && (
            <>
              <GitBranch className="size-3.5 shrink-0" />
              <span className="truncate">{thread.branch}</span>
            </>
          )}
        </span>
        {(thread.provider || thread.model) && (
          <span
            title={[thread.provider, thread.model].filter(Boolean).join(" / ")}
            className={cn(
              "flex min-w-0 shrink-0 items-center gap-1.5",
              thread.settled &&
                "opacity-40 grayscale transition group-hover:opacity-100 group-hover:grayscale-0",
            )}
          >
            <ProviderIcon providerInstanceId={thread.provider} />
            {thread.model && (
              <span className="max-w-32 truncate">{thread.model}</span>
            )}
          </span>
        )}
      </div>
      <div className="mt-auto flex items-center justify-between pt-1 text-xs text-muted-foreground">
        <span>{formatRelativeTime(thread.updated_at)}</span>
        {thread.pull_request_count > 0 && (
          <span
            className="inline-flex items-center gap-1"
            aria-label={`${thread.pull_request_count} 个 PR`}
          >
            <GitPullRequest className="size-3.5" />
            {thread.pull_request_count}
          </span>
        )}
      </div>
    </Link>
  );
}
