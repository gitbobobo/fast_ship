import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useLocation, useNavigate, useParams, useSearchParams } from "react-router";
import {
  CheckCircle2,
  Circle,
  Copy,
  Ellipsis,
  ExternalLink,
  FileText,
  Inbox,
  Lightbulb,
  Link2,
  List,
  ListX,
  Loader2,
  MessageSquare,
  Pencil,
  PanelLeft,
  Play,
  RefreshCw,
  X,
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
import {
  describeIssueSourceContext,
  readIssueSourceContext,
} from "@/lib/issue-list-context";
import { ISSUE_SOURCE_LABELS } from "@/lib/issue-source";
import { useAuthStore } from "@/lib/store/auth-store";
import { useIsMobile } from "@/lib/hooks/use-is-mobile";
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
  IssueChecklistSuggestionsDialog,
  useIssueChecklistController,
  type IssueChecklistDraftItem,
} from "./components/issue-checklist-card";
import { IssueRecommendationBanner } from "./components/issue-recommendation-banner";
import { IssueSourceList } from "./components/issue-source-list";
import { useIssueSourceList } from "./components/use-issue-source";

// 详情页全局快捷键要避让的浮层：Dialog/AlertDialog/Sheet/下拉菜单/Popover/Select 打开时
// J/K/［ 让位浮层内部交互；来源列表抽屉是页面内普通面板（非 sheet-content），不在选择器内
const OVERLAY_SELECTOR = [
  '[data-slot="dialog-content"]',
  '[data-slot="alert-dialog-content"]',
  '[data-slot="sheet-content"]',
  '[data-slot="dropdown-menu-content"]',
  '[data-slot="popover-content"]',
  '[data-slot="select-content"]',
].join(",");

function isForeignOverlayOpen() {
  if (typeof document === "undefined") {
    return false;
  }
  return document.querySelectorAll(OVERLAY_SELECTOR).length > 0;
}

