import { useEffect, useRef, useState } from "react";
import {
  CheckCircle2,
  Circle,
  CircleDot,
  ExternalLink,
  Eye,
  Flag,
  GitBranch,
  GitCommit,
  GitMerge,
  Link2,
  Lock,
  MessageSquare,
  Pencil,
  Tag,
  Unlock,
  User,
} from "lucide-react";
import { GitHubContent } from "@/components/github-content";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Textarea } from "@/components/ui/textarea";
import { useAuthStore } from "@/lib/store/auth-store";
import { toGitHubMediaProxyUrl } from "@/lib/utils/github-media-proxy";
import { cn, getInitials } from "@/lib/utils";
import { formatDate, formatRelativeTime } from "@/lib/utils/format";
import { toast } from "sonner";

export type IssueTimelineItem =
  | { type: "comment"; data: IssueComment; created_at: string }
  | { type: "event"; data: IssueTimelineEvent; created_at: string };

export type IssueTimelineProps = {
  items: IssueTimelineItem[];
  isLoading: boolean;
  hasMore: boolean;
  isFetchingMore: boolean;
  onLoadMore: () => void;
  flashHighlightId: string | null;
  /** 点击定位按钮时把 `#issuecomment-*` / `#issueevent-*` 交给路由写进 URL */
  onNavigateToAnchor: (hash: string) => void;
  /** 复制评论/动态的 GitHub 深链接；url 为空时由调用方给失败提示与兜底文案 */
  onCopyGithubLink: (url: string | null, successMessage: string) => void;
  /** 事件找不到精确 GitHub 链接时的兜底 URL */
  fallbackGithubUrl?: string | null;
};

export type IssueCommentComposerProps = {
  isInternalIssue: boolean;
  /** 返回 true 表示发布成功，模块据此清空输入 */
  onSubmit: (body: string) => Promise<boolean>;
};

function getEventIcon(eventType: string) {
  switch (eventType) {
    case "labeled":
    case "unlabeled":
      return Tag;
    case "milestoned":
    case "demilestoned":
      return Flag;
    case "assigned":
    case "unassigned":
      return User;
    case "closed":
      return CheckCircle2;
    case "reopened":
      return Circle;
    case "renamed":
      return Pencil;
    case "locked":
      return Lock;
    case "unlocked":
      return Unlock;
    case "referenced":
    case "cross-referenced":
      return GitCommit;
    case "merged":
      return GitMerge;
    case "head_ref_deleted":
    case "head_ref_restored":
      return GitBranch;
    case "review_requested":
    case "review_request_removed":
      return Eye;
    case "issue_type_added":
    case "issue_type_removed":
    case "added_type":
    case "removed_type":
      return CircleDot;
    default:
      return Circle;
  }
}

function getLabelFromPayload(payload: Record<string, unknown>) {
  if (!payload || typeof payload !== "object") return null;
  const label = payload.label as Record<string, unknown> | undefined;
  if (!label || typeof label !== "object") return null;
  return {
    name: String(label.name || ""),
    color: String(label.color || ""),
  };
}

function getIssueTypeFromPayload(payload: Record<string, unknown>) {
  if (!payload || typeof payload !== "object") return null;
  const issueType = payload.issue_type as Record<string, unknown> | undefined;
  if (!issueType || typeof issueType !== "object") return null;
  return {
    name: String(issueType.name || ""),
    color: String(issueType.color || ""),
  };
}

function ColoredPill({ name, color }: { name: string; color: string }) {
  const bg = color ? `#${color}20` : "var(--muted)";
  const text = color ? `#${color}` : "var(--muted-foreground)";
  return (
    <span
      className="inline-flex items-center rounded-full border px-2 py-0.5 text-xs font-medium"
      style={{ backgroundColor: bg, borderColor: `${text}40`, color: text }}
    >
      {name}
    </span>
  );
}

function renderEventSummary(event: IssueTimelineEvent) {
  switch (event.event_type) {
    case "labeled": {
      const label = getLabelFromPayload(event.payload);
      if (label?.name) {
        return (
          <>
            添加了标签 <ColoredPill name={label.name} color={label.color} />
          </>
        );
      }
      if (event.summary && event.summary !== event.event_type) return event.summary;
      return "添加了标签";
    }
    case "unlabeled": {
      const label = getLabelFromPayload(event.payload);
      if (label?.name) {
        return (
          <>
            移除了标签 <ColoredPill name={label.name} color={label.color} />
          </>
        );
      }
      if (event.summary && event.summary !== event.event_type) return event.summary;
      return "移除了标签";
    }
    case "issue_type_added":
    case "added_type": {
      const type = getIssueTypeFromPayload(event.payload);
      if (type?.name) {
        return (
          <>
            添加了问题类型 <ColoredPill name={type.name} color={type.color} />
          </>
        );
      }
      if (event.summary && event.summary !== event.event_type) return event.summary;
      return "添加了问题类型";
    }
    case "issue_type_removed":
    case "removed_type": {
      const type = getIssueTypeFromPayload(event.payload);
      if (type?.name) {
        return (
          <>
            移除了问题类型 <ColoredPill name={type.name} color={type.color} />
          </>
        );
      }
      if (event.summary && event.summary !== event.event_type) return event.summary;
      return "移除了问题类型";
    }
    default:
      return event.summary || event.event_type;
  }
}

