import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useLocation, useNavigate, useParams, useSearchParams } from "react-router";
import {
  Check,
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
  Inbox,
  Lightbulb,
  Link2,
  ListChecks,
  Loader2,
  Lock,
  MessageSquare,
  Pencil,
  Plus,
  RefreshCw,
  Sparkles,
  Tag,
  Trash2,
  Unlock,
  User,
  Wand2,
  X,
} from "lucide-react";
import { GitHubContent } from "@/components/github-content";
import { Header } from "@/components/layout/header";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";

import { Card, CardContent } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
  DropdownMenuSeparator,
} from "@/components/ui/dropdown-menu";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { Separator } from "@/components/ui/separator";
import {
  useIssue,
  useIssueRepoLabels,
  useCreateIssueComment,
  useInfiniteIssueComments,
  useInfiniteIssueTimeline,
  useIssues,
  useSyncProjectIssues,
  useUpdateIssue,
  useReplaceIssueChecklist,
  useUpdateIssueInternalMeta,
} from "@/lib/hooks/use-issues";
import { useProject } from "@/lib/hooks/use-projects";
import { useIssueChecklistSuggestions } from "@/lib/hooks/use-ai";
import {
  ISSUE_WORKFLOW_STATUS_LABELS,
  ISSUE_WORKFLOW_STATUS_OPTIONS,
  type IssueWorkflowStatus,
} from "@/lib/issue-workflow-status";
import { readIssueDetailContext } from "@/lib/issue-list-context";
import { useAuthStore } from "@/lib/store/auth-store";
import { toGitHubMediaProxyUrl } from "@/lib/utils/github-media-proxy";
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

type ChecklistDraftItem = {
  localId: string;
  id?: string;
  title: string;
  isCompleted: boolean;
};

type SuggestedChecklistDraftItem = {
  id: string;
  title: string;
  selected: boolean;
};

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

function WorkflowStatusBadge({ status }: { status?: string | null }) {
  if (!status) {
    return null;
  }

  const label = ISSUE_WORKFLOW_STATUS_LABELS[status as IssueWorkflowStatus];
  if (!label) {
    return null;
  }

  const className =
    status === "todo"
      ? "border-slate-500/20 bg-slate-500/10 text-slate-600 dark:text-slate-400"
      : status === "in_progress"
        ? "border-amber-500/20 bg-amber-500/10 text-amber-600 dark:text-amber-400"
        : "border-emerald-500/20 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400";

  return (
    <span
      className={cn(
        "inline-flex items-center rounded-full border px-2.5 py-1 text-xs font-semibold",
        className,
      )}
    >
      {label}
    </span>
  );
}

function normalizeChecklistDraft(items: ChecklistDraftItem[]) {
  return items.map((item) => ({
    id: item.id ?? "",
    title: item.title.trim(),
    is_completed: item.isCompleted,
  }));
}