function isEditableTarget(target: EventTarget | null) {
  if (!(target instanceof HTMLElement)) {
    return false;
  }
  if (target.isContentEditable) {
    return true;
  }
  return ["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName);
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
  const issueSourceContext = useMemo(
    () => readIssueSourceContext(searchParams),
    [searchParams],
  );
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

  // 来源列表抽屉：单一开关，宽窄屏共用同一套实现；每次进页默认收起（不持久化偏好）
  const isNarrowViewport = useIsMobile(1280);
  const [isSourcePanelOpen, setIsSourcePanelOpen] = useState(false);
  const hasSourceContext =
    issueSourceContext.kind !== "none" &&
    (issue === undefined || issue.project_id === id);
  // 面板标题副行：说明来源是哪一列/哪组筛选与条数
  const sourceLabel = describeIssueSourceContext(issueSourceContext);
  const source = useIssueSourceList({
    projectId: id!,
    context: issueSourceContext,
    currentIssueId: iid ?? "",
    currentIssueProjectId: issue?.project_id,
  });
  // 条数取符合来源条件的行数（不含 stale 保留项），与副标题的筛选描述自洽；
  // 加载中/空数据加载失败时用「—」占位——不报「0」（暗示空列表是成功结果），
  // 也不留空槽（像标签被截断）；标签本身仍是来源列描述不随数据态切换。
  // 展示为「已加载 / 总数」：总数取接口首页 total（服务端过滤后的全量计数），
  // 已加载数含翻页进度语义，与无限滚动的「还在继续加载」相符
  const sourceListIdle = !(source.isLoading || (source.isError && source.items.length === 0));
  const sourceCount = sourceListIdle
    ? source.items.filter((entry) => !entry.stale).length
    : null;
  // stale 保留项不计入已加载数，但会占一行——头部单独交代，避免「2 / 2 却有 3 行」的矛盾
  const staleCount = sourceListIdle
    ? source.items.filter((entry) => entry.stale).length
    : 0;
  let sourceCountLabel = sourceCount === null
    ? "—"
    : source.total === null
      ? // total 缺失（未拉到首页/异常）时不可用已加载数伪分母：那不等于已加载完
        `已加载 ${sourceCount} / —`
      : `已加载 ${sourceCount} / ${source.total}`;
  if (sourceCount !== null && staleCount > 0) {
    sourceCountLabel += ` · 另有 ${staleCount} 条已失效`;
  }

  // 主动收起路径（[、Esc、X、窄屏点选）统一把焦点还给把手；
  // 点外部收起不走这里——焦点应落在用户点的目标上
  const closeSourcePanel = useCallback(() => {
    setIsSourcePanelOpen(false);
    // 窄屏打开态把手不渲染（头部 X 承担关闭），等把手回到 DOM 再聚焦
    requestAnimationFrame(() => {
      document.getElementById("issue-source-list-handle")?.focus();
    });
  }, []);

  const toggleSourcePanel = useCallback(() => {
    if (!hasSourceContext) {
      return;
    }
    if (isSourcePanelOpen) {
      closeSourcePanel();
    } else {
      // 打开后焦点进面板标题：面板在 DOM 中位于把手前，不移焦 Tab 会整段跳过列表
      requestAnimationFrame(() => {
        document.getElementById("issue-source-list-title")?.focus({ preventScroll: true });
      });
      setIsSourcePanelOpen(true);
    }
  }, [hasSourceContext, isSourcePanelOpen, closeSourcePanel]);

  // 来源上下文消失（同路由跳到无来源详情页或后退）时复位抽屉状态：
  // 否则 isSourcePanelOpen 残留 true，FAB 被永久隐藏、回到有来源页又莫名自动展开
  useEffect(() => {
    if (!hasSourceContext) {
      setIsSourcePanelOpen(false);
    }
  }, [hasSourceContext]);

  const {
    goToNext,
    goToPrevious,
  } = source;

  // 抽屉 JSX 提取为组件级变量：加载中与加载完成两个 return 分支都渲染在同一
  // 位置（[Header, 抽屉, 正文/骨架]），早退分支不再让抽屉复用骨架 div 的 DOM
  // 节点——否则外层容器 className 从无平移变为 -translate-x-full +
  // transition-transform，进页会播一次 0%→-100% 的收起过渡（INT-242）。
  // 抽屉与来源数据不依赖 issue 加载，加载态即可打开列表；无来源时渲染 null，
  // 三个分支的子节点位置保持一致。
  // 头部只保留两行：标题 + 来源描述·计数；当前项不再单独一行（靠列表高亮 +
  // 自动滚动定位），也让抽屉 JSX 与 issue 加载状态彻底解耦。
  const sourceDrawer = hasSourceContext ? (
    <div className="pointer-events-none sticky top-14 z-20 h-0 w-full">
      {/* 抽屉内层：贴主区左缘（侧边栏右缘）的覆盖式，几何与开合详见
          docs/internal/issue-detail-source-context.md；无来源时整块不渲染 */}
      <div
        className={cn(
          "absolute left-0 top-0 flex h-[calc(100dvh-3.5rem)] w-[300px] max-w-[85vw] transition-transform duration-150 motion-reduce:transition-none max-sm:w-[calc(100%-1.5rem)] max-sm:max-w-none",
          isSourcePanelOpen ? "translate-x-0" : "-translate-x-full",
        )}
      >
        <div
          id="issue-source-list-panel"
          role="region"
          aria-label="来源列表"
          className={cn(
            "pointer-events-auto flex min-w-0 flex-1 flex-col overflow-hidden border-r bg-card shadow-xl transition-[visibility] duration-150 motion-reduce:transition-none dark:border-neutral-600",
            isSourcePanelOpen ? "visible" : "invisible",
          )}
        >
          {/* 头部左内边距对齐列表项文字（ul p-1.5 + 项选中条 border-l-2 + px-2 = 16px；
              紧凑两行布局后未读点并入行内，旧的 pl-9≈36px 对齐依据已不存在，
              INT-242 视觉审查：头部与行文字共用同一左对齐线） */}
          <div className="flex items-start justify-between gap-2 border-b pl-4 pr-4 py-2.5">
            <div className="min-w-0">
              <h2
                id="issue-source-list-title"
                tabIndex={-1}
                className="text-sm font-semibold focus-visible:ring-2 focus-visible:ring-ring"
              >
                来源列表
              </h2>
              {/* 单一文本节点拼副行：flex 双 span 会吞掉第二段的前导空格，
                  出现「OPEN· 已加载」错位（INT-242 视觉审查）；
                  整行 truncate，标签过长时计数随尾截断——把手角标与 aria-label
                  仍带已加载数，信息不丢 */}
              <p className="mt-0.5 truncate text-xs text-muted-foreground">
                {sourceLabel} · {sourceCountLabel}
              </p>
            </div>
            {/* 窄屏抽屉近满宽，点外部只剩细条；头部给明确的关闭控件，把手与 Esc 作补充 */}
            <button
              type="button"
              aria-label="关闭来源列表"
              onClick={closeSourcePanel}
              className="-mr-2 -mt-0.5 flex h-10 w-10 shrink-0 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground sm:hidden"
            >
              <X className="h-4 w-4" />
            </button>
          </div>
          <IssueSourceList
            entries={source.items}
            currentIssueId={iid ?? ""}
            onSelect={(issue) => {
              source.goToIssue(issue);
              // 宽屏点选后保持打开便于逐条浏览；窄屏点选即收起
              if (isNarrowViewport) {
                closeSourcePanel();
              }
            }}
            isLoading={source.isLoading}
            isError={source.isError}
            onRetry={() => void source.refetch()}
            hasNextPage={source.hasNextPage}
            isFetchingNextPage={source.isFetchingNextPage}
            onLoadMore={() => {
              if (source.hasNextPage && !source.isFetchingNextPage) {
                void source.fetchNextPage();
              }
            }}
            // 看板列来源整列同一内部状态，行内不再重复展示
            showInternalStatus={issueSourceContext.kind !== "board"}
            // OPEN/CLOSED 由来源上下文稳定决定：仅列表来源未筛选状态时恒显示
            // （首帧即定，翻页/补拉不回流）；看板列固定 open、带状态筛选的列表来源不显示
            showState={
              issueSourceContext.kind === "list" && !issueSourceContext.state
            }
            token={token ?? undefined}
            className="min-h-0 flex-1 overflow-y-auto"
          />
        </div>
        {/* 左缘把手：开与关同一控件，随抽屉一起平移，打开时停在抽屉右缘。
            顶部锚定（top-8），不垂直居中——居中会落在卡片纵向中部像渲染残留。
            收起时把手落在主区左缘（贴全局侧边栏右缘；窄屏贴屏幕左缘），
            把手自身 rounded-r-md（弹层方边、把手圆角——需求方定调），
            窄屏可见宽 max-sm:w-7（28px）——它是窄屏唯一入口，16px 太难点中；
            命中区只向右扩——向左会盖住侧边栏右缘吞掉导航点击；
            打开态两侧都挨着可点内容收窄到 ±4px，
            窄屏打开态把手不渲染（头部 X 是主要关闭控件）。
            图标用 List/ListX 而非 PanelLeft*——右下角移动侧栏 FAB 也是 PanelLeft，
            同族图标并排会让用户分不清哪个开来源列表、哪个开全局侧栏；
            图标常态 neutral-600 + 14px，不依赖 hover 才可见 */}
        <button
          id="issue-source-list-handle"
          type="button"
          onClick={toggleSourcePanel}
          aria-expanded={isSourcePanelOpen}
          aria-controls="issue-source-list-panel"
          aria-label={`来源列表（快捷键 [）${sourceCount !== null ? `，已加载 ${sourceCount} 个` : ""}`}
          title="来源列表（快捷键 [）"
          className={cn(
            "group pointer-events-auto absolute left-full top-8 flex h-16 w-3.5 items-center justify-center rounded-r-md border border-l-0 border-neutral-400 bg-neutral-300 text-neutral-600 transition-[width,background-color,border-color,color] duration-150 after:absolute after:inset-y-0 after:content-[''] hover:w-4 hover:bg-neutral-400 hover:text-neutral-900 focus-visible:ring-2 focus-visible:ring-neutral-500 focus-visible:ring-offset-1 motion-reduce:transition-none max-sm:w-7 dark:border-neutral-500 dark:bg-neutral-600 dark:text-neutral-300 dark:hover:bg-neutral-500 dark:hover:text-white dark:focus-visible:ring-neutral-400",
            isSourcePanelOpen
              ? "after:-left-1 after:-right-1 max-sm:hidden"
              : "after:left-0 after:-right-4",
          )}
        >
          <span className="flex flex-col items-center gap-0.5">
            {!isSourcePanelOpen && sourceCount !== null && (
              <span className="text-[9px] font-bold leading-none">{sourceCount}</span>
            )}
            {isSourcePanelOpen ? (
              <ListX className="h-3.5 w-3.5" />
            ) : (
              <List className="h-3.5 w-3.5" />
            )}
          </span>
        </button>
        {/* 窄屏打开时右侧 ~24px 留白盖一条轻遮罩：底层卡片的描边/圆角断线
            露出来像渲染残留；遮罩不在面板内，点击仍走「点外部收起」 */}
        {isSourcePanelOpen && (
          <div className="pointer-events-auto absolute inset-y-0 left-full w-6 bg-black/10 sm:hidden dark:bg-black/40" />
        )}
      </div>
    </div>
  ) : null;

  // 全局快捷键：［ 开关来源列表抽屉，J/K 按来源列表顺序翻页；Esc 仅在无外来
  // 浮层时收起抽屉。避让：defaultPrevented、IME 组合态、修饰键、输入控件、
  // 其他 Dialog/Sheet/DropdownMenu/Popover/Select 浮层打开时。
  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.isComposing) {
        return;
      }
      if (event.metaKey || event.ctrlKey || event.altKey) {
        return;
      }
      // Esc 收起抽屉优先于输入控件避让：抽屉无遮罩，焦点可落回正文输入框；
      // 外来浮层打开时 Esc 归浮层，不动抽屉
      if (event.key === "Escape" && isSourcePanelOpen) {
        if (!isForeignOverlayOpen()) {
          event.preventDefault();
          closeSourcePanel();
        }
        return;
      }
      if (isEditableTarget(event.target) || isForeignOverlayOpen()) {
        return;
      }
      if (event.key === "[") {
        event.preventDefault();
        toggleSourcePanel();
      } else if (event.key === "j") {
        event.preventDefault();
        goToNext();
      } else if (event.key === "k") {
        event.preventDefault();
        goToPrevious();
      }
    };
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [toggleSourcePanel, closeSourcePanel, goToNext, goToPrevious, isSourcePanelOpen]);

  // 抽屉无遮罩：打开期间点击面板与把手以外区域（正文、右栏等）直接收起
  useEffect(() => {
    if (!isSourcePanelOpen) {
      return;
    }
    const handlePointerDown = (event: PointerEvent) => {
      if (!(event.target instanceof Node)) {
        return;
      }
      const panel = document.getElementById("issue-source-list-panel");
      const handle = document.getElementById("issue-source-list-handle");
      if (panel?.contains(event.target) || handle?.contains(event.target)) {
        return;
      }
      setIsSourcePanelOpen(false);
    };
    document.addEventListener("pointerdown", handlePointerDown);
    return () => document.removeEventListener("pointerdown", handlePointerDown);
  }, [isSourcePanelOpen]);
  // 命中推荐时在正文卡片上方展示横幅；queryKey 与看板弹框同源，命中缓存
  const { data: recommendationsData } = useRecommendations(id);
  const recommendation = useMemo(
    () =>
      recommendationsData?.items.find((entry) => entry.issue.id === iid) ??
      null,
    [recommendationsData, iid],
  );
  const updateIssue = useUpdateIssue(iid!, id);
  const updateInternalMeta = useUpdateIssueInternalMeta(iid!, id);
  const createComment = useCreateIssueComment(iid!, id);
  const replaceChecklist = useReplaceIssueChecklist(iid!, id);
  const suggestChecklist = useIssueChecklistSuggestions(iid!);
  const isInternalIssue = issue?.source === "internal";
  const shouldShowLocalAssetNotice =
    issue?.source === "github" && containsLocalIssueAssetReference(issue.body);
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

  // 清单状态只在 route 创建一次：桌面 aside 与移动 Sheet 的两份卡片共享同一份 draft/编辑态/建议弹框
  const checklistController = useIssueChecklistController({
    items: persistedChecklist,
    updatedAt: issue?.internal_meta?.checklist_updated_at ?? null,
    isSaving: replaceChecklist.isPending,
    onSave: handleSaveChecklist,
    requestSuggestions: requestChecklistSuggestions,
    onConfigureAi: handleConfigureAi,
  });

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

  // 加载态与完成态共用同一抽屉渲染（sourceDrawer），抽屉与来源数据不依赖 issue，
  // 加载中即可开抽屉看列表；骨架分支也不再与抽屉争抢同一 DOM 位置
  if (isLoading) {
    return (
      <>
        <Header title="问题详情" />
        {sourceDrawer}
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
        {sourceDrawer}
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

      <IssueChecklistCard controller={checklistController} />
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
      {/* 来源列表抽屉：贴主区左缘（侧边栏右缘）的覆盖式。sticky 容器铺满 main 宽度，
          left-0 即紧贴全局侧边栏右缘——窄屏则贴屏幕左缘；与正文卡片解耦，
          不随 max-w-7xl 居中移动。贴在顶栏下方且不占文档流高度，
          只覆盖内容区（不盖全局侧栏与顶栏）；无全屏遮罩，开着列表也能看清正文
          （窄屏右侧 24px 露出区有一条轻遮罩挡卡片断线，见面板后注释）。
          JSX 本体提取为组件级 sourceDrawer（见上方定义）：加载中与加载完成两个
          return 分支都渲染在同一位置，防进页时抽屉复用骨架 DOM 播收起动画 */}
      {sourceDrawer}
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
                            e.preventDefault();
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

      {/* Mobile sidebar floating button；来源抽屉打开时退场，
          避免深色 FAB 浮在面板之上与把手抢 PanelLeft 语义。
          联动 hasSourceContext：防来源参数消失后抽屉卸载但 FAB 仍被压状态 */}
      {!(isSourcePanelOpen && hasSourceContext) && (
        <div className="fixed bottom-6 right-6 z-40 lg:hidden">
          <Button
            size="icon"
            className="h-12 w-12 rounded-full shadow-lg"
            onClick={() => setIsMobileSidebarOpen(true)}
          >
            <PanelLeft className="h-5 w-5" />
          </Button>
        </div>
      )}

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

      {/* 清单建议弹框全局只渲染一份，由 checklistController 驱动 */}
      <IssueChecklistSuggestionsDialog controller={checklistController} />

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
