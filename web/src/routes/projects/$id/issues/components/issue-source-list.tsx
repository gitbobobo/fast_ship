import { Fragment, useEffect, useRef } from "react";
import { CircleAlert, GitPullRequest, ListX, Loader2 } from "lucide-react";
import {
  ISSUE_WORKFLOW_STATUS_LABELS,
  type IssueWorkflowStatus,
} from "@/lib/issue-workflow-status";
import { cn, getInitials } from "@/lib/utils";
import { formatRelativeTime } from "@/lib/utils/format";
import { toGitHubMediaProxyUrl } from "@/lib/utils/github-media-proxy";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import type { IssueSourceEntry } from "./use-issue-source";

function UnreadDot() {
  return (
    <span
      role="img"
      aria-label="有未读评论"
      className="h-1.5 w-1.5 shrink-0 rounded-full bg-github-accent"
    />
  );
}

/** 内部状态小圆点用色，与 IssueWorkflowStatusBadge 的三色语义一致 */
const WORKFLOW_STATUS_DOT: Record<IssueWorkflowStatus, string> = {
  "": "bg-neutral-400 dark:bg-neutral-500",
  todo: "bg-slate-500 dark:bg-slate-400",
  in_progress: "bg-amber-500",
  done: "bg-emerald-500",
};

/** 行内元信息之间的分隔点 */
function MetaDot() {
  return (
    <span aria-hidden className="shrink-0 text-muted-foreground/60">
      ·
    </span>
  );
}

/** 来源列表条目里参与展示的标签：github 用远端标签，internal 用本地标签 */
function issueLabels(issue: Issue) {
  return issue.source === "github"
    ? issue.github?.labels ?? []
    : issue.internal_meta?.labels ?? [];
}

export interface IssueSourceListProps {
  entries: IssueSourceEntry[];
  currentIssueId: string;
  onSelect: (issue: Issue) => void;
  isLoading: boolean;
  isError: boolean;
  onRetry: () => void;
  hasNextPage: boolean;
  isFetchingNextPage: boolean;
  onLoadMore: () => void;
  /** 是否显示行内内部状态；来源是看板列（整列相同）时由页面传 false 省略 */
  showInternalStatus?: boolean;
  /** 认证 token：GitHub 负责人头像走媒体代理时附上，避免裸请求 401 */
  token?: string;
  /** 内部滚动容器额外类名（如最大高度） */
  className?: string;
}

/**
 * 详情页来源列表：紧凑两行（编号+未读点+标题 / 弱化元信息），高亮当前问题并自动
 * 滚动到可见。内部状态仅 showInternalStatus 时显示（看板列来源整列相同，省略）；
 * OPEN/CLOSED 仅在来源混有两种状态时显示（圆点着色，不用胶囊）。标签最多 2 个余
 * 「+N」；PR 聚合、相对更新时间与 GitHub 负责人头像按数据有无显示。「已不符合来源
 * 条件」的保留项整行弱化、不展示快照元信息（可能已过期），仅保留标记。桌面左栏与
 * 移动抽屉共用本组件，数据与翻页控制器由页面级的 useIssueSourceList 提供，本组件
 * 不持有副作用。
 */
