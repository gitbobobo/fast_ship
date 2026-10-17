import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useLocation, useNavigate, useParams, useSearchParams } from "react-router";
import {
  CheckCircle2,
  ChevronLeft,
  ChevronRight,
  Circle,
  Copy,
  Ellipsis,
  ExternalLink,
  FileText,
  Inbox,
  Lightbulb,
  Link2,
  Loader2,
  MessageSquare,
  Pencil,
  PanelLeft,
  Play,
  RefreshCw,
} from "lucide-react";
import { GitHubContent } from "@/components/github-content";
import { CollaborationArea } from "@/components/issues/collaboration-area";
import { LaunchIssueThreadDialog } from "@/components/issues/launch-issue-thread-dialog";
import { IssueAttachmentsCard } from "@/components/issues/issue-attachments-card";
import { IssuePullRequestsCard } from "@/components/issues/issue-pull-requests-card";
import { IssueScreenshotAnnotationsCard } from "@/components/issues/issue-screenshot-annotations-card";
import { IssueShipHookCard } from "@/components/issues/issue-ship-hook-card";
import { IssueWorkflowStatusBadge } from "@/components/issues/issue-workflow-status-badge";
import { Header } from "@/components/layout/header";
import { HeaderActions } from "@/components/layout/header-actions";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent } from "@/components/ui/dialog";