export function getCommentAnchorId(commentId: number) {
  return `issuecomment-${commentId}`;
}

export function getTimelineAnchorId(eventId: number) {
  return `issueevent-${eventId}`;
}

export function findPayloadHtmlUrl(value: unknown, depth = 0): string | null {
  if (depth > 4 || !value || typeof value !== "object") {
    return null;
  }

  if ("html_url" in value && typeof value.html_url === "string" && value.html_url) {
    return value.html_url;
  }

  if (Array.isArray(value)) {
    for (const item of value) {
      const found = findPayloadHtmlUrl(item, depth + 1);
      if (found) {
        return found;
      }
    }
    return null;
  }

  for (const nestedValue of Object.values(value)) {
    const found = findPayloadHtmlUrl(nestedValue, depth + 1);
    if (found) {
      return found;
    }
  }

  return null;
}

function EmptyState({ message }: { message: string }) {
  return (
    <div className="flex flex-col items-center justify-center rounded-xl border border-dashed py-14 text-center">
      <div className="mb-3 flex h-10 w-10 items-center justify-center rounded-full bg-muted">
        <MessageSquare className="h-5 w-5 text-muted-foreground" />
      </div>
      <p className="text-sm font-medium text-muted-foreground">{message}</p>
    </div>
  );
}