function calculateChecklistProgress(items: Array<{ isCompleted: boolean }>) {
  if (items.length === 0) {
    return null;
  }
  const completed = items.filter((item) => item.isCompleted).length;
  return Math.round((completed * 100) / items.length);
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
  const [commentDraft, setCommentDraft] = useState("");
  const [isTitleEditing, setIsTitleEditing] = useState(false);
  const [titleDraft, setTitleDraft] = useState("");
  const [isChecklistEditing, setIsChecklistEditing] = useState(false);
  const [checklistDraft, setChecklistDraft] = useState<ChecklistDraftItem[]>([]);
  const [labelDraft, setLabelDraft] = useState<string[] | null>(null);
  const [isSuggestionDialogOpen, setIsSuggestionDialogOpen] = useState(false);
  const [suggestionState, setSuggestionState] = useState<
    "idle" | "loading" | "ready" | "missing-settings" | "error"
  >("idle");
  const [suggestedChecklistItems, setSuggestedChecklistItems] = useState<
    SuggestedChecklistDraftItem[]
  >([]);
  const sentinelRef = useRef<HTMLDivElement | null>(null);
  const timelineSectionRef = useRef<HTMLDivElement | null>(null);
  const isChecklistEditingRef = useRef(false);
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
  const isInternalIssue = issue?.source === "internal";
  const updateIssue = useUpdateIssue(iid!, id);
  const updateInternalMeta = useUpdateIssueInternalMeta(iid!, id);
  const createComment = useCreateIssueComment(iid!, id);
  const replaceChecklist = useReplaceIssueChecklist(iid!, id);
  const suggestChecklist = useIssueChecklistSuggestions(iid!);
  const { data: issueListData } = useIssues(id!, {
    state: issueContext.state === "all" ? undefined : issueContext.state || undefined,
    q: issueContext.q || undefined,
    label: issueContext.label === "all" ? undefined : issueContext.label || undefined,
    source: issueContext.source === "all" ? undefined : issueContext.source || undefined,
    workflow_status:
      issueContext.workflowStatus === "all"
        ? undefined
        : issueContext.workflowStatus || undefined,
    sort: issueContext.sort,
    page: issueContext.page,
    page_size: 20,
  });
  const { data: prevPageData } = useIssues(id!, {
    state: issueContext.state === "all" ? undefined : issueContext.state || undefined,
    q: issueContext.q || undefined,
    label: issueContext.label === "all" ? undefined : issueContext.label || undefined,
    source: issueContext.source === "all" ? undefined : issueContext.source || undefined,
    workflow_status:
      issueContext.workflowStatus === "all"
        ? undefined
        : issueContext.workflowStatus || undefined,
    sort: issueContext.sort,
    page: issueContext.page > 1 ? issueContext.page - 1 : 1,
    page_size: 20,
  });
  const { data: nextPageData } = useIssues(id!, {
    state: issueContext.state === "all" ? undefined : issueContext.state || undefined,
    q: issueContext.q || undefined,
    label: issueContext.label === "all" ? undefined : issueContext.label || undefined,
    source: issueContext.source === "all" ? undefined : issueContext.source || undefined,
    workflow_status:
      issueContext.workflowStatus === "all"
        ? undefined
        : issueContext.workflowStatus || undefined,
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
  const persistedChecklist = useMemo(
    () =>
      (issue?.internal_meta?.checklist ?? []).map((item) => ({
        localId: item.id,
        id: item.id,
        title: item.title,
        isCompleted: item.is_completed,
      })),
    [issue?.internal_meta?.checklist],
  );
  const draftProgress = useMemo(
    () => calculateChecklistProgress(checklistDraft),
    [checklistDraft],
  );
  const completedChecklistCount = useMemo(
    () => checklistDraft.filter((item) => item.isCompleted).length,
    [checklistDraft],
  );
  const isChecklistDirty = useMemo(() => {
    return JSON.stringify(normalizeChecklistDraft(checklistDraft)) !==
      JSON.stringify(normalizeChecklistDraft(persistedChecklist));
  }, [checklistDraft, persistedChecklist]);
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
    isChecklistEditingRef.current = isChecklistEditing;
  }, [isChecklistEditing]);

  useEffect(() => {
    if (isChecklistEditingRef.current) {
      return;
    }
    setChecklistDraft(persistedChecklist);
  }, [persistedChecklist]);

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
  const timelineItems: TimelineItem[] = useMemo(() => {
    const items: TimelineItem[] = [
      ...comments.map((c) => ({ type: "comment" as const, data: c, created_at: c.created_at })),
      ...timeline.map((e) => ({ type: "event" as const, data: e, created_at: e.created_at })),
    ];
    items.sort((a, b) => new Date(a.created_at).getTime() - new Date(b.created_at).getTime());
    return items;
  }, [comments, timeline]);

  const hasMore = hasNextCommentsPage || hasNextTimelinePage;
  const isLoadingTimeline = commentsLoading || timelineLoading;

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

  const handleCopyCommentGitHubLink = async (comment: IssueComment) => {
    await copyGitHubUrl(comment.html_url, "已复制 GitHub 深链接");
  };

  const handleCopyTimelineGitHubLink = async (event: IssueTimelineEvent) => {
    const eventHtmlUrl = findPayloadHtmlUrl(event.payload);
    const targetUrl = eventHtmlUrl || issue?.github?.html_url;
    const successMessage = eventHtmlUrl ? "已复制 GitHub 深链接" : "当前动态没有精确 GitHub 链接，已复制问题链接";
    await copyGitHubUrl(targetUrl, successMessage);
  };

  const handleChecklistAdd = () => {
    setChecklistDraft((current) => [
      ...current,
      {
        localId: globalThis.crypto?.randomUUID?.() ?? `draft-${Date.now()}`,
        title: "",
        isCompleted: false,
      },
    ]);
  };

  const handleChecklistChange = (
    localId: string,
    updates: Partial<Pick<ChecklistDraftItem, "title" | "isCompleted">>,
  ) => {
    setChecklistDraft((current) =>
      current.map((item) =>
        item.localId === localId ? { ...item, ...updates } : item,
      ),
    );
  };

  const handleChecklistRemove = (localId: string) => {
    setChecklistDraft((current) => current.filter((item) => item.localId !== localId));
  };

  const persistChecklist = async (
    nextDraft: ChecklistDraftItem[],
    options?: { successMessage?: string; rollbackDraft?: ChecklistDraftItem[]; exitEditMode?: boolean },
  ) => {
    const normalized = nextDraft.map((item) => ({
      id: item.id,
      title: item.title.trim(),
      is_completed: item.isCompleted,
    }));
    if (normalized.some((item) => !item.title)) {
      toast.error("请先填写所有任务清单标题");
      return false;
    }

    try {
      await replaceChecklist.mutateAsync({ items: normalized });
      if (options?.successMessage) {
        toast.success(options.successMessage);
      }
      if (options?.exitEditMode) {
        setIsChecklistEditing(false);
      }
      return true;
    } catch {
      if (options?.rollbackDraft) {
        setChecklistDraft(options.rollbackDraft);
      }
      toast.error("保存任务清单失败");
      return false;
    }
  };

  const handleChecklistSave = async () => {
    await persistChecklist(checklistDraft, {
      successMessage: "任务清单已保存",
      exitEditMode: true,
    });
  };

  const handleChecklistToggleCompleted = async (localId: string) => {
    const rollbackDraft = checklistDraft;
    const nextDraft = checklistDraft.map((item) =>
      item.localId === localId ? { ...item, isCompleted: !item.isCompleted } : item,
    );
    setChecklistDraft(nextDraft);
    await persistChecklist(nextDraft, { rollbackDraft });
  };

  const handleChecklistCancelEdit = () => {
    setChecklistDraft(persistedChecklist);
    setIsChecklistEditing(false);
  };

  const handleOpenSuggestionDialog = async () => {
    setIsSuggestionDialogOpen(true);
    setSuggestionState("loading");
    setSuggestedChecklistItems([]);

    try {
      const result = await suggestChecklist.mutateAsync();
      setSuggestedChecklistItems(
        result.items.map((item, index) => ({
          id: `${index}-${item.title}`,
          title: item.title,
          selected: true,
        })),
      );
      setSuggestionState("ready");
    } catch (error) {
      const status = (error as { response?: { status?: number } })?.response?.status;
      setSuggestionState(status === 404 ? "missing-settings" : "error");
    }
  };

  const handleSuggestionToggle = (id: string) => {
    setSuggestedChecklistItems((current) =>
      current.map((item) =>
        item.id === id ? { ...item, selected: !item.selected } : item,
      ),
    );
  };

  const handleAppendSuggestions = async () => {
    const selectedItems = suggestedChecklistItems.filter((item) => item.selected);
    if (selectedItems.length === 0) {
      toast.error("请至少选择一项");
      return;
    }

    const rollbackDraft = checklistDraft;
    const nextDraft = [
      ...checklistDraft,
      ...selectedItems.map((item) => ({
        localId: globalThis.crypto?.randomUUID?.() ?? `draft-${Date.now()}-${item.id}`,
        title: item.title,
        isCompleted: false,
      })),
    ];
    setChecklistDraft(nextDraft);

    const success = await persistChecklist(nextDraft, {
      rollbackDraft,
      successMessage: "已补充到任务清单",
    });
    if (success) {
      setIsSuggestionDialogOpen(false);
    }
  };

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
    let persistedAny = false;

    try {
      while (true) {
        labelQueuedRef.current = false;
        const labelsToPersist =
          labelDraftRef.current ?? labelPersistedRef.current;

        await updateIssue.mutateAsync({
          labels: labelsToPersist,
        });
        persistedAny = true;
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

      if (persistedAny) {
        toast.success("标签已更新");
      }
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

  const handleCreateComment = async () => {
    if (!commentDraft.trim()) {
      toast.error("请输入评论内容");
      return;
    }
    try {
      await createComment.mutateAsync({ body: commentDraft });
      setCommentDraft("");
      toast.success("评论已发布");
    } catch {
      toast.error("发布评论失败");
    }
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
      <Header title={`${project?.name ?? ""}${project ? "·" : ""}${issue.reference}`} />
      <div className="mx-auto max-w-7xl p-4 md:p-6">
        {/* Top Navigation Bar */}
        <div className="mb-6 flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-end">
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
        </div>

        <div className="grid gap-6 lg:grid-cols-[1fr_300px] xl:grid-cols-[1fr_340px]">
          {/* Main Content */}
          <div className="min-w-[800px] space-y-6 [&_img]:cursor-zoom-in" onClick={handleImageClick}>
            {/* Issue Header Card */}
            <div className="overflow-hidden rounded-2xl border bg-card shadow-sm transition-shadow hover:shadow-md">
              <div className="p-5 md:p-6">
                <div className="flex flex-wrap items-start gap-3">
                  <StateBadge state={issue.state} />
                  <span className="inline-flex items-center rounded-full border px-2.5 py-1 text-xs font-semibold">
                    {issue.source === "github" ? "GitHub" : "内部"}
                  </span>
                  <ProgressBadge progress={issue.internal_meta?.progress_percent} />
                  <WorkflowStatusBadge status={issue.internal_meta?.workflow_status} />
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

            {/* Timeline and Comment Section */}
            <div className="-mt-6">
              {/* Unified Timeline */}
              <div id="timeline" ref={timelineSectionRef} className="scroll-mt-20" />

              {isLoadingTimeline ? (
                <div className="relative space-y-6 pt-6">
                  {Array.from({ length: 3 }).map((_, i) => (
                    <Skeleton key={i} className="h-24 rounded-2xl" />
                  ))}
                </div>
              ) : timelineItems.length === 0 ? (
                <div className="py-6">
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
                                  setAnchor(`#${getCommentAnchorId(item.data.github_comment_id)}`)
                                }
                              >
                                <Link2 className="h-3.5 w-3.5" />
                              </Button>
                              {item.data.source === "github" && (
                                <Button
                                  variant="ghost"
                                  size="icon-xs"
                                  aria-label={`复制评论 ${item.data.github_comment_id} 的 GitHub 深链接`}
                                  onClick={() => void handleCopyCommentGitHubLink(item.data)}
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

              {timelineItems.length > 0 && hasMore && (
                <div ref={sentinelRef} className="h-1 w-full" />
              )}

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
                    value={commentDraft}
                    onChange={(event) => setCommentDraft(event.target.value)}
                    rows={5}
                    placeholder="使用 Markdown 输入评论内容"
                  />
                  <div className="flex justify-end">
                    <Button
                      onClick={() => void handleCreateComment()}
                      disabled={createComment.isPending}
                    >
                      {createComment.isPending ? "发布中..." : "发布评论"}
                    </Button>
                  </div>
                </div>
              </CardContent>
            </Card>
            </div>
          </div>

          {/* Sidebar */}
          <aside className="space-y-4 lg:sticky lg:top-20 lg:self-start">
            {/* 创建者与元信息 */}
            <Card>
              <CardContent className="space-y-4 p-4">
                {/* 创建者 */}
                <div className="flex items-center gap-3">
                  <Avatar className="h-10 w-10">
                    <AvatarImage src={toGitHubMediaProxyUrl(issue.author.avatar_url, token)} alt={issue.author.login} />
                    <AvatarFallback>{getInitials(issue.author.login)}</AvatarFallback>
                  </Avatar>
                  <div className="min-w-0">
                    <p className="text-xs text-muted-foreground">创建者</p>
                    <p className="truncate text-sm font-semibold">@{issue.author.login}</p>
                  </div>
                </div>

                <Separator />

                {/* 时间 */}
                <div className="space-y-1.5 text-sm">
                  <div className="flex items-center justify-between gap-2">
                    <span className="text-muted-foreground">创建于</span>
                    <span className="font-medium">{formatDate(issue.created_at)}</span>
                  </div>
                  <div className="flex items-center justify-between gap-2">
                    <span className="text-muted-foreground">更新于</span>
                    <span className="font-medium">{formatRelativeTime(issue.updated_at)}</span>
                  </div>
                  {issue.github?.synced_at && (
                    <div className="flex items-center justify-between gap-2">
                      <span className="text-muted-foreground">同步于</span>
                      <span className="font-medium">{formatRelativeTime(issue.github.synced_at)}</span>
                    </div>
                  )}
                </div>

                <Separator />

                {/* 内部状态 */}
                <div className="flex items-center justify-between gap-2">
                  <span className="text-sm text-muted-foreground">内部状态</span>
                  <Select
                    value={issue.internal_meta?.workflow_status || "unset"}
                    onValueChange={(value) => void handleWorkflowStatusChange(value ?? "unset")}
                    disabled={updateInternalMeta.isPending}
                  >
                    <SelectTrigger className="h-7 w-auto min-w-[80px] border-0 bg-muted/50 text-xs hover:bg-muted data-[state=open]:bg-muted">
                      <SelectValue placeholder="未设置">
                        {issue.internal_meta?.workflow_status
                          ? ISSUE_WORKFLOW_STATUS_LABELS[issue.internal_meta.workflow_status as IssueWorkflowStatus]
                          : "未设置"}
                      </SelectValue>
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="unset">未设置</SelectItem>
                      {ISSUE_WORKFLOW_STATUS_OPTIONS.map((option) => (
                        <SelectItem key={option.value} value={option.value}>
                          {option.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>

                {/* 负责人 & 里程碑 & 标签 */}
                {(issue.source === "github" ||
                  issue.source === "internal" ||
                  (issue.github?.assignees?.length ?? 0) > 0 ||
                  issue.github?.milestone ||
                  (issue.github?.labels?.length ?? 0) > 0) && (
                  <>
                    <Separator />
                    <div className="space-y-3">
                      {(issue.github?.assignees?.length ?? 0) > 0 && (
                        <div>
                          <p className="mb-1.5 text-xs font-medium text-muted-foreground">负责人</p>
                          <div className="flex flex-wrap items-center gap-2">
                            {issue.github?.assignees.map((assignee) => (
                              <div
                                key={assignee.login}
                                className="inline-flex items-center gap-1.5 rounded-full border px-2 py-1 text-sm"
                              >
                                <Avatar className="h-5 w-5">
                                  <AvatarImage src={toGitHubMediaProxyUrl(assignee.avatar_url, token)} alt={assignee.login} />
                                  <AvatarFallback className="text-[10px]">{getInitials(assignee.login)}</AvatarFallback>
                                </Avatar>
                                <span className="font-medium">@{assignee.login}</span>
                              </div>
                            ))}
                          </div>
                        </div>
                      )}
                      {issue.github?.milestone && (
                        <div>
                          <p className="mb-1 text-xs font-medium text-muted-foreground">里程碑</p>
                          <p className="text-sm font-semibold">{issue.github.milestone.title}</p>
                        </div>
                      )}
                      {(issue.source === "github" || issue.source === "internal") && (
                        <div className="flex items-center justify-between gap-2">
                          <span className="text-sm text-muted-foreground">标签</span>
                          <DropdownMenu>
                            <DropdownMenuTrigger
                              render={
                                <Button
                                  variant="ghost"
                                  size="sm"
                                  className="h-7 w-auto min-w-[80px] border-0 bg-muted/50 text-xs hover:bg-muted data-[state=open]:bg-muted"
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
                      )}
                    </div>
                  </>
                )}
              </CardContent>
            </Card>

            {/* 任务清单 */}
            <Card>
              <CardContent className="space-y-4 p-4">
                <div className="flex items-center justify-between">
                  <h3 className="text-sm font-semibold">任务清单</h3>
                  {isChecklistEditing ? (
                    <div className="flex items-center gap-1">
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        aria-label="取消编辑"
                        onClick={handleChecklistCancelEdit}
                        disabled={replaceChecklist.isPending}
                      >
                        <X className="h-3.5 w-3.5" />
                      </Button>
                      <Button
                        size="icon-sm"
                        aria-label="保存"
                        title="保存任务清单"
                        onClick={() => void handleChecklistSave()}
                        disabled={!isChecklistDirty || replaceChecklist.isPending}
                      >
                        {replaceChecklist.isPending ? (
                          <Loader2 className="h-3.5 w-3.5 animate-spin" />
                        ) : (
                          <Check className="h-3.5 w-3.5" />
                        )}
                      </Button>
                    </div>
                  ) : (
                    <div className="flex items-center gap-1">
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        aria-label="智能识别任务清单建议"
                        onClick={() => void handleOpenSuggestionDialog()}
                      >
                        <Sparkles className="h-3.5 w-3.5" />
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        aria-label="编辑"
                        title="编辑任务清单"
                        onClick={() => setIsChecklistEditing(true)}
                      >
                        <Pencil className="h-3.5 w-3.5" />
                      </Button>
                    </div>
                  )}
                </div>

                {checklistDraft.length > 0 && (
                  <div className="space-y-2">
                    <div className="flex items-center justify-between text-sm">
                      <span className="font-semibold">{draftProgress ?? 0}%</span>
                      <span className="text-xs text-muted-foreground">
                        {completedChecklistCount}/{checklistDraft.length} 项完成
                        {issue.internal_meta?.checklist_updated_at && (
                          <span className="ml-1">· 更新于 {formatRelativeTime(issue.internal_meta.checklist_updated_at)}</span>
                        )}
                      </span>
                    </div>
                    <div className="h-2 overflow-hidden rounded-full bg-muted">
                      <div
                        className={cn(
                          "h-full rounded-full transition-all",
                          (draftProgress ?? 0) >= 100 ? "bg-emerald-500" : "bg-amber-500",
                        )}
                        style={{ width: `${draftProgress ?? 0}%` }}
                      />
                    </div>
                  </div>
                )}

                <div className="space-y-2">
                  {checklistDraft.length === 0 ? (
                    <div className="text-sm text-muted-foreground">
                      还没有任务清单。
                    </div>
                  ) : (
                    checklistDraft.map((item, index) => (
                      <div
                        key={item.localId}
                        className={cn(
                          "group flex items-center gap-3 rounded-lg border p-2.5 transition-colors",
                          isChecklistEditing ? "bg-card" : "bg-card hover:bg-muted/50",
                        )}
                      >
                        {isChecklistEditing ? (
                          <>
                            <button
                              type="button"
                              onClick={() => handleChecklistChange(item.localId, { isCompleted: !item.isCompleted })}
                              className={cn(
                                "inline-flex h-5 w-5 shrink-0 items-center justify-center rounded border transition-colors",
                                item.isCompleted
                                  ? "border-emerald-500 bg-emerald-500 text-white"
                                  : "border-input bg-background text-transparent hover:border-muted-foreground",
                              )}
                            >
                              <CheckCircle2 className="h-3.5 w-3.5" />
                            </button>
                            <Input
                              value={item.title}
                              onChange={(event) =>
                                handleChecklistChange(item.localId, { title: event.target.value })
                              }
                              placeholder={`任务 ${index + 1}`}
                              className={cn("h-8 flex-1", item.isCompleted && "text-muted-foreground line-through")}
                            />
                            <Button
                              variant="ghost"
                              size="icon"
                              className="h-8 w-8 shrink-0"
                              aria-label={`删除任务清单 ${index + 1}`}
                              onClick={() => handleChecklistRemove(item.localId)}
                            >
                              <Trash2 className="h-4 w-4 text-muted-foreground" />
                            </Button>
                          </>
                        ) : (
                          <>
                            <button
                              type="button"
                              disabled={replaceChecklist.isPending}
                              onClick={() => void handleChecklistToggleCompleted(item.localId)}
                              aria-label={
                                item.isCompleted
                                  ? `取消完成任务清单 ${index + 1}`
                                  : `标记完成任务清单 ${index + 1}`
                              }
                              className={cn(
                                "inline-flex h-5 w-5 shrink-0 cursor-pointer items-center justify-center rounded border transition-colors",
                                item.isCompleted
                                  ? "border-emerald-500 bg-emerald-500 text-white"
                                  : "border-input bg-background text-transparent hover:border-muted-foreground",
                              )}
                            >
                              <CheckCircle2 className="h-3.5 w-3.5" />
                            </button>
                            <p
                              className={cn(
                                "min-w-0 flex-1 text-sm",
                                item.isCompleted && "text-muted-foreground line-through",
                              )}
                            >
                              {item.title}
                            </p>
                          </>
                        )}
                      </div>
                    ))
                  )}

                  {isChecklistEditing && (
                    <Button variant="outline" className="w-full" onClick={handleChecklistAdd}>
                      <Plus className="mr-1.5 h-3.5 w-3.5" />
                      {checklistDraft.length === 0 ? "添加第一项" : "添加项"}
                    </Button>
                  )}
                </div>
              </CardContent>
            </Card>

            <Dialog open={isSuggestionDialogOpen} onOpenChange={setIsSuggestionDialogOpen}>
              <DialogContent className="flex w-[90vw] max-w-[calc(100%-2rem)] flex-col overflow-hidden p-0 max-h-[90vh] sm:max-w-[1100px]">
                {/* Header */}
                <DialogHeader className="shrink-0 border-b bg-muted/30 px-6 pt-5 pb-4">
                  <div className="flex items-center gap-3">
                    <div className="flex h-9 w-9 items-center justify-center rounded-lg bg-primary/10 text-primary">
                      <Wand2 className="h-4.5 w-4.5" />
                    </div>
                    <div className="flex-1">
                      <DialogTitle className="text-base">清单建议</DialogTitle>
                      <DialogDescription className="mt-0.5 text-xs">
                        基于问题内容智能识别可补充的任务清单项
                      </DialogDescription>
                    </div>
                  </div>
                </DialogHeader>

                {/* Body */}
                <div className="grid flex-1 gap-0 overflow-hidden md:grid-cols-[3fr_2fr]">
                  {/* Left: AI Suggestions */}
                  <div className="flex flex-col gap-3 overflow-hidden border-r px-6 py-5">
                    <div className="flex items-center justify-between">
                      <div className="flex items-center gap-2">
                        <Lightbulb className="h-4 w-4 text-amber-500" />
                        <span className="text-sm font-semibold">AI 建议</span>
                        {suggestionState === "ready" && suggestedChecklistItems.length > 0 && (
                          <span className="inline-flex items-center rounded-full bg-primary/10 px-2 py-0.5 text-[11px] font-medium text-primary">
                            {suggestedChecklistItems.filter((i) => i.selected).length}/
                            {suggestedChecklistItems.length} 已选
                          </span>
                        )}
                      </div>
                      <Button
                        variant="ghost"
                        size="sm"
                        className="h-8 gap-1.5 text-xs"
                        onClick={() => void handleOpenSuggestionDialog()}
                        disabled={suggestionState === "loading"}
                      >
                        {suggestionState === "loading" ? (
                          <>
                            <Loader2 className="h-3.5 w-3.5 animate-spin" />
                            识别中...
                          </>
                        ) : (
                          <>
                            <RefreshCw className="h-3.5 w-3.5" />
                            重新识别
                          </>
                        )}
                      </Button>
                    </div>

                    <div className="flex-1 overflow-y-auto rounded-xl border bg-muted/20 p-3">
                      {suggestionState === "loading" ? (
                        <div className="space-y-3">
                          {[...Array(4)].map((_, i) => (
                            <div
                              key={i}
                              className="flex items-start gap-3 rounded-lg border border-dashed border-border/60 bg-background/50 p-3"
                            >
                              <Skeleton className="mt-0.5 h-5 w-5 shrink-0 rounded" />
                              <div className="flex-1 space-y-2">
                                <Skeleton className="h-4 w-3/4 rounded" />
                                <Skeleton className="h-3 w-1/4 rounded" />
                              </div>
                            </div>
                          ))}
                        </div>
                      ) : suggestionState === "missing-settings" ? (
                        <div className="flex h-full min-h-[280px] flex-col items-center justify-center rounded-lg border border-dashed bg-background px-6 text-center">
                          <div className="flex h-12 w-12 items-center justify-center rounded-full bg-muted">
                            <Sparkles className="h-5 w-5 text-muted-foreground" />
                          </div>
                          <p className="mt-4 text-sm font-medium">尚未配置 AI</p>
                          <p className="mt-1 max-w-[240px] text-xs text-muted-foreground leading-relaxed">
                            先在设置中保存 API Host、模型和 API Key，再回来使用智能识别。
                          </p>
                          <Button
                            className="mt-5 h-8 text-xs"
                            size="sm"
                            onClick={() => navigate("/settings/ai")}
                          >
                            前往 AI 配置
                          </Button>
                        </div>
                      ) : suggestionState === "error" ? (
                        <div className="flex h-full min-h-[280px] flex-col items-center justify-center rounded-lg border border-dashed bg-background px-6 text-center">
                          <div className="flex h-12 w-12 items-center justify-center rounded-full bg-destructive/10">
                            <RefreshCw className="h-5 w-5 text-destructive" />
                          </div>
                          <p className="mt-4 text-sm font-medium">识别失败</p>
                          <p className="mt-1 max-w-[240px] text-xs text-muted-foreground leading-relaxed">
                            当前无法获取 AI 建议，请稍后重试。
                          </p>
                        </div>
                      ) : suggestedChecklistItems.length === 0 ? (
                        <div className="flex h-full min-h-[280px] flex-col items-center justify-center rounded-lg border border-dashed bg-background px-6 text-center">
                          <div className="flex h-12 w-12 items-center justify-center rounded-full bg-muted">
                            <Lightbulb className="h-5 w-5 text-muted-foreground" />
                          </div>
                          <p className="mt-4 text-sm font-medium">暂无建议项</p>
                          <p className="mt-1 max-w-[240px] text-xs text-muted-foreground leading-relaxed">
                            当前内容不足以生成建议，可以在补充评论后再试一次。
                          </p>
                        </div>
                      ) : (
                        <div className="space-y-2">
                          {suggestedChecklistItems.map((item, index) => (
                            <button
                              key={item.id}
                              type="button"
                              onClick={() => handleSuggestionToggle(item.id)}
                              className={cn(
                                "group flex w-full items-start gap-3 rounded-lg border p-3 text-left transition-all duration-150",
                                item.selected
                                  ? "border-primary/20 bg-background shadow-sm"
                                  : "border-border/60 bg-background/60 opacity-70 hover:opacity-100",
                              )}
                            >
                              <span
                                className={cn(
                                  "mt-0.5 inline-flex h-5 w-5 shrink-0 items-center justify-center rounded-md border transition-all duration-150",
                                  item.selected
                                    ? "border-emerald-500 bg-emerald-500 text-white shadow-sm"
                                    : "border-input bg-background text-transparent group-hover:border-emerald-500/50",
                                )}
                              >
                                <CheckCircle2 className="h-3.5 w-3.5" />
                              </span>
                              <div className="min-w-0 flex-1">
                                <p className="text-sm font-medium leading-5">{item.title}</p>
                                <p className="mt-0.5 text-[11px] text-muted-foreground">
                                  建议项 {index + 1}
                                </p>
                              </div>
                            </button>
                          ))}
                        </div>
                      )}
                    </div>
                  </div>

                  {/* Right: Current Checklist Preview */}
                  <div className="flex flex-col gap-3 overflow-hidden bg-muted/10 px-6 py-5">
                    <div className="flex items-center justify-between">
                      <div className="flex items-center gap-2">
                        <ListChecks className="h-4 w-4 text-emerald-500" />
                        <span className="text-sm font-semibold">任务清单</span>
                        {checklistDraft.length > 0 && (
                          <span className="inline-flex items-center rounded-full bg-emerald-500/10 px-2 py-0.5 text-[11px] font-medium text-emerald-600 dark:text-emerald-400">
                            {checklistDraft.filter((i) => i.isCompleted).length}/
                            {checklistDraft.length} 完成
                          </span>
                        )}
                      </div>
                    </div>

                    <div className="flex-1 overflow-y-auto rounded-xl border bg-muted/20 p-3">
                      {checklistDraft.length === 0 ? (
                        <div className="flex h-full min-h-[280px] flex-col items-center justify-center rounded-lg border border-dashed bg-background px-6 text-center">
                          <div className="flex h-12 w-12 items-center justify-center rounded-full bg-muted">
                            <Inbox className="h-5 w-5 text-muted-foreground" />
                          </div>
                          <p className="mt-4 text-sm font-medium">当前还没有任务清单项</p>
                          <p className="mt-1 max-w-[200px] text-xs text-muted-foreground leading-relaxed">
                            从左侧选择建议项，追加到这里。
                          </p>
                        </div>
                      ) : (
                        <div className="space-y-2">
                          {checklistDraft.map((item, index) => (
                            <div
                              key={item.localId}
                              className="flex items-start gap-3 rounded-lg border bg-background p-3"
                            >
                              <span
                                className={cn(
                                  "mt-0.5 inline-flex h-5 w-5 shrink-0 items-center justify-center rounded-md border",
                                  item.isCompleted
                                    ? "border-emerald-500 bg-emerald-500 text-white"
                                    : "border-input bg-background text-transparent",
                                )}
                              >
                                <CheckCircle2 className="h-3.5 w-3.5" />
                              </span>
                              <div className="min-w-0 flex-1">
                                <p
                                  className={cn(
                                    "text-sm leading-5",
                                    item.isCompleted && "text-muted-foreground line-through",
                                  )}
                                >
                                  {item.title || `任务 ${index + 1}`}
                                </p>
                              </div>
                            </div>
                          ))}
                        </div>
                      )}
                    </div>
                  </div>
                </div>

                {/* Footer */}
                <DialogFooter className="shrink-0 border-t bg-muted/30 px-6 py-4 sm:justify-between">
                  <div className="hidden items-center gap-2 text-xs text-muted-foreground sm:flex">
                    {suggestionState === "ready" && suggestedChecklistItems.length > 0 ? (
                      <>
                        <CheckCircle2 className="h-3.5 w-3.5 text-emerald-500" />
                        已选择 {suggestedChecklistItems.filter((i) => i.selected).length} 项建议
                      </>
                    ) : suggestionState === "loading" ? null : (
                      <>
                        <Lightbulb className="h-3.5 w-3.5" />
                        点击建议项即可选中或取消
                      </>
                    )}
                  </div>
                  <div className="flex flex-col-reverse gap-2 sm:flex-row sm:items-center">
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      className="h-8 text-xs"
                      onClick={() => setIsSuggestionDialogOpen(false)}
                    >
                      关闭
                    </Button>
                    <Button
                      type="button"
                      size="sm"
                      className="h-8 gap-1.5 text-xs"
                      aria-label="追加到任务清单"
                      onClick={() => void handleAppendSuggestions()}
                      disabled={
                        suggestionState !== "ready" ||
                        suggestedChecklistItems.every((item) => !item.selected) ||
                        replaceChecklist.isPending
                      }
                    >
                      {replaceChecklist.isPending && (
                        <Loader2 className="h-3.5 w-3.5 animate-spin" />
                      )}
                      {replaceChecklist.isPending
                        ? "追加中..."
                        : `追加到任务清单 (${suggestedChecklistItems.filter((i) => i.selected).length})`}
                    </Button>
                  </div>
                </DialogFooter>
              </DialogContent>
            </Dialog>

          </aside>
        </div>
      </div>

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
    </>
  );
}
