import { Link } from "react-router";
import { Cpu, GitBranch, GitPullRequest } from "lucide-react";
import type { ThreadSummary } from "@/lib/api/threads";
import { threadDetailPath } from "@/lib/threads";
import { formatRelativeTime } from "@/lib/utils/format";
import { cn } from "@/lib/utils";
import { ThreadStatusBadge } from "./thread-status-badge";

export function ThreadCard({ thread }: { thread: ThreadSummary }) {
  const model = [thread.provider, thread.model].filter(Boolean).join(" / ");

  return (
    <Link
      to={threadDetailPath(thread)}
      data-testid="thread-card"
      className={cn(
        "group flex min-w-0 flex-col gap-3 rounded-xl bg-card p-4 text-sm ring-1 ring-foreground/10 transition-shadow hover:ring-foreground/25 focus-visible:outline-hidden focus-visible:ring-2 focus-visible:ring-ring",
        thread.status === "waiting" && "ring-amber-500/50 hover:ring-amber-500",
      )}
    >
      <div className="flex items-start justify-between gap-3">
        <h2 className="line-clamp-2 min-h-10 min-w-0 flex-1 text-sm leading-5 font-medium break-words">
          {thread.title || "未命名线程"}
        </h2>
        <ThreadStatusBadge status={thread.status} />
      </div>
      <p className="truncate text-xs text-muted-foreground">
        {thread.environment_name} · {thread.project_name}
      </p>
      <div className="flex min-w-0 flex-col gap-1 text-xs text-muted-foreground">
        {thread.branch && (
          <span className="flex min-w-0 items-center gap-1.5">
            <GitBranch className="h-3.5 w-3.5 shrink-0" />
            <span className="truncate">{thread.branch}</span>
          </span>
        )}
        {model && (
          <span className="flex min-w-0 items-center gap-1.5">
            <Cpu className="h-3.5 w-3.5 shrink-0" />
            <span className="truncate">{model}</span>
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
            <GitPullRequest className="h-3.5 w-3.5" />
            {thread.pull_request_count}
          </span>
        )}
      </div>
    </Link>
  );
}