/** 评论 + 动态统一时间线：锚点、flash 高亮、无限滚动 sentinel 都在模块内。 */
export function IssueTimeline({
  items,
  isLoading,
  hasMore,
  isFetchingMore,
  onLoadMore,
  flashHighlightId,
  onNavigateToAnchor,
  onCopyGithubLink,
  fallbackGithubUrl = null,
}: IssueTimelineProps) {
  const token = useAuthStore((state) => state.token);
  const sentinelRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    if (!sentinelRef.current) {
      return;
    }
    if (typeof IntersectionObserver === "undefined") {
      return;
    }

    const observer = new IntersectionObserver(
      (entries) => {
        const entry = entries[0];
        if (entry?.isIntersecting && hasMore && !isFetchingMore) {
          onLoadMore();
        }
      },
      { rootMargin: "200px" }
    );

    observer.observe(sentinelRef.current);
    return () => observer.disconnect();
  }, [hasMore, isFetchingMore, onLoadMore]);

  return (
    <>
      {isLoading ? (
        <div className="relative space-y-6 pt-6">
          {Array.from({ length: 3 }).map((_, i) => (
            <Skeleton key={i} className="h-24 rounded-2xl" />
          ))}
        </div>
      ) : items.length === 0 ? (
        <div className="py-6">
          <EmptyState message="暂无评论和动态" />
        </div>
      ) : (
        <div className="relative space-y-0 pt-4">
          <div className="absolute bottom-0 left-4 top-0 w-px bg-border" />

          {items.map((item) =>
            item.type === "comment" ? (
              <div
                key={`comment-${item.data.id}`}
                id={getCommentAnchorId(item.data.github_comment_id)}
                className={cn(
                  "relative pb-6 scroll-mt-20 transition-all",
                  flashHighlightId === getCommentAnchorId(item.data.github_comment_id)
                    ? "ring-2 ring-primary/30 rounded-2xl"
                    : ""
                )}
              >
                <div className="group overflow-hidden rounded-2xl border bg-card transition-all hover:border-foreground/15 hover:shadow-sm">
                  <div className="flex items-center justify-between border-b bg-muted/30 px-4 py-3 md:px-5">
                    <div className="flex items-center gap-3">
                      <Avatar size="sm">
                        <AvatarImage src={toGitHubMediaProxyUrl(item.data.author.avatar_url, token)} alt={item.data.author.login} />
                        <AvatarFallback>{getInitials(item.data.author.login)}</AvatarFallback>
                      </Avatar>
                      <div className="flex flex-col">
                        <span className="text-sm font-medium">@{item.data.author.login}</span>
                        <span className="text-xs text-muted-foreground">{formatDate(item.data.created_at)}</span>
                      </div>
                    </div>
                    <div className="flex items-center gap-1 opacity-100 transition-opacity md:opacity-0 md:group-hover:opacity-100">
                      <Button
                        variant="ghost"
                        size="icon-xs"
                        aria-label={`定位到评论 ${item.data.github_comment_id}`}
                        onClick={() =>
                          onNavigateToAnchor(`#${getCommentAnchorId(item.data.github_comment_id)}`)
                        }
                      >
                        <Link2 className="h-3.5 w-3.5" />
                      </Button>
                      {item.data.source === "github" && (
                        <Button
                          variant="ghost"
                          size="icon-xs"
                          aria-label={`复制评论 ${item.data.github_comment_id} 的 GitHub 深链接`}
                          onClick={() =>
                            onCopyGithubLink(item.data.html_url, "已复制 GitHub 深链接")
                          }
                        >
                          <ExternalLink className="h-3.5 w-3.5" />
                        </Button>
                      )}
                    </div>
                  </div>
                  <div className="px-4 py-4 md:px-5 md:py-5">
                    <GitHubContent
                      className="markdown-body"
                      html={item.data.body_html}
                      markdown={item.data.body || "_空评论_"}
                    />
                  </div>
                </div>
              </div>
            ) : (
              <div
                key={`event-${item.data.id}`}
                id={getTimelineAnchorId(item.data.github_event_id)}
                className={cn(
                  "group relative flex items-start gap-3 pb-4 scroll-mt-20 md:gap-4",
                  flashHighlightId === getTimelineAnchorId(item.data.github_event_id)
                    ? "rounded-lg bg-primary/5 py-1"
                    : ""
                )}
              >
                {/* Timeline badge - centered on the vertical line */}
                <div className="absolute left-4 top-0 flex shrink-0 -translate-x-1/2">
                  <div className="flex h-8 w-8 items-center justify-center rounded-full border bg-background shadow-sm">
                    {(() => {
                      const Icon = getEventIcon(item.data.event_type);
                      return <Icon className="h-3.5 w-3.5 text-muted-foreground" />;
                    })()}
                  </div>
                </div>

                <div className="min-w-0 flex-1 pl-8 pt-1.5">
                  <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                    <span className="text-sm font-semibold text-foreground">
                      {item.data.actor.login || "GitHub"}
                    </span>
                    <span className="text-sm text-muted-foreground">{renderEventSummary(item.data)}</span>
                    <span className="text-xs text-muted-foreground/80">
                      {formatRelativeTime(item.data.created_at)}
                    </span>
                  </div>

                  {item.data.body && (
                    <p className="mt-1 text-sm text-muted-foreground line-clamp-3">{item.data.body}</p>
                  )}

                  <div className="mt-1 flex items-center gap-2 opacity-100 transition-opacity md:opacity-0 md:group-hover:opacity-100">
                    <Button
                      variant="ghost"
                      size="icon-xs"
                      aria-label={`定位到动态 ${item.data.github_event_id}`}
                      onClick={() =>
                        onNavigateToAnchor(`#${getTimelineAnchorId(item.data.github_event_id)}`)
                      }
                    >
                      <Link2 className="h-3.5 w-3.5" />
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon-xs"
                      aria-label={`复制动态 ${item.data.github_event_id} 的 GitHub 深链接`}
                      onClick={() => {
                        const eventHtmlUrl = findPayloadHtmlUrl(item.data.payload);
                        onCopyGithubLink(
                          eventHtmlUrl || fallbackGithubUrl,
                          eventHtmlUrl
                            ? "已复制 GitHub 深链接"
                            : "当前动态没有精确 GitHub 链接，已复制问题链接",
                        );
                      }}
                    >
                      <ExternalLink className="h-3.5 w-3.5" />
                    </Button>
                  </div>
                </div>
              </div>
            )
          )}
        </div>
      )}

      {items.length > 0 && hasMore && (
        <div ref={sentinelRef} className="h-1 w-full" />
      )}
    </>
  );
}

/** 评论发布卡片：draft 与提交 pending 态都在模块内，发布成功后清空输入。 */
export function IssueCommentComposer({
  isInternalIssue,
  onSubmit,
}: IssueCommentComposerProps) {
  const [draft, setDraft] = useState("");
  const [isSubmitting, setIsSubmitting] = useState(false);

  const handleSubmit = async () => {
    if (!draft.trim()) {
      toast.error("请输入评论内容");
      return;
    }
    setIsSubmitting(true);
    try {
      const success = await onSubmit(draft);
      if (success) {
        setDraft("");
      }
    } finally {
      setIsSubmitting(false);
    }
  };

  return (
    <Card>
      <CardContent className="space-y-4 p-5 md:p-6">
        <div className="flex items-center justify-between gap-3">
          <div>
            <h2 className="text-sm font-semibold">{isInternalIssue ? "内部评论" : "添加评论"}</h2>
            <p className="text-sm text-muted-foreground">
              {isInternalIssue ? "评论仅保存在 Fast Ship 内部。" : "评论会直接发布到 GitHub Issue。"}
            </p>
          </div>
        </div>
        <div className="space-y-3">
          <Textarea
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
            rows={5}
            placeholder="使用 Markdown 输入评论内容"
          />
          <div className="flex justify-end">
            <Button
              onClick={() => void handleSubmit()}
              disabled={isSubmitting}
            >
              {isSubmitting ? "发布中..." : "发布评论"}
            </Button>
          </div>
        </div>
      </CardContent>
    </Card>
  );
}