export function IssueSourceList({
  entries,
  currentIssueId,
  onSelect,
  isLoading,
  isError,
  onRetry,
  hasNextPage,
  isFetchingNextPage,
  onLoadMore,
  showInternalStatus = true,
  token,
  className,
}: IssueSourceListProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const sentinelRef = useRef<HTMLDivElement>(null);

  // OPEN/CLOSED 只在来源混有两种状态时展示（board 来源固定 open，永不触发）
  const hasMixedStates =
    new Set(entries.filter((e) => !e.stale).map((e) => e.issue.state)).size > 1;

  // 自动滚动：当前项或列表变化后把当前项滚进可视区
  useEffect(() => {
    if (!currentIssueId) {
      return;
    }
    containerRef.current
      ?.querySelector(`[data-issue-id="${CSS.escape(currentIssueId)}"]`)
      ?.scrollIntoView({ block: "nearest" });
  }, [currentIssueId, entries.length]);

  useEffect(() => {
    if (!hasNextPage || isFetchingNextPage || isError) {
      return;
    }
    const el = sentinelRef.current;
    if (!el) {
      return;
    }
    const observer = new IntersectionObserver(
      ([entry]) => {
        if (entry.isIntersecting) {
          onLoadMore();
        }
      },
      { rootMargin: "100px" },
    );
    observer.observe(el);
    return () => observer.disconnect();
  }, [hasNextPage, isFetchingNextPage, isError, onLoadMore]);

  if (isLoading) {
    return (
      <div className={cn("flex items-center justify-center gap-2 p-6 text-sm text-muted-foreground", className)}>
        <Loader2 className="h-4 w-4 animate-spin" />
        加载中…
      </div>
    );
  }

  // 有已加载条目时分页/后台刷新失败不整替列表：只在内联提供重试，保住用户已看到的内容
  // 三个非列表态（加载/失败/空）共用 justify-center，图标与文案落在同一垂直位置
  if (isError && entries.length === 0) {
    return (
      <div className={cn("flex flex-col items-center justify-center gap-2 p-6 text-sm", className)}>
        <CircleAlert className="h-5 w-5 text-red-500" />
        <span className="text-red-600 dark:text-red-400">来源列表加载失败</span>
        <button
          type="button"
          onClick={onRetry}
          className="rounded-md border px-3 py-1.5 text-sm font-medium hover:bg-muted"
        >
          重试
        </button>
      </div>
    );
  }

  if (entries.length === 0) {
    return (
      <div className={cn("flex flex-col items-center justify-center gap-2 p-6 text-sm text-muted-foreground", className)}>
        <ListX className="h-5 w-5" />
        <span>来源列表暂无问题</span>
      </div>
    );
  }

  return (
    <div ref={containerRef} data-testid="issue-source-list" className={className}>
      <ul className="space-y-0.5 p-1.5">
        {entries.map((entry) => {
          const isCurrent = entry.issue.id === currentIssueId;
          const hasUnread =
            entry.issue.source === "github" &&
            entry.issue.unread_comments_count > 0;
          const assignees =
            entry.issue.source === "github"
              ? entry.issue.github?.assignees ?? []
              : [];
          return (
            <li key={entry.issue.id}>
              <button
                type="button"
                data-issue-id={entry.issue.id}
                aria-current={isCurrent ? "true" : undefined}
                onClick={() => onSelect(entry.issue)}
                title={
                  entry.stale
                    ? "已不符合来源条件，仅保留在此处"
                    : entry.issue.title
                }
                className={cn(
                  "flex w-full flex-col gap-0.5 rounded-lg border-l-2 px-2 py-1.5 text-left text-sm transition-colors",
                  entry.stale
                    ? "border-transparent text-muted-foreground/70 hover:bg-muted/50"
                    : "border-transparent text-foreground/90 hover:bg-muted",
                  // 当前项保留左侧竖条，背景改淡、不再加粗——靠竖条与底色定位即可
                  isCurrent &&
                    "border-primary bg-accent/50 text-foreground hover:bg-accent/50",
                )}
              >
                {/* 第 1 行：编号（等宽弱化）+ 未读点 + 标题单行截断，悬停看全文 */}
                <span className="flex w-full items-center gap-1.5">
                  <span
                    className={cn(
                      "shrink-0 font-mono text-xs text-muted-foreground",
                      isCurrent && "text-primary",
                    )}
                  >
                    {entry.issue.reference}
                  </span>
                  {hasUnread ? <UnreadDot /> : null}
                  <span className="min-w-0 flex-1 truncate leading-snug">
                    {entry.issue.title}
                  </span>
                </span>
                {/* 第 2 行：弱化小字元信息；stale 保留项的快照可能已过期
                    （如同屏详情已 CLOSED），只保留「已不符合来源条件」标记 */}
                {entry.stale ? (
                  <span className="w-full truncate text-xs text-muted-foreground">
                    已不符合来源条件
                  </span>
                ) : (
                  <span className="flex w-full items-center gap-1 text-[11px] leading-4 text-muted-foreground">
                    <SourceEntryMeta
                      issue={entry.issue}
                      hasMixedStates={hasMixedStates}
                      showInternalStatus={showInternalStatus}
                    />
                    {assignees.length > 0 && (
                      <span
                        className="ml-auto flex shrink-0 items-center -space-x-1"
                        title={assignees.map((a) => a.login).join("、")}
                      >
                        {assignees.slice(0, 3).map((actor) => (
                          <Avatar
                            key={actor.login}
                            className="h-4 w-4 border border-background after:hidden"
                          >
                            {/* 头像是媒体代理 URL（服务端投影改写），不带 token 会 401 */}
                            <AvatarImage
                              src={toGitHubMediaProxyUrl(actor.avatar_url, token)}
                              alt=""
                            />
                            <AvatarFallback className="text-[8px]">
                              {getInitials(actor.login)}
                            </AvatarFallback>
                          </Avatar>
                        ))}
                        {assignees.length > 3 && (
                          <span className="pl-0.5 text-[10px]">
                            +{assignees.length - 3}
                          </span>
                        )}
                      </span>
                    )}
                  </span>
                )}
              </button>
            </li>
          );
        })}
      </ul>
      {(hasNextPage || isError) && (
        <div ref={sentinelRef} className="flex justify-center py-2">
          {isError ? (
            <button
              type="button"
              onClick={onRetry}
              className="rounded-md border px-2.5 py-1 text-xs font-medium hover:bg-muted"
            >
              {hasNextPage ? "加载更多失败，点击重试" : "刷新失败，点击重试"}
            </button>
          ) : (
            isFetchingNextPage && (
              <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
            )
          )}
        </div>
      )}
    </div>
  );
}

