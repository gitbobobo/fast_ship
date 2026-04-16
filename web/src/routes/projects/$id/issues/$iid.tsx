import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useLocation, useNavigate, useParams, useSearchParams } from "react-router";
import {
  ArrowLeft,
  CheckCircle2,
  ChevronLeft,
  ChevronRight,
  Circle,
  CircleDot,
  Copy,
  Ellipsis,
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
  RefreshCw,
  Tag,
  Unlock,
  User,
} from "lucide-react";
import { GitHubContent } from "@/components/github-content";
import { Header } from "@/components/layout/header";
import { Button } from "@/components/ui/button";

import { Card, CardContent } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
} from "@/components/ui/dialog";

import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
  DropdownMenuSeparator,
} from "@/components/ui/dropdown-menu";
import { Skeleton } from "@/components/ui/skeleton";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { Separator } from "@/components/ui/separator";
import {
  useIssue,
  useInfiniteIssueComments,
  useInfiniteIssueTimeline,
  useIssues,
  useSyncProjectIssues,
} from "@/lib/hooks/use-issues";
import { readIssueDetailContext } from "@/lib/issue-list-context";
import { cn } from "@/lib/utils";
import { formatDate, formatRelativeTime } from "@/lib/utils/format";
import { toast } from "sonner";

function getInitials(login: string) {
  return login.slice(0, 2).toUpperCase();
}

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

function getCommentAnchorId(commentId: number) {
  return `issuecomment-${commentId}`;
}

function getTimelineAnchorId(eventId: number) {
  return `issueevent-${eventId}`;
}

function findPayloadHtmlUrl(value: unknown, depth = 0): string | null {
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

function StateBadge({ state }: { state: "open" | "closed" }) {
  const isOpen = state === "open";
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-xs font-semibold uppercase tracking-wide",
        isOpen
          ? "border-emerald-500/20 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400"
          : "border-rose-500/20 bg-rose-500/10 text-rose-600 dark:text-rose-400"
      )}
    >
      {isOpen ? <Circle className="h-3 w-3 fill-current" /> : <CheckCircle2 className="h-3 w-3" />}
      {isOpen ? "Open" : "Closed"}
    </span>
  );
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

type TimelineItem =
  | { type: "comment"; data: IssueComment; created_at: string }
  | { type: "event"; data: IssueTimelineEvent; created_at: string };