import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
  DropdownMenuSeparator,
} from "@/components/ui/dropdown-menu";
import {
  OptionSelect,
  withMissingOption,
} from "@/components/ui/option-select";
import { Skeleton } from "@/components/ui/skeleton";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { Separator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import {
  useIssue,
  useIssueRepoLabels,
  useCreateIssueComment,
  useInfiniteIssueComments,
  useInfiniteIssueTimeline,
  useIssues,
  useMarkIssueReadWhenLoaded,
  useSyncProjectIssues,
  useUpdateIssue,
  useReplaceIssueChecklist,
  useUpdateIssueInternalMeta,
} from "@/lib/hooks/use-issues";
import { useProject } from "@/lib/hooks/use-projects";
import { useRecommendations } from "@/lib/hooks/use-recommendations";
import { useIssueChecklistSuggestions } from "@/lib/hooks/use-ai";
import {
  ISSUE_WORKFLOW_STATUS_SELECT_OPTIONS,
  type IssueWorkflowStatus,
} from "@/lib/issue-workflow-status";
import { useIssuePromptList } from "@/lib/hooks/use-issue-prompt";
import { useEnvironments } from "@/lib/hooks/use-threads";
import { buildIssuePrompt } from "@/lib/issue-prompt";
import { canOperate } from "@/lib/threads-compose";
import { readIssueDetailContext } from "@/lib/issue-list-context";
import { ISSUE_SOURCE_LABELS } from "@/lib/issue-source";
import { useAuthStore } from "@/lib/store/auth-store";
import {
  containsLocalIssueAssetReference,
  GITHUB_LOCAL_ASSET_NOTICE,
  toGitHubMediaProxyUrl,
} from "@/lib/utils/github-media-proxy";
import { cn, getInitials } from "@/lib/utils";
import { copyWithToast } from "@/lib/copy";
import { formatDate, formatRelativeTime } from "@/lib/utils/format";
import { toast } from "sonner";
import {
  IssueCommentComposer,
  IssueTimeline,
  findPayloadHtmlUrl,
  getCommentAnchorId,
  getTimelineAnchorId,
  type IssueTimelineItem,
} from "./components/issue-timeline";
import {
  IssueChecklistCard,
  type IssueChecklistDraftItem,
} from "./components/issue-checklist-card";
import { IssueRecommendationBanner } from "./components/issue-recommendation-banner";

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

function ProgressBadge({ progress }: { progress?: number | null }) {
  if (progress == null) {
    return null;
  }

  const className =
    progress >= 100
      ? "border-emerald-500/20 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300"
      : progress > 0
        ? "border-amber-500/20 bg-amber-500/10 text-amber-700 dark:text-amber-300"
        : "border-slate-500/20 bg-slate-500/10 text-slate-600 dark:text-slate-300";

  return (
    <span
      className={cn(
        "inline-flex items-center rounded-full border px-2.5 py-1 text-xs font-semibold",
        className,
      )}
    >
      进度 {progress}%
    </span>
  );
}

function normalizeLabelNames(labels: string[]) {
  const seen = new Set<string>();
  const normalized: string[] = [];
  for (const label of labels) {
    const value = label.trim();
    if (!value) continue;
    const key = value.toLowerCase();
    if (seen.has(key)) continue;
    seen.add(key);
    normalized.push(value);
  }
  return normalized;
}

function haveSameLabelNames(left: string[], right: string[]) {
  const leftNames = normalizeLabelNames(left);
  const rightKeys = new Set(
    normalizeLabelNames(right).map((label) => label.toLowerCase()),
  );

  return (
    leftNames.length === rightKeys.size &&
    leftNames.every((label) => rightKeys.has(label.toLowerCase()))
  );
}

function toggleLabelName(labels: string[], labelName: string) {
  const key = labelName.toLowerCase();
  const exists = labels.some((label) => label.toLowerCase() === key);

  return exists
    ? labels.filter((label) => label.toLowerCase() !== key)
    : [...labels, labelName];
}

export default function IssueDetailPage() {
  const token = useAuthStore((state) => state.token);
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
  const [isMobileSidebarOpen, setIsMobileSidebarOpen] = useState(false);
  const [isTitleEditing, setIsTitleEditing] = useState(false);
  const [titleDraft, setTitleDraft] = useState("");
  const [labelDraft, setLabelDraft] = useState<string[] | null>(null);
  const [launchDialogOpen, setLaunchDialogOpen] = useState(false);
  const issuePrompts = useIssuePromptList();
  // 「启动线程…」入口只在存在可操作环境时出现；绑定与否由弹框处理
  const { data: launchEnvironments } = useEnvironments();
  const hasOperableEnvironment = (launchEnvironments ?? []).some((env) =>
    canOperate(env),
  );
  const timelineSectionRef = useRef<HTMLDivElement | null>(null);
  const labelDraftRef = useRef<string[] | null>(null);
  const labelPersistedRef = useRef<string[]>([]);
  const labelCommitInFlightRef = useRef(false);
  const labelQueuedRef = useRef(false);

  const handleImageClick = useCallback((e: React.MouseEvent) => {
    const target = e.target as HTMLElement;
    if (target.tagName === "IMG") {
      const img = target as HTMLImageElement;
      setLightboxImage({ src: img.src, alt: img.alt });
      setLightboxOpen(true);
    }
  }, []);

  const { data: issue, isLoading } = useIssue(iid!);
  const { data: project } = useProject(id!);
  const { data: repoLabels } = useIssueRepoLabels(id!);
  // 命中推荐时在正文卡片上方展示横幅；queryKey 与看板弹框同源，命中缓存
  const { data: recommendationsData } = useRecommendations(id);
  const recommendation = useMemo(
    () =>
      recommendationsData?.items.find((entry) => entry.issue.id === iid) ??
      null,
    [recommendationsData, iid],
  );
  const isInternalIssue = issue?.source === "internal";
  const shouldShowLocalAssetNotice =
    issue?.source === "github" && containsLocalIssueAssetReference(issue.body);
  const updateIssue = useUpdateIssue(iid!, id);
  const updateInternalMeta = useUpdateIssueInternalMeta(iid!, id);
  const createComment = useCreateIssueComment(iid!, id);
  const replaceChecklist = useReplaceIssueChecklist(iid!, id);
  const suggestChecklist = useIssueChecklistSuggestions(iid!);
  // 当前页与 prev/next 页共用同一组筛选，只有 page 不同
  const issueListFilters = {
    state: issueContext.state === "all" ? undefined : issueContext.state || undefined,
    q: issueContext.q || undefined,
    label: issueContext.label === "all" ? undefined : issueContext.label || undefined,
    source: issueContext.source === "all" ? undefined : issueContext.source || undefined,
    workflow_status:
      issueContext.workflowStatus === "all"
        ? undefined
        : issueContext.workflowStatus || undefined,
    sort: issueContext.sort,
  };
  const { data: issueListData } = useIssues(id!, {
    ...issueListFilters,
    page: issueContext.page,
    page_size: 20,
  });
  const { data: prevPageData } = useIssues(id!, {
    ...issueListFilters,
    page: issueContext.page > 1 ? issueContext.page - 1 : 1,
    page_size: 20,
  });
  const { data: nextPageData } = useIssues(id!, {
    ...issueListFilters,
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
  useMarkIssueReadWhenLoaded(issue, !!infiniteCommentsData, id);
  const {
    data: infiniteTimelineData,
    fetchNextPage: fetchNextTimelinePage,
    hasNextPage: hasNextTimelinePage,
    isFetchingNextPage: isFetchingNextTimelinePage,
    isLoading: timelineLoading,
  } = useInfiniteIssueTimeline(iid!, 20);
  const syncIssues = useSyncProjectIssues(id!);
  const persistedChecklist: IssueChecklistDraftItem[] = useMemo(
    () =>
      (issue?.internal_meta?.checklist ?? []).map((item) => ({
        localId: item.id,
        id: item.id,
        title: item.title,
        isCompleted: item.is_completed,
      })),
    [issue?.internal_meta?.checklist],
  );
  const issueLabelNames = useMemo(
    () =>
      normalizeLabelNames(
        (issue?.source === "github"
          ? issue?.github?.labels ?? []
          : issue?.internal_meta?.labels ?? []
        ).map((label) => label.name),
      ),
    [issue?.source, issue?.github?.labels, issue?.internal_meta?.labels],
  );
  const visibleLabelNames = labelDraft ?? issueLabelNames;
  const labelOptions = useMemo(() => {
    const options = new Set<string>();
    for (const name of visibleLabelNames) {
      options.add(name);
    }
    for (const item of repoLabels ?? []) {
      const value = item.name.trim();
      if (value) {
        options.add(value);
      }
    }
    return Array.from(options).sort((a, b) => a.localeCompare(b));
  }, [repoLabels, visibleLabelNames]);

  const setLabelDraftState = useCallback((labels: string[] | null) => {
    const normalized = labels ? normalizeLabelNames(labels) : null;
    labelDraftRef.current = normalized;
    setLabelDraft(normalized);
  }, []);

  useEffect(() => {
    if (labelCommitInFlightRef.current || labelQueuedRef.current) {
      return;
    }
    labelPersistedRef.current = issueLabelNames;
    setLabelDraftState(null);
  }, [issueLabelNames, setLabelDraftState]);

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

  const comments = useMemo(
    () => infiniteCommentsData?.pages.flatMap((page) => page.items) ?? [],
    [infiniteCommentsData?.pages],
  );
  const timeline = useMemo(
    () => infiniteTimelineData?.pages.flatMap((page) => page.items) ?? [],
    [infiniteTimelineData?.pages],
  );
  const loadedCommentsPages = infiniteCommentsData?.pages.length ?? 1;
  const loadedTimelinePages = infiniteTimelineData?.pages.length ?? 1;
  const targetCommentAnchorId = isCommentAnchorHash ? location.hash.slice(1) : null;
  const targetTimelineAnchorId = isTimelineAnchorHash ? location.hash.slice(1) : null;
  const timelineItems: IssueTimelineItem[] = useMemo(() => {
    const items: IssueTimelineItem[] = [
      ...comments.map((c) => ({ type: "comment" as const, data: c, created_at: c.created_at })),
      ...timeline.map((e) => ({ type: "event" as const, data: e, created_at: e.created_at })),
    ];
    items.sort((a, b) => new Date(a.created_at).getTime() - new Date(b.created_at).getTime());
    return items;
  }, [comments, timeline]);

  const hasMore = hasNextCommentsPage || hasNextTimelinePage;
  const isFetchingMoreTimeline = isFetchingNextCommentsPage || isFetchingNextTimelinePage;
  const isLoadingTimeline = commentsLoading || timelineLoading;

  // 时间线无限滚动：模块只给一个触发时机，两个分页各自按原条件补拉
  const handleTimelineLoadMore = useCallback(() => {
    if (hasNextCommentsPage && !isFetchingNextCommentsPage) {
      void fetchNextCommentsPage();
    }
    if (hasNextTimelinePage && !isFetchingNextTimelinePage) {
      void fetchNextTimelinePage();
    }
  }, [
    fetchNextCommentsPage,
    fetchNextTimelinePage,
    hasNextCommentsPage,
    hasNextTimelinePage,
    isFetchingNextCommentsPage,
    isFetchingNextTimelinePage,
  ]);

  const updateSearchParam = useCallback(
    (key: string, value: number) => {
      setSearchParams((current) => {
        const next = new URLSearchParams(current);
        if (value <= 1) {
          next.delete(key);
        } else {
          next.set(key, String(value));
        }
        return next.toString() === current.toString() ? current : next;
      }, { replace: true });
    },
    [setSearchParams]
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

  const copyIssuePrompt = async (content: string) => {
    await copyWithToast(
      buildIssuePrompt({
        projectId: id!,
        issueId: iid!,
        content,
        reason: recommendation?.reason,
      }),
      "已复制提示词",
    );
  };

  const handleCopyCurrentViewLink = async () => {
    const currentUrl = new URL(`${location.pathname}${location.search}${location.hash}`, window.location.origin).toString();
    await copyWithToast(currentUrl, "已复制当前问题视图链接");
  };

  const copyGitHubUrl = async (url: string | null | undefined, successMessage: string) => {
    if (!url) {
      toast.error("复制失败");
      return;
    }
    await copyWithToast(url, successMessage);
  };

  const handleCopyGitHubLink = async () => {
    const hash = location.hash;
    let targetUrl = issue?.github?.html_url;
    let successMessage = "已复制 GitHub 深链接";

    if (hash.startsWith("#issuecomment-")) {
      const comment = comments.find((c) => getCommentAnchorId(c.github_comment_id) === hash.slice(1));
      targetUrl = comment?.html_url || issue?.github?.html_url;
    } else if (hash.startsWith("#issueevent-")) {
      const event = timeline.find((e) => getTimelineAnchorId(e.github_event_id) === hash.slice(1));
      const eventHtmlUrl = event ? findPayloadHtmlUrl(event.payload) : null;
      targetUrl = eventHtmlUrl || issue?.github?.html_url;
      successMessage = eventHtmlUrl ? "已复制 GitHub 深链接" : "当前动态没有精确 GitHub 链接，已复制问题链接";
    }

    await copyGitHubUrl(targetUrl, successMessage);
  };

  // 清单模块只管 draft 与交互；持久化、成功/失败 toast 都由路由负责
  const handleSaveChecklist = useCallback(
    async (
      items: { id?: string; title: string; is_completed: boolean }[],
      options?: { successMessage?: string },
    ): Promise<boolean> => {
      try {
        await replaceChecklist.mutateAsync({ items });
        if (options?.successMessage) {
          toast.success(options.successMessage);
        }
        return true;
      } catch {
        toast.error("保存任务清单失败");
        return false;
      }
    },
    [replaceChecklist],
  );

  const requestChecklistSuggestions = useCallback(
    () => suggestChecklist.mutateAsync(),
    [suggestChecklist],
  );

  const handleConfigureAi = useCallback(() => navigate("/settings/ai"), [navigate]);

  const handleToggleIssueState = async () => {
    if (!issue) {
      return;
    }
    try {
      await updateIssue.mutateAsync({
        state: issue.state === "open" ? "closed" : "open",
      });
      toast.success(issue.state === "open" ? "问题已关闭" : "问题已重新打开");
    } catch {
      toast.error(issue.state === "open" ? "关闭问题失败" : "重新打开问题失败");
    }
  };

  const handleStartTitleEdit = () => {
    setTitleDraft(issue?.title ?? "");
    setIsTitleEditing(true);
  };

  const handleCancelTitleEdit = () => {
    setIsTitleEditing(false);
    setTitleDraft("");
  };

  const handleSaveTitle = async () => {
    if (!issue) return;
    const trimmed = titleDraft.trim();
    if (!trimmed) {
      toast.error("标题不能为空");
      return;
    }
    if (trimmed === issue.title) {
      setIsTitleEditing(false);
      return;
    }
    try {
      await updateIssue.mutateAsync({ title: trimmed });
      toast.success("标题已更新");
      setIsTitleEditing(false);
    } catch {
      toast.error("更新标题失败");
    }
  };

  const handleWorkflowStatusChange = async (value: string) => {
    const status = value === "unset" ? "" : (value as IssueWorkflowStatus);
    try {
      await updateInternalMeta.mutateAsync({
        workflow_status: status,
      });
      toast.success("内部状态已更新");
    } catch {
      toast.error("更新内部状态失败");
    }
  };

  const commitLabelDraft = useCallback(async () => {
    if (labelCommitInFlightRef.current) {
      labelQueuedRef.current = true;
      return;
    }

    labelCommitInFlightRef.current = true;

    try {
      while (true) {
        labelQueuedRef.current = false;
        const labelsToPersist =
          labelDraftRef.current ?? labelPersistedRef.current;

        await updateIssue.mutateAsync({
          labels: labelsToPersist,
        });
        labelPersistedRef.current = labelsToPersist;

        const latestDraft =
          labelDraftRef.current ?? labelPersistedRef.current;
        if (
          !labelQueuedRef.current ||
          haveSameLabelNames(latestDraft, labelsToPersist)
        ) {
          break;
        }
      }

      toast.success("标签已更新");
    } catch {
      labelQueuedRef.current = false;
      setLabelDraftState(labelPersistedRef.current);
      toast.error("更新标签失败");
    } finally {
      labelCommitInFlightRef.current = false;
    }
  }, [setLabelDraftState, updateIssue]);

  const handleToggleLabel = async (labelName: string) => {
    const currentLabels = labelDraftRef.current ?? issueLabelNames;
    const nextLabels = toggleLabelName(currentLabels, labelName);

    setLabelDraftState(nextLabels);
    await commitLabelDraft();
  };

  // 评论输入框的空值校验与清空都在 composer 模块内；这里只负责发布与 toast
  const handleCreateComment = useCallback(
    async (body: string): Promise<boolean> => {
      try {
        await createComment.mutateAsync({ body });
        toast.success("评论已发布");
        return true;
      } catch {
        toast.error("发布评论失败");
        return false;
      }
    },
    [createComment],
  );

  if (isLoading) {
    return (
      <>
        <Header title="问题详情" />
        <div className="mx-auto w-full max-w-7xl p-4 md:p-6">
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
        <div className="mx-auto w-full max-w-7xl p-4 md:p-6">
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

  const sidebarCards = (
    <>
      <Card>
        <CardHeader className="pb-0 pt-4">
          <CardTitle className="text-sm">元数据</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3 p-4 pt-3">
          <div className="flex items-center justify-between gap-2">
            <span className="text-sm">项目</span>
            <span className="text-sm text-muted-foreground">{project?.name ?? "-"}</span>
          </div>

          <div className="flex items-center justify-between gap-2">
            <span className="text-sm">内部状态</span>
            <OptionSelect
              value={issue.internal_meta?.workflow_status || "unset"}
              onValueChange={(value) => void handleWorkflowStatusChange(value ?? "unset")}
              disabled={updateInternalMeta.isPending}
              options={withMissingOption(
                [
                  { value: "unset", label: "未设置" },
                  ...ISSUE_WORKFLOW_STATUS_SELECT_OPTIONS,
                ],
                issue.internal_meta?.workflow_status || "unset",
                `${issue.internal_meta?.workflow_status}（未知）`,
              )}
              placeholder="未设置"
              className="h-7 w-auto min-w-[80px] border-0 bg-muted/50 text-xs text-muted-foreground hover:bg-muted data-[state=open]:bg-muted"
            />
          </div>

          {((issue.github?.assignees?.length ?? 0) > 0 ||
            issue.github?.milestone) && (
            <div className="space-y-3">
              {(issue.github?.assignees?.length ?? 0) > 0 && (
                <div>
                  <p className="mb-1.5 text-xs font-medium">负责人</p>
                  <div className="flex flex-wrap items-center gap-2">
                    {issue.github?.assignees.map((assignee) => (
                      <div
                        key={assignee.login}
                        className="inline-flex items-center gap-1.5 rounded-full border px-2 py-1 text-sm text-muted-foreground"
                      >
                        <Avatar className="h-5 w-5">
                          <AvatarImage src={toGitHubMediaProxyUrl(assignee.avatar_url, token)} alt={assignee.login} />
                          <AvatarFallback className="text-[10px]">{getInitials(assignee.login)}</AvatarFallback>
                        </Avatar>
                        <span>@{assignee.login}</span>
                      </div>
                    ))}
                  </div>
                </div>
              )}
              {issue.github?.milestone && (
                <div>
                  <p className="mb-1 text-xs font-medium">里程碑</p>
                  <p className="text-sm text-muted-foreground">{issue.github.milestone.title}</p>
                </div>
              )}
            </div>
          )}
          <div className="flex items-center justify-between gap-2">
            <span className="text-sm">标签</span>
            <DropdownMenu>
              <DropdownMenuTrigger
                render={
                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-7 w-auto min-w-[80px] border-0 bg-muted/50 text-xs text-muted-foreground hover:bg-muted data-[state=open]:bg-muted"
                  >
                    {visibleLabelNames.length === 0
                      ? "未设置"
                      : visibleLabelNames.join(", ")}
                  </Button>
                }
              />
              <DropdownMenuContent align="end" className="min-w-[180px] max-w-[260px]">
                {labelOptions.length === 0 ? (
                  <DropdownMenuItem disabled>暂无可选标签</DropdownMenuItem>
                ) : (
                  labelOptions.map((labelName) => (
                    <DropdownMenuCheckboxItem
                      key={labelName}
                      checked={visibleLabelNames.some(
                        (label) =>
                          label.toLowerCase() === labelName.toLowerCase(),
                      )}
                      onCheckedChange={() =>
                        void handleToggleLabel(labelName)
                      }
                    >
                      {labelName}
                    </DropdownMenuCheckboxItem>
                  ))
                )}
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        </CardContent>
      </Card>

      <IssueShipHookCard
        issueId={issue.id}
        projectId={issue.project_id}
        shipHook={issue.ship_hook}
      />

      <IssuePullRequestsCard
        key={issue.id}
        issueId={issue.id}
        projectId={issue.project_id}
        pullRequests={issue.pull_requests}
      />

      <IssueScreenshotAnnotationsCard
        projectId={issue.project_id}
        annotations={issue.screenshot_annotations}
      />

      <IssueChecklistCard
        items={persistedChecklist}
        updatedAt={issue.internal_meta?.checklist_updated_at ?? null}
        isSaving={replaceChecklist.isPending}
        onSave={handleSaveChecklist}
        requestSuggestions={requestChecklistSuggestions}
        onConfigureAi={handleConfigureAi}
      />
    </>
  );

  return (
    <>
      <Header
        title="问题详情"
        actions={
          <HeaderActions
            primary={
              <div className="flex flex-wrap items-center gap-2">
                <div className="mr-2 flex items-center gap-1">
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={!previousIssue}
                    onClick={() => previousIssue && navigateToIssue(previousIssue.id)}
                    title={previousIssue ? previousIssue.reference : "没有上一条"}
                  >
                    <ChevronLeft className="mr-1.5 h-3.5 w-3.5" />
                    上一条
                  </Button>
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={!nextIssue}
                    onClick={() => nextIssue && navigateToIssue(nextIssue.id)}
                    title={nextIssue ? nextIssue.reference : "没有下一条"}
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
                    {hasOperableEnvironment && (
                      <DropdownMenuItem onClick={() => setLaunchDialogOpen(true)}>
                        <Play className="h-4 w-4" />
                        启动线程…
                      </DropdownMenuItem>
                    )}
                    {issuePrompts.length === 1 ? (
                      <DropdownMenuItem
                        onClick={() => void copyIssuePrompt(issuePrompts[0].content)}
                      >
                        <FileText className="h-4 w-4" />
                        复制提示词
                      </DropdownMenuItem>
                    ) : issuePrompts.length > 1 ? (
                      <DropdownMenuSub>
                        <DropdownMenuSubTrigger>
                          <FileText className="h-4 w-4" />
                          复制提示词
                        </DropdownMenuSubTrigger>
                        <DropdownMenuSubContent>
                          {issuePrompts.map((prompt) => (
                            <DropdownMenuItem
                              key={prompt.id}
                              onClick={() => void copyIssuePrompt(prompt.content)}
                            >
                              {prompt.name}
                            </DropdownMenuItem>
                          ))}
                        </DropdownMenuSubContent>
                      </DropdownMenuSub>
                    ) : null}
                    <DropdownMenuSeparator />
                    {isInternalIssue && (
                      <DropdownMenuItem
                        onClick={() =>
                          navigate({
                            pathname: `/projects/${id}/issues/${iid}/edit`,
                            search: location.search,
                          })
                        }
                      >
                        <Pencil className="h-4 w-4" />
                        编辑问题
                      </DropdownMenuItem>
                    )}
                    <DropdownMenuItem
                      onClick={() => void handleToggleIssueState()}
                      disabled={updateIssue.isPending}
                    >
                      {issue.state === "open" ? <CheckCircle2 className="h-4 w-4" /> : <Inbox className="h-4 w-4" />}
                      {issue.state === "open" ? "关闭问题" : "重新打开"}
                    </DropdownMenuItem>
                    <DropdownMenuSeparator />
                    <DropdownMenuItem onClick={() => void handleCopyCurrentViewLink()}>
                      <Copy className="h-4 w-4" />
                      复制链接
                    </DropdownMenuItem>
                    {issue.github?.html_url && (
                      <DropdownMenuItem onClick={() => void handleCopyGitHubLink()}>
                        <Link2 className="h-4 w-4" />
                        复制 GitHub 深链接
                      </DropdownMenuItem>
                    )}
                    {issue.source === "github" && (
                      <>
                        <DropdownMenuSeparator />
                        <DropdownMenuItem
                          onClick={handleSync}
                          disabled={syncIssues.isPending}
                        >
                          <RefreshCw className={cn("h-4 w-4", syncIssues.isPending && "animate-spin")} />
                          {syncIssues.isPending ? "同步中..." : "重新同步"}
                        </DropdownMenuItem>
                      </>
                    )}
                    {issue.github?.html_url && (
                      <>
                        <DropdownMenuSeparator />
                        <DropdownMenuItem
                          render={
                            <a
                              href={issue.github.html_url}
                              target="_blank"
                              rel="noopener noreferrer"
                            />
                          }
                        >
                          <ExternalLink className="h-4 w-4" />
                          在 GitHub 查看
                        </DropdownMenuItem>
                      </>
                    )}
                  </DropdownMenuContent>
                </DropdownMenu>
              </div>
            }
          />
        }
      />
      <div className="mx-auto w-full max-w-7xl p-4 md:p-6">
        <div className="grid gap-6 lg:grid-cols-[1fr_300px] xl:grid-cols-[1fr_340px]">
          {/* Main Content */}
          <div className="min-w-0 space-y-6 [&_img]:cursor-zoom-in" onClick={handleImageClick}>
            {recommendation ? (
              <IssueRecommendationBanner recommendation={recommendation} />
            ) : null}
            {/* Issue Header Card */}
            <div className="overflow-hidden rounded-2xl border bg-card shadow-sm transition-shadow hover:shadow-md">
              <div className="p-5 md:p-6">
                <div className="flex flex-wrap items-start gap-3">
                  <StateBadge state={issue.state} />
                  <span className="inline-flex items-center rounded-full border px-2.5 py-1 text-xs font-semibold">
                    {ISSUE_SOURCE_LABELS[issue.source]}
                  </span>
                  <ProgressBadge progress={issue.internal_meta?.progress_percent} />
                  <IssueWorkflowStatusBadge
                    size="md"
                    status={issue.internal_meta?.workflow_status}
                  />
                  {(issue.source === "github"
                    ? issue.github?.labels ?? []
                    : issue.internal_meta?.labels ?? []
                  ).map((label) => (
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

                <div className="mt-4 flex items-start gap-3">
                  {isTitleEditing ? (
                    <>
                      <Input
                        value={titleDraft}
                        onChange={(e) => setTitleDraft(e.target.value)}
                        onKeyDown={(e) => {
                          if (e.key === "Enter") {
                            e.preventDefault();
                            void handleSaveTitle();
                          } else if (e.key === "Escape") {
                            handleCancelTitleEdit();
                          }
                        }}
                        className="flex-1 text-base font-semibold md:text-lg"
                        autoFocus
                      />
                      <div className="flex items-center gap-2">
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={handleCancelTitleEdit}
                          disabled={updateIssue.isPending}
                        >
                          取消
                        </Button>
                        <Button
                          size="sm"
                          onClick={() => void handleSaveTitle()}
                          disabled={updateIssue.isPending}
                        >
                          {updateIssue.isPending ? (
                            <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
                          ) : null}
                          保存
                        </Button>
                      </div>
                    </>
                  ) : (
                    <>
                      <h1 className="flex-1 text-xl font-semibold leading-snug text-foreground md:text-2xl">
                        {issue.reference} {issue.title}
                      </h1>
                      <Button
                        variant="ghost"
                        size="sm"
                        className="shrink-0"
                        onClick={handleStartTitleEdit}
                        aria-label="编辑标题"
                      >
                        <Pencil className="mr-1.5 h-3.5 w-3.5" />
                        编辑
                      </Button>
                    </>
                  )}
                </div>

                <div className="mt-4 flex flex-wrap items-center gap-4 text-sm text-muted-foreground">
                  <div className="flex items-center gap-2">
                    <Avatar size="sm">
                      <AvatarImage src={toGitHubMediaProxyUrl(issue.author.avatar_url, token)} alt={issue.author.login} />
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
                  <>
                    {shouldShowLocalAssetNotice ? (
                        <div className="mb-4 flex items-start gap-2 rounded-xl border border-amber-500/20 bg-amber-500/5 px-4 py-3 text-sm text-amber-700 dark:text-amber-300">
                          <Lightbulb className="mt-0.5 h-4 w-4 shrink-0" />
                          <p>{GITHUB_LOCAL_ASSET_NOTICE}</p>
                        </div>
                    ) : null}
                    <div className="markdown-body">
                      <GitHubContent html={issue.body_html} markdown={issue.body} />
                    </div>
                  </>
                ) : (
                  <div className="flex items-center gap-2 text-sm italic text-muted-foreground">
                    <MessageSquare className="h-4 w-4" />
                    暂无描述
                  </div>
                )}
              </div>

              <Separator />
              <div className="p-5 md:p-6">
                <CollaborationArea issueId={iid!} />
              </div>
            </div>

            {isInternalIssue && (
              <IssueAttachmentsCard
                issueId={issue.id}
                attachments={issue.attachments}
              />
            )}

            <div className="-mt-6">
              <div id="timeline" ref={timelineSectionRef} className="scroll-mt-20" />

              <IssueTimeline
                items={timelineItems}
                isLoading={isLoadingTimeline}
                hasMore={hasMore}
                isFetchingMore={isFetchingMoreTimeline}
                onLoadMore={handleTimelineLoadMore}
                flashHighlightId={flashHighlightId}
                onNavigateToAnchor={setAnchor}
                onCopyGithubLink={copyGitHubUrl}
                fallbackGithubUrl={issue.github?.html_url ?? null}
              />

              <IssueCommentComposer
                isInternalIssue={isInternalIssue}
                onSubmit={handleCreateComment}
              />
            </div>
          </div>

          {/* Desktop Sidebar */}
          <aside className="hidden space-y-4 lg:block lg:sticky lg:top-20 lg:self-start">
            {sidebarCards}
          </aside>
        </div>
      </div>

      {/* Mobile sidebar floating button */}
      <div className="fixed bottom-6 right-6 z-40 lg:hidden">
        <Button
          size="icon"
          className="h-12 w-12 rounded-full shadow-lg"
          onClick={() => setIsMobileSidebarOpen(true)}
        >
          <PanelLeft className="h-5 w-5" />
        </Button>
      </div>

      {/* Mobile sidebar Sheet */}
      <Sheet open={isMobileSidebarOpen} onOpenChange={setIsMobileSidebarOpen}>
        <SheetContent side="bottom" showCloseButton={false} className="max-h-[85vh] overflow-y-auto rounded-t-2xl">
          <SheetHeader className="border-b pb-3">
            <SheetTitle>面板</SheetTitle>
          </SheetHeader>
          <div className="space-y-4 p-4 pb-6">
            {sidebarCards}
          </div>
        </SheetContent>
      </Sheet>

      {/* Image Lightbox */}
      <Dialog open={lightboxOpen} onOpenChange={setLightboxOpen}>
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

      <LaunchIssueThreadDialog
        open={launchDialogOpen}
        onOpenChange={setLaunchDialogOpen}
        projectId={id!}
        issue={{
          id: issue.id,
          reference: issue.reference,
          title: issue.title,
        }}
        reason={recommendation?.reason}
      />
    </>
  );
}