/**
 * 单条非 stale 条目的元信息段：内部状态、OPEN/CLOSED（混态才显示）、标签（≤2）、
 * PR 聚合、相对更新时间，用「·」分隔；负责人头像由调用方追加在行尾。
 */
function SourceEntryMeta({
  issue,
  hasMixedStates,
  showInternalStatus,
}: {
  issue: Issue;
  hasMixedStates: boolean;
  showInternalStatus: boolean;
}) {
  const parts: React.ReactNode[] = [];

  if (showInternalStatus) {
    const status: IssueWorkflowStatus = issue.internal_meta?.workflow_status ?? "";
    const label = ISSUE_WORKFLOW_STATUS_LABELS[status] ?? status;
    parts.push(
      <span key="workflow" className="inline-flex min-w-0 items-center gap-1">
        <span
          className={cn(
            "h-1.5 w-1.5 shrink-0 rounded-full",
            WORKFLOW_STATUS_DOT[status] ?? WORKFLOW_STATUS_DOT[""],
          )}
        />
        <span className="truncate">{label}</span>
      </span>,
    );
  }

  if (hasMixedStates) {
    parts.push(
      <span
        key="state"
        className="inline-flex shrink-0 items-center gap-1"
      >
        <span
          className={cn(
            "h-1.5 w-1.5 shrink-0 rounded-full",
            issue.state === "open" ? "bg-emerald-500" : "bg-rose-500",
          )}
        />
        {issue.state === "open" ? "Open" : "Closed"}
      </span>,
    );
  }

  const labels = issueLabels(issue);
  if (labels.length > 0) {
    parts.push(
      <span key="labels" className="inline-flex min-w-0 items-center gap-1">
        {labels.slice(0, 2).map((label) => (
          <span
            key={label.name}
            className="max-w-16 truncate rounded-full px-1.5 text-[10px] leading-4"
            style={{
              backgroundColor: `#${label.color}20`,
              color: `#${label.color}`,
            }}
          >
            {label.name}
          </span>
        ))}
        {labels.length > 2 && (
          <span className="shrink-0">+{labels.length - 2}</span>
        )}
      </span>,
    );
  }

  const prSummary = issue.pull_request_summary;
  if (prSummary && prSummary.total > 0) {
    parts.push(
      <span
        key="prs"
        className="inline-flex shrink-0 items-center gap-0.5"
        title={`${prSummary.total} 个关联 PR${prSummary.merged > 0 ? `，${prSummary.merged} 个已合并` : ""}`}
      >
        <GitPullRequest className="h-3 w-3" />
        {`PR ${prSummary.total}`}
        {prSummary.merged > 0 && ` · 合 ${prSummary.merged}`}
      </span>,
    );
  }

  parts.push(
    <span key="updated" className="truncate">
      {formatRelativeTime(issue.updated_at)}
    </span>,
  );

  return (
    <>
      {parts.map((part, index) => (
        <Fragment key={index}>
          {index > 0 && <MetaDot />}
          {part}
        </Fragment>
      ))}
    </>
  );
}