export default function IssueDetailPage() {
  const { id, iid } = useParams();
  const location = useLocation();
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const issueContext = readIssueDetailContext(searchParams);
  const commentsPage = Math.max(Number(searchParams.get("comments_page") ?? "1") || 1, 1);
  const timelinePage = Math.max(Number(searchParams.get("timeline_page") ?? "1") || 1, 1);
  const isCommentAnchorHash = location.hash.startsWith("#issuecomment-");
  const isTimelineAnchorHash = location.hash.startsWith("#issueevent-");
  const [flashHighlightId, setFlashHighlightId] = useState<string | null>(
    isCommentAnchorHash || isTimelineAnchorHash ? location.hash.slice(1) : null
  );
  const [lightboxOpen, setLightboxOpen] = useState(false);
  const [lightboxImage, setLightboxImage] = useState<{ src: string; alt: string } | null>(null);
  const sentinelRef = useRef<HTMLDivElement | null>(null);
  const timelineSectionRef = useRef<HTMLDivElement | null>(null);

  const handleImageClick = useCallback((e: React.MouseEvent) => {
    const target = e.target as HTMLElement;
    if (target.tagName === "IMG") {
      const img = target as HTMLImageElement;
      setLightboxImage({ src: img.src, alt: img.alt });
      setLightboxOpen(true);
    }
  }, []);

  const { data: issue, isLoading } = useIssue(iid!);
  const { data: issueListData } = useIssues(id!, {
    state: issueContext.state === "all" ? undefined : issueContext.state || undefined,
    q: issueContext.q || undefined,
    label: issueContext.label === "all" ? undefined : issueContext.label || undefined,
    assignee: issueContext.assignee === "all" ? undefined : issueContext.assignee || undefined,
    milestone: issueContext.milestone === "all" ? undefined : issueContext.milestone || undefined,
    sort: issueContext.sort,
    page: issueContext.page,
    page_size: 20,
  });
  const { data: prevPageData } = useIssues(id!, {
    state: issueContext.state === "all" ? undefined : issueContext.state || undefined,
    q: issueContext.q || undefined,
    label: issueContext.label === "all" ? undefined : issueContext.label || undefined,
    assignee: issueContext.assignee === "all" ? undefined : issueContext.assignee || undefined,
    milestone: issueContext.milestone === "all" ? undefined : issueContext.milestone || undefined,
    sort: issueContext.sort,
    page: issueContext.page > 1 ? issueContext.page - 1 : 1,
    page_size: 20,
  });
  const { data: nextPageData } = useIssues(id!, {
    state: issueContext.state === "all" ? undefined : issueContext.state || undefined,
    q: issueContext.q || undefined,
    label: issueContext.label === "all" ? undefined : issueContext.label || undefined,
    assignee: issueContext.assignee === "all" ? undefined : issueContext.assignee || undefined,
    milestone: issueContext.milestone === "all" ? undefined : issueContext.milestone || undefined,
    sort: issueContext.sort,
    page: issueContext.page + 1,
    page_size: 20,
  });
  const {
    data: infiniteCommentsData,
    fetchNextPage: fetchNextCommentsPage,
    hasNextPage: hasNextCommentsPage,
    isFetchingNextPage: isFetchingNextCommentsPage,
    isLoading: commentsLoading,
  } = useInfiniteIssueComments(iid!, 20);
  const {
    data: infiniteTimelineData,
    fetchNextPage: fetchNextTimelinePage,
    hasNextPage: hasNextTimelinePage,
    isFetchingNextPage: isFetchingNextTimelinePage,
    isLoading: timelineLoading,
  } = useInfiniteIssueTimeline(iid!, 20);
  const syncIssues = useSyncProjectIssues(id!);

  const pageIssues = issueListData?.items ?? [];
  const issueIndex = pageIssues.findIndex((item) => item.id === iid);
  const currentTotalPages = Math.max(Math.ceil((issueListData?.total ?? 0) / (issueListData?.page_size ?? 20)), 1);
  const previousIssue =
    issueIndex > 0
      ? pageIssues[issueIndex - 1]
      : issueContext.page > 1
        ? (prevPageData?.items ?? []).at(-1) ?? null
        : null;
  const nextIssue =
    issueIndex >= 0 && issueIndex < pageIssues.length - 1
      ? pageIssues[issueIndex + 1]
      : issueContext.page < currentTotalPages
        ? (nextPageData?.items ?? [])[0] ?? null
        : null;

  const comments = infiniteCommentsData?.pages.flatMap((page) => page.items) ?? [];
  const timeline = infiniteTimelineData?.pages.flatMap((page) => page.items) ?? [];
  const commentsTotal = infiniteCommentsData?.pages[0]?.total ?? 0;
  const timelineTotal = infiniteTimelineData?.pages[0]?.total ?? 0;
  const loadedCommentsPages = infiniteCommentsData?.pages.length ?? 1;
  const loadedTimelinePages = infiniteTimelineData?.pages.length ?? 1;
  const targetCommentAnchorId = isCommentAnchorHash ? location.hash.slice(1) : null;
  const targetTimelineAnchorId = isTimelineAnchorHash ? location.hash.slice(1) : null;
  const activeComment = targetCommentAnchorId
    ? comments.find((comment) => getCommentAnchorId(comment.github_comment_id) === targetCommentAnchorId)
    : null;
  const activeTimelineEvent = targetTimelineAnchorId
    ? timeline.find((event) => getTimelineAnchorId(event.github_event_id) === targetTimelineAnchorId)
    : null;

  const timelineItems: TimelineItem[] = useMemo(() => {
    const items: TimelineItem[] = [
      ...comments.map((c) => ({ type: "comment" as const, data: c, created_at: c.created_at })),
      ...timeline.map((e) => ({ type: "event" as const, data: e, created_at: e.created_at })),
    ];
    items.sort((a, b) => new Date(a.created_at).getTime() - new Date(b.created_at).getTime());
    return items;
  }, [comments, timeline]);

  const totalLoaded = comments.length + timeline.length;
  const totalItems = commentsTotal + timelineTotal;
  const hasMore = hasNextCommentsPage || hasNextTimelinePage;
  const isLoadingMore = isFetchingNextCommentsPage || isFetchingNextTimelinePage;
  const isLoadingTimeline = commentsLoading || timelineLoading;

  const updateSearchParam = useCallback(
    (key: string, value: number) => {
      const next = new URLSearchParams(searchParams);
      if (value <= 1) {
        next.delete(key);
      } else {
        next.set(key, String(value));
      }
      setSearchParams(next, { replace: true });
    },
    [searchParams, setSearchParams]
  );

  const setAnchor = (hash: string) => {
    navigate(
      {
        pathname: location.pathname,
        search: location.search,
        hash,
      },
      { replace: true }
    );
  };

  useEffect(() => {
    if (!iid || !hasNextCommentsPage || loadedCommentsPages >= commentsPage) {
      return;
    }
    void fetchNextCommentsPage();
  }, [commentsPage, fetchNextCommentsPage, hasNextCommentsPage, iid, loadedCommentsPages]);

  useEffect(() => {
    if (!iid || !hasNextTimelinePage || loadedTimelinePages >= timelinePage) {
      return;
    }
    void fetchNextTimelinePage();
  }, [fetchNextTimelinePage, hasNextTimelinePage, iid, loadedTimelinePages, timelinePage]);

  useEffect(() => {
    if (loadedCommentsPages > commentsPage) {
      updateSearchParam("comments_page", loadedCommentsPages);
    }
  }, [commentsPage, loadedCommentsPages, updateSearchParam]);

  useEffect(() => {
    if (loadedTimelinePages > timelinePage) {
      updateSearchParam("timeline_page", loadedTimelinePages);
    }
  }, [loadedTimelinePages, timelinePage, updateSearchParam]);

  useEffect(() => {
    if (!targetCommentAnchorId) {
      return;
    }
    if (typeof document !== "undefined" && document.getElementById(targetCommentAnchorId)) {
      return;
    }
    if (!hasNextCommentsPage || isFetchingNextCommentsPage) {
      return;
    }
    void fetchNextCommentsPage();
  }, [fetchNextCommentsPage, hasNextCommentsPage, isFetchingNextCommentsPage, loadedCommentsPages, targetCommentAnchorId]);

  useEffect(() => {
    if (!targetTimelineAnchorId) {
      return;
    }
    if (typeof document !== "undefined" && document.getElementById(targetTimelineAnchorId)) {
      return;
    }
    if (!hasNextTimelinePage || isFetchingNextTimelinePage) {
      return;
    }
    void fetchNextTimelinePage();
  }, [
    fetchNextTimelinePage,
    hasNextTimelinePage,
    isFetchingNextTimelinePage,
    loadedTimelinePages,
    targetTimelineAnchorId,
  ]);

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
        if (entry?.isIntersecting) {
          if (hasNextCommentsPage && !isFetchingNextCommentsPage) {
            void fetchNextCommentsPage();
          }
          if (hasNextTimelinePage && !isFetchingNextTimelinePage) {
            void fetchNextTimelinePage();
          }
        }
      },
      { rootMargin: "200px" }
    );

    observer.observe(sentinelRef.current);
    return () => observer.disconnect();
  }, [
    fetchNextCommentsPage,
    fetchNextTimelinePage,
    hasNextCommentsPage,
    hasNextTimelinePage,
    isFetchingNextCommentsPage,
    isFetchingNextTimelinePage,
  ]);

  useEffect(() => {
    if (!location.hash) {
      queueMicrotask(() => setFlashHighlightId(null));
      return;
    }

    const currentAnchorId =
      location.hash.startsWith("#issuecomment-") || location.hash.startsWith("#issueevent-")
        ? location.hash.slice(1)
        : null;
    const targetElement =
      location.hash === "#timeline"
        ? timelineSectionRef.current
        : location.hash.startsWith("#issuecomment-") || location.hash.startsWith("#issueevent-")
          ? document.getElementById(location.hash.slice(1))
          : null;
    if (!targetElement) {
      return;
    }

    if (currentAnchorId) {
      queueMicrotask(() => setFlashHighlightId(currentAnchorId));
    } else {
      queueMicrotask(() => setFlashHighlightId(null));
    }

    const frame = window.requestAnimationFrame(() => {
      targetElement.scrollIntoView({ block: "start", behavior: "smooth" });
    });

    const timeout = currentAnchorId
      ? window.setTimeout(() => {
          setFlashHighlightId((activeId) => (activeId === currentAnchorId ? null : activeId));
        }, 2200)
      : null;

    return () => {
      window.cancelAnimationFrame(frame);
      if (timeout !== null) {
        window.clearTimeout(timeout);
      }
    };
  }, [loadedCommentsPages, loadedTimelinePages, location.hash]);

  const navigateToIssue = (targetIssueId: string) => {
    navigate({
      pathname: `/projects/${id}/issues/${targetIssueId}`,
      search: location.search,
    });
  };

  const handleSync = async () => {
    try {
      const res = await syncIssues.mutateAsync();
      toast.success(`同步完成：${res.data.synced_issue_count} 个问题`);
    } catch {
      toast.error("同步失败");
    }
  };

  const handleCopyCurrentViewLink = async () => {
    try {
      const currentUrl = new URL(`${location.pathname}${location.search}${location.hash}`, window.location.origin).toString();
      await navigator.clipboard.writeText(currentUrl);
      toast.success("已复制当前问题视图链接");
    } catch {
      toast.error("复制失败");
    }
  };

  const copyGitHubUrl = async (url: string | null | undefined, successMessage: string) => {
    if (!url) {
      toast.error("复制失败");
      return;
    }
    try {
      await navigator.clipboard.writeText(url);
      toast.success(successMessage);
    } catch {
      toast.error("复制失败");
    }
  };

  const handleCopyGitHubLink = async () => {
    const hash = location.hash;
    let targetUrl = issue?.html_url;
    let successMessage = "已复制 GitHub 深链接";

    if (hash.startsWith("#issuecomment-")) {
      const comment = comments.find((c) => getCommentAnchorId(c.github_comment_id) === hash.slice(1));
      targetUrl = comment?.html_url || issue?.html_url;
    } else if (hash.startsWith("#issueevent-")) {
      const event = timeline.find((e) => getTimelineAnchorId(e.github_event_id) === hash.slice(1));
      const eventHtmlUrl = event ? findPayloadHtmlUrl(event.payload) : null;
      targetUrl = eventHtmlUrl || issue?.html_url;
      successMessage = eventHtmlUrl ? "已复制 GitHub 深链接" : "当前动态没有精确 GitHub 链接，已复制问题链接";
    }

    await copyGitHubUrl(targetUrl, successMessage);
  };

  const handleCopyCommentGitHubLink = async (comment: IssueComment) => {
    await copyGitHubUrl(comment.html_url, "已复制 GitHub 深链接");
  };

  const handleCopyTimelineGitHubLink = async (event: IssueTimelineEvent) => {
    const eventHtmlUrl = findPayloadHtmlUrl(event.payload);
    const targetUrl = eventHtmlUrl || issue?.html_url;
    const successMessage = eventHtmlUrl ? "已复制 GitHub 深链接" : "当前动态没有精确 GitHub 链接，已复制问题链接";
    await copyGitHubUrl(targetUrl, successMessage);
  };

  if (isLoading) {
    return (
      <>
        <Header title="问题详情" />
        <div className="mx-auto max-w-7xl p-4 md:p-6">
          <div className="grid gap-6 lg:grid-cols-[1fr_320px]">
            <div className="space-y-4">
              <Skeleton className="h-40 rounded-2xl" />
              <Skeleton className="h-96 rounded-2xl" />
            </div>
            <div className="space-y-4">
              <Skeleton className="h-64 rounded-2xl" />
              <Skeleton className="h-40 rounded-2xl" />
            </div>
          </div>
        </div>
      </>
    );
  }

  if (!issue) {
    return (
      <>
        <Header title="问题详情" />
        <div className="mx-auto max-w-7xl p-4 md:p-6">
          <div className="flex flex-col items-center justify-center rounded-2xl border border-dashed py-20 text-center">
            <div className="mb-4 flex h-14 w-14 items-center justify-center rounded-full bg-muted">
              <MessageSquare className="h-6 w-6 text-muted-foreground" />
            </div>
            <h2 className="text-lg font-semibold">问题不存在</h2>
            <p className="mt-1 text-sm text-muted-foreground">该问题可能已被删除或您没有访问权限</p>
          </div>
        </div>
      </>
    );
  }

  return (
    <>
      <Header title={`#${issue.number}`} />
      <div className="mx-auto max-w-7xl p-4 md:p-6">
        {/* Top Navigation Bar */}
        <div className="mb-6 flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <Button
            variant="outline"
            size="sm"
            className="w-fit"
            onClick={() => navigate(-1)}
          >
            <ArrowLeft className="mr-1.5 h-3.5 w-3.5" />
            返回
          </Button>

          <div className="flex flex-wrap items-center gap-2">
            <div className="mr-2 flex items-center gap-1">
              <Button
                variant="outline"
                size="sm"
                disabled={!previousIssue}
                onClick={() => previousIssue && navigateToIssue(previousIssue.id)}
                title={previousIssue ? `#${previousIssue.number}` : "没有上一条"}
              >
                <ChevronLeft className="mr-1.5 h-3.5 w-3.5" />
                上一条
              </Button>
              <Button
                variant="outline"
                size="sm"
                disabled={!nextIssue}
                onClick={() => nextIssue && navigateToIssue(nextIssue.id)}
                title={nextIssue ? `#${nextIssue.number}` : "没有下一条"}
              >
                下一条
                <ChevronRight className="ml-1.5 h-3.5 w-3.5" />
              </Button>
            </div>

            <DropdownMenu>
              <DropdownMenuTrigger
                render={
                  <Button variant="outline" size="icon-sm" aria-label="更多">
                    <Ellipsis className="h-4 w-4" />
                  </Button>
                }
              />
              <DropdownMenuContent align="end" className="w-44">
                <DropdownMenuItem onClick={() => void handleCopyCurrentViewLink()}>
                  <Copy className="h-4 w-4" />
                  复制链接
                </DropdownMenuItem>
                <DropdownMenuItem onClick={() => void handleCopyGitHubLink()}>
                  <Link2 className="h-4 w-4" />
                  复制 GitHub 深链接
                </DropdownMenuItem>
                <DropdownMenuSeparator />
                <DropdownMenuItem
                  onClick={handleSync}
                  disabled={syncIssues.isPending}
                >
                  <RefreshCw className={cn("h-4 w-4", syncIssues.isPending && "animate-spin")} />
                  {syncIssues.isPending ? "同步中..." : "重新同步"}
                </DropdownMenuItem>
                <DropdownMenuSeparator />
                <DropdownMenuItem
                  render={
                    <a
                      href={issue.html_url}
                      target="_blank"
                      rel="noopener noreferrer"
                    />
                  }
                >
                  <ExternalLink className="h-4 w-4" />
                  在 GitHub 查看
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        </div>

        <div className="grid gap-6 lg:grid-cols-[1fr_300px] xl:grid-cols-[1fr_340px]">
          {/* Main Content */}
          <div className="min-w-0 space-y-6 [&_img]:cursor-zoom-in" onClick={handleImageClick}>
            {/* Issue Header Card */}
            <div className="overflow-hidden rounded-2xl border bg-card shadow-sm transition-shadow hover:shadow-md">
              <div className="p-5 md:p-6">
                <div className="flex flex-wrap items-start gap-3">
                  <StateBadge state={issue.state} />
                  {issue.labels.map((label) => (
                    <span
                      key={label.name}
                      className="inline-flex items-center rounded-full border px-2.5 py-0.5 text-xs font-medium"
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

                <h1 className="mt-4 text-xl font-semibold leading-snug text-foreground md:text-2xl">
                  #{issue.number} {issue.title}
                </h1>

                <div className="mt-4 flex flex-wrap items-center gap-4 text-sm text-muted-foreground">
                  <div className="flex items-center gap-2">
                    <Avatar size="sm">
                      <AvatarImage src={issue.author.avatar_url} alt={issue.author.login} />
                      <AvatarFallback>{getInitials(issue.author.login)}</AvatarFallback>
                    </Avatar>
                    <span className="font-medium text-foreground">@{issue.author.login}</span>
                    <span>创建于 {formatDate(issue.created_at)}</span>
                  </div>
                  <span className="hidden sm:inline">·</span>
                  <span>更新于 {formatRelativeTime(issue.updated_at)}</span>
                </div>
              </div>

              <Separator />

              <div className="p-5 md:p-6">
                {issue.body || issue.body_html ? (
                  <div className="markdown-body">
                    <GitHubContent html={issue.body_html} markdown={issue.body} />
                  </div>
                ) : (
                  <div className="flex items-center gap-2 text-sm italic text-muted-foreground">
                    <MessageSquare className="h-4 w-4" />
                    暂无描述
                  </div>
                )}
              </div>
            </div>

            {/* Unified Timeline */}
            <div className="-mt-6">
              <div id="timeline" ref={timelineSectionRef} className="scroll-mt-20" />

              {isLoadingTimeline ? (
                <div className="relative space-y-6 pt-6">
                  {Array.from({ length: 3 }).map((_, i) => (
                    <Skeleton key={i} className="h-24 rounded-2xl" />
                  ))}
                </div>
              ) : timelineItems.length === 0 ? (
                <div className="pt-6">
                  <EmptyState message="暂无评论和动态" />
                </div>
              ) : (
                <div className="relative space-y-0 pt-4">
                  {/* Timeline connector line */}
                  <div className="absolute bottom-0 left-4 top-0 w-px bg-border" />

                  {timelineItems.map((item) =>
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
                                <AvatarImage src={item.data.author.avatar_url} alt={item.data.author.login} />
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
                                  setAnchor(`#${getCommentAnchorId(item.data.github_comment_id)}`)
                                }
                              >
                                <Link2 className="h-3.5 w-3.5" />
                              </Button>
                              <Button
                                variant="ghost"
                                size="icon-xs"
                                aria-label={`复制评论 ${item.data.github_comment_id} 的 GitHub 深链接`}
                                onClick={() => void handleCopyCommentGitHubLink(item.data)}
                              >
                                <ExternalLink className="h-3.5 w-3.5" />
                              </Button>
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
                                setAnchor(`#${getTimelineAnchorId(item.data.github_event_id)}`)
                              }
                            >
                              <Link2 className="h-3.5 w-3.5" />
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon-xs"
                              aria-label={`复制动态 ${item.data.github_event_id} 的 GitHub 深链接`}
                              onClick={() => void handleCopyTimelineGitHubLink(item.data)}
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

              {timelineItems.length > 0 && (
                <div className="mt-6 space-y-3">
                  <p className="text-center text-xs text-muted-foreground">
                    已加载 {Math.min(totalLoaded, totalItems)} / {totalItems} 条活动
                  </p>
                  <div ref={sentinelRef} className="h-1 w-full" />
                  {hasMore ? (
                    <div className="flex items-center justify-center">
                      <Button
                        variant="outline"
                        size="sm"
                        className="min-w-[140px]"
                        onClick={() => {
                          if (hasNextCommentsPage && !isFetchingNextCommentsPage) {
                            void fetchNextCommentsPage();
                          }
                          if (hasNextTimelinePage && !isFetchingNextTimelinePage) {
                            void fetchNextTimelinePage();
                          }
                        }}
                        disabled={isLoadingMore}
                      >
                        {isLoadingMore ? "加载中..." : "加载更多"}
                      </Button>
                    </div>
                  ) : (
                    <p className="text-center text-xs text-muted-foreground">活动已全部加载</p>
                  )}
                </div>
              )}
            </div>
          </div>

          {/* Sidebar */}
          <aside className="space-y-4 lg:sticky lg:top-20 lg:self-start">
            <Card>
              <CardContent className="space-y-5 p-5">
                {/* 创建者 */}
                <div className="flex items-center gap-3">
                  <Avatar size="lg" className="h-12 w-12">
                    <AvatarImage src={issue.author.avatar_url} alt={issue.author.login} />
                    <AvatarFallback>{getInitials(issue.author.login)}</AvatarFallback>
                  </Avatar>
                  <div className="min-w-0">
                    <p className="text-sm font-medium text-muted-foreground">创建者</p>
                    <p className="truncate text-base font-semibold">@{issue.author.login}</p>
                  </div>
                </div>

                {/* 负责人 & 里程碑 */}
                {(issue.assignees.length > 0 || issue.milestone) && (
                  <div className="space-y-3">
                    {issue.assignees.length > 0 && (
                      <div>
                        <p className="mb-1.5 text-xs font-medium text-muted-foreground">负责人</p>
                        <div className="flex flex-wrap items-center gap-2">
                          {issue.assignees.map((assignee) => (
                            <div
                              key={assignee.login}
                              className="inline-flex items-center gap-1.5 rounded-full border px-2 py-1 text-sm"
                            >
                              <Avatar size="sm">
                                <AvatarImage src={assignee.avatar_url} alt={assignee.login} />
                                <AvatarFallback>{getInitials(assignee.login)}</AvatarFallback>
                              </Avatar>
                              <span className="font-medium">@{assignee.login}</span>
                            </div>
                          ))}
                        </div>
                      </div>
                    )}
                    {issue.milestone && (
                      <div>
                        <p className="mb-1 text-xs font-medium text-muted-foreground">里程碑</p>
                        <p className="text-sm font-semibold">{issue.milestone.title}</p>
                      </div>
                    )}
                  </div>
                )}

                {/* 时间 */}
                <div className="space-y-2">
                  <div className="flex items-center justify-between gap-2">
                    <span className="text-xs text-muted-foreground">创建于</span>
                    <span className="text-sm font-medium">{formatDate(issue.created_at)}</span>
                  </div>
                  <div className="flex items-center justify-between gap-2">
                    <span className="text-xs text-muted-foreground">更新于</span>
                    <span className="text-sm font-medium">{formatRelativeTime(issue.updated_at)}</span>
                  </div>
                  <div className="flex items-center justify-between gap-2">
                    <span className="text-xs text-muted-foreground">同步于</span>
                    <span className="text-sm font-medium">{formatRelativeTime(issue.synced_at)}</span>
                  </div>
                </div>

                {/* 标签 */}
                {issue.labels.length > 0 && (
                  <div>
                    <p className="mb-2 text-xs font-medium text-muted-foreground">标签</p>
                    <div className="flex flex-wrap gap-2">
                      {issue.labels.map((label) => (
                        <span
                          key={label.name}
                          className="inline-flex items-center rounded-full px-2.5 py-1 text-xs font-medium"
                          style={{
                            backgroundColor: `#${label.color}20`,
                            color: `#${label.color}`,
                          }}
                        >
                          {label.name}
                        </span>
                      ))}
                    </div>
                  </div>
                )}
              </CardContent>
            </Card>

          </aside>
        </div>
      </div>

      {/* Image Lightbox */}
      <Dialog open={lightboxOpen} onOpenChange={setLightboxOpen} dismissible>
        <DialogContent
          className="max-w-[90vw] place-items-center border-none bg-transparent p-0 shadow-none ring-0 sm:max-w-[90vw]"
          overlayClassName="bg-black/80 supports-backdrop-filter:backdrop-blur-sm"
          showCloseButton={false}
          onClick={() => setLightboxOpen(false)}
        >
          {lightboxImage && (
            <img
              src={lightboxImage.src}
              alt={lightboxImage.alt}
              className="max-h-[85vh] max-w-full rounded-lg object-contain"
            />
          )}
        </DialogContent>
      </Dialog>
    </>
  );
}
