import type { ReactNode } from "react";
import { Link } from "react-router";
import { GitBranch, GitPullRequest } from "lucide-react";
import type { ThreadSummary } from "@/lib/api/threads";
import { threadDetailPath } from "@/lib/threads";
import { formatDate, formatRelativeTime } from "@/lib/utils/format";
import { cn } from "@/lib/utils";
import {
  PreviewCard,
  PreviewCardContent,
  PreviewCardTrigger,
} from "@/components/ui/preview-card";
import { ProjectBadge } from "./project-badge";
import { ProviderIcon } from "./provider-icon";
import { ThreadStatusBadge } from "./thread-status-badge";

/**
 * 侧栏里的三行式线程条目：第一行项目徽章+名称与状态，第二行两行 clamp 的
 * 标题，第三行分支与 PR 数/相对时间。整行即链接，左侧等待标记是 2px 竖条
 * 而非描边；悬浮/聚焦展开的完整信息卡在下方 PreviewCard 里。
 */
export function ThreadRow({
  thread,
  active = false,
}: {
  thread: ThreadSummary;
  /** 右栏正在显示这个线程：整行高亮并标成当前页。 */
  active?: boolean;
}) {
  return (
    <PreviewCard>
      <PreviewCardTrigger
        delay={300}
        render={
          <Link
            to={threadDetailPath(thread)}
            data-testid="thread-card"
            data-settled={thread.settled || undefined}
            aria-current={active ? "page" : undefined}
            className={cn(
              "group relative flex flex-col gap-1 rounded-lg px-2.5 py-2 transition-colors hover:bg-accent/60 focus-visible:outline-hidden focus-visible:ring-2 focus-visible:ring-ring",
              active && "bg-accent",
              thread.settled &&
                "opacity-60 hover:opacity-100 focus-visible:opacity-100",
            )}
          />
        }
      >
        {thread.status === "waiting" && (
          <span
            aria-hidden
            className="absolute inset-y-1.5 left-0 w-0.5 rounded bg-amber-500"
          />
        )}
        <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <ProjectBadge
            name={thread.project_name}
            className="size-4 text-[10px]"
          />
          <span className="min-w-0 flex-1 truncate">{thread.project_name}</span>
          <ThreadStatusBadge
            status={thread.status}
            runStartedAt={thread.run_started_at}
          />
        </div>
        <h2
          className={cn(
            "line-clamp-2 text-sm leading-5 font-medium",
            thread.settled &&
              "text-foreground/60 transition-colors group-hover:text-foreground",
          )}
        >
          {thread.settled && <span className="sr-only">已完结，</span>}
          {thread.title || "未命名线程"}
        </h2>
        <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
          {thread.branch && (
            <>
              <GitBranch aria-hidden className="size-3.5 shrink-0" />
              <span className="truncate">{thread.branch}</span>
            </>
          )}
          <span className="flex-1" />
          {thread.pull_request_count > 0 && (
            <span
              className="inline-flex shrink-0 items-center gap-1"
              aria-label={`${thread.pull_request_count} 个 PR`}
            >
              <GitPullRequest aria-hidden className="size-3" />
              {thread.pull_request_count}
            </span>
          )}
          <span className="shrink-0 tabular-nums">
            {formatRelativeTime(thread.updated_at)}
          </span>
        </div>
      </PreviewCardTrigger>
      <PreviewCardContent
        side="right"
        align="start"
        sideOffset={10}
        collisionPadding={12}
        data-testid="thread-preview"
      >
        <ThreadPreviewContent thread={thread} />
      </PreviewCardContent>
    </PreviewCard>
  );
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt className="shrink-0 text-muted-foreground">{label}</dt>
      <dd className="min-w-0">{children}</dd>
    </>
  );
}

/** 悬浮卡片正文：承载行上放不下的完整信息；单独导出便于单测。 */
export function ThreadPreviewContent({ thread }: { thread: ThreadSummary }) {
  const model = [thread.provider, thread.model].filter(Boolean).join(" / ");
  return (
    <div className="space-y-2.5 text-sm">
      <div className="space-y-1">
        <div className="flex items-start justify-between gap-2">
          <h3 className="min-w-0 leading-snug font-medium break-words [overflow-wrap:anywhere]">
            {thread.title || "未命名线程"}
          </h3>
          {thread.settled && (
            <span className="shrink-0 rounded-full bg-muted px-2 py-0.5 text-xs text-muted-foreground">
              已完结
            </span>
          )}
        </div>
        <p className="text-xs text-muted-foreground">
          {thread.environment_name} · {thread.project_name}
        </p>
      </div>
      <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1.5 text-xs">
        <Fact label="状态">
          <ThreadStatusBadge
            status={thread.status}
            runStartedAt={thread.run_started_at}
          />
        </Fact>
        <Fact label="分支">
          <span className="break-all">{thread.branch || "—"}</span>
        </Fact>
        <Fact label="模型">
          {model ? (
            <span className="inline-flex items-center gap-1.5">
              <ProviderIcon providerInstanceId={thread.provider} />
              <span className="break-all">{model}</span>
            </span>
          ) : (
            "—"
          )}
        </Fact>
        <Fact label="更新时间">
          {formatRelativeTime(thread.updated_at)}（{formatDate(thread.updated_at)}）
        </Fact>
        <Fact label="PR">
          {thread.pull_request_count > 0 ? (
            <span className="inline-flex items-center gap-1">
              <GitPullRequest aria-hidden className="size-3.5" />
              {thread.pull_request_count} 个
            </span>
          ) : (
            "—"
          )}
        </Fact>
      </dl>
    </div>
  );
}
