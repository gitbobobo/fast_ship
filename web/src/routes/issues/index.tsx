import { useDeferredValue, useMemo, useState, useEffect } from "react";
import { Link, useLocation, useNavigate, useSearchParams } from "react-router";
import { useProjectPreferenceStore } from "@/lib/store/project-preference-store";
import {
  Bug,
  ChevronLeft,
  ChevronRight,
  Copy,
  Ellipsis,
  ExternalLink,
  Inbox,
  Link2,
  MessageSquare,
  Package,
  Pencil,
  Plus,
  RefreshCw,
  RotateCcw,
  Search,
  Clock,
  CheckCircle2,
} from "lucide-react";
import { Header } from "@/components/layout/header";
import { HeaderActions } from "@/components/layout/header-actions";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Card,
  CardContent,
} from "@/components/ui/card";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
  DropdownMenuSeparator,
} from "@/components/ui/dropdown-menu";
import { Skeleton } from "@/components/ui/skeleton";
import { useProjects } from "@/lib/hooks/use-projects";
import {
  useIssueFilterOptions,
  useIssues,
  useSyncProjectIssues,
  useUpdateIssue,
} from "@/lib/hooks/use-issues";
import {
  ISSUE_WORKFLOW_STATUS_LABELS,
  ISSUE_WORKFLOW_STATUS_SELECT_OPTIONS,
  type IssueWorkflowStatus,
} from "@/lib/issue-workflow-status";
import { buildIssueDetailSearchParams } from "@/lib/issue-list-context";
import { ISSUE_SOURCE_LABELS } from "@/lib/issue-source";
import { IssueShipHookBadge } from "@/components/issues/issue-ship-hook-badge";
import { cn } from "@/lib/utils";
import { copyWithToast } from "@/lib/copy";
import { ensureGitHubLinked, hasGitHubRepo } from "@/lib/utils/github";
import { formatRelativeTime } from "@/lib/utils/format";
import { toast } from "sonner";

function IssueProgressBadge({
  progress,
}: {
  progress?: number | null;
}) {
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
        "inline-flex items-center rounded-full border px-2 py-0.5 text-xs font-medium",
        className,
      )}
    >
      进度 {progress}%
    </span>
  );
}

function IssueWorkflowStatusBadge({
  status,
}: {
  status?: string | null;
}) {
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
        "inline-flex items-center rounded-full border px-2 py-0.5 text-xs font-medium",
        className,
      )}
    >
      {label}
    </span>
  );
}

function getActiveProjectId(
  projects: Project[],
  selectedId: string,
  urlId: string | null,
): string {
  if (projects.some((p) => p.id === selectedId)) {
    return selectedId;
  }
  if (urlId && projects.some((p) => p.id === urlId)) {
    return urlId;
  }
  return projects[0]?.id ?? "";
}

function IssueCardActions({
  issue,
  issueDetailSearch,
  syncIssues,
}: {
  issue: Issue;
  issueDetailSearch: string;
  syncIssues: ReturnType<typeof useSyncProjectIssues>;
}) {
  const navigate = useNavigate();
  const location = useLocation();
  const updateIssue = useUpdateIssue(issue.id, issue.project_id);

  const handleToggleIssueState = async () => {
    try {
      await updateIssue.mutateAsync({
        state: issue.state === "open" ? "closed" : "open",
      });
      toast.success(issue.state === "open" ? "问题已关闭" : "问题已重新打开");
    } catch {
      toast.error(issue.state === "open" ? "关闭问题失败" : "重新打开问题失败");
    }
  };

  const handleCopyIssueLink = async () => {
    const url = new URL(
      `/projects/${issue.project_id}/issues/${issue.id}`,
      window.location.origin,
    );
    if (issueDetailSearch) {
      url.search = issueDetailSearch;
    }
    await copyWithToast(url.toString(), "已复制当前问题链接");
  };

  const handleCopyGitHubLink = async () => {
    if (!issue.github?.html_url) {
      toast.error("复制失败");
      return;
    }
    await copyWithToast(issue.github.html_url, "已复制 GitHub 深链接");
  };

  const handleSync = async () => {
    try {
      const res = await syncIssues.mutateAsync();
      toast.success(
        `已触发项目同步：${res.data.synced_issue_count} 个问题，${res.data.synced_comment_count} 条评论`,
      );
    } catch {
      toast.error("同步失败，请检查 GitHub 仓库配置和 Token");
    }
  };

  const isInternalIssue = issue.source === "internal";

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        onClick={(event) => {
          event.preventDefault();
          event.stopPropagation();
        }}
        className="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-sm font-medium transition-colors hover:bg-accent hover:text-accent-foreground focus-visible:outline-hidden focus-visible:ring-1 focus-visible:ring-ring"
        aria-label="更多操作"
      >
        <Ellipsis className="h-3.5 w-3.5" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-44">
        {isInternalIssue && (
          <DropdownMenuItem
            onClick={(event) => {
              event.preventDefault();
              event.stopPropagation();
              navigate({
                pathname: `/projects/${issue.project_id}/issues/${issue.id}/edit`,
                search: location.search,
              });
            }}
          >
            <Pencil className="h-4 w-4" />
            编辑问题
          </DropdownMenuItem>
        )}
        <DropdownMenuItem
          onClick={(event) => {
            event.preventDefault();
            event.stopPropagation();
            void handleToggleIssueState();
          }}
          disabled={updateIssue.isPending}
        >
          {issue.state === "open" ? <CheckCircle2 className="h-4 w-4" /> : <Inbox className="h-4 w-4" />}
          {issue.state === "open" ? "关闭问题" : "重新打开"}
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuItem
          onClick={(event) => {
            event.preventDefault();
            event.stopPropagation();
            void handleCopyIssueLink();
          }}
        >
          <Copy className="h-4 w-4" />
          复制链接
        </DropdownMenuItem>
        {issue.github?.html_url && (
          <DropdownMenuItem
            onClick={(event) => {
              event.preventDefault();
              event.stopPropagation();
              void handleCopyGitHubLink();
            }}
          >
            <Link2 className="h-4 w-4" />
            复制 GitHub 深链接
          </DropdownMenuItem>
        )}
        {issue.source === "github" && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              onClick={(event) => {
                event.preventDefault();
                event.stopPropagation();
                void handleSync();
              }}
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
                  onClick={(event) => event.stopPropagation()}
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
  );
}

export default function IssuesPage() {
  const { data: projectsData, isLoading: projectsLoading } = useProjects();
  const projects = useMemo(() => projectsData?.items ?? [], [projectsData]);

  const [searchParams, setSearchParams] = useSearchParams();
  const urlProjectId = searchParams.get("project");
  const { lastSelectedProjectId, setLastSelectedProjectId } =
    useProjectPreferenceStore();

  const [selectedProjectId, setSelectedProjectId] = useState<string>(
    () => urlProjectId ?? lastSelectedProjectId ?? "",
  );

  const activeProjectId = useMemo(
    () => getActiveProjectId(projects, selectedProjectId, urlProjectId),
    [projects, selectedProjectId, urlProjectId],
  );

  const activeProject = useMemo(
    () => projects.find((p) => p.id === activeProjectId),
    [projects, activeProjectId],
  );

  // 当 activeProjectId 回退时（如存储的项目被删除），同步 state 和 store
  useEffect(() => {
    if (!projectsLoading && activeProjectId !== selectedProjectId) {
      setSelectedProjectId(activeProjectId);
      setLastSelectedProjectId(activeProjectId || null);
    }
  }, [
    projectsLoading,
    activeProjectId,
    selectedProjectId,
    setLastSelectedProjectId,
  ]);

  // Sync URL when active project changes from non-URL sources (e.g. first load)
  // Also ensure default state filter is "open" when not specified
  // Skip when user manually changes project (selectedProjectId matches activeProjectId)
  useEffect(() => {
    if (
      activeProjectId &&
      activeProjectId !== urlProjectId &&
      selectedProjectId !== activeProjectId
    ) {
      const next = new URLSearchParams(searchParams);
      next.set("project", activeProjectId);
      next.delete("page");
      if (!next.has("state")) {
        next.set("state", "open");
      }
      setSearchParams(next, { replace: true });
    }
  }, [activeProjectId, urlProjectId, searchParams, setSearchParams, selectedProjectId]);

  // Filters from URL
  const issueStateFilter = searchParams.get("state") ?? "open";
  const issueQuery = searchParams.get("q") ?? "";
  const issueLabelFilter = searchParams.get("label") ?? "all";
  const issueSourceFilter = searchParams.get("source") ?? "all";
  const issueWorkflowFilter = searchParams.get("workflow_status") ?? "all";
  const issueSort = searchParams.get("sort") ?? "updated_desc";
  const issuePage = Math.max(
    Number(searchParams.get("page") ?? "1") || 1,
    1,
  );
  const deferredIssueQuery = useDeferredValue(issueQuery.trim());

  const { data: issuesData, isLoading: issuesLoading } = useIssues(
    activeProjectId,
    {
      state: issueStateFilter === "all" ? undefined : issueStateFilter,
      q: deferredIssueQuery || undefined,
      label: issueLabelFilter === "all" ? undefined : issueLabelFilter,
      source: issueSourceFilter === "all" ? undefined : issueSourceFilter,
      workflow_status:
        issueWorkflowFilter === "all" ? undefined : issueWorkflowFilter,
      sort: issueSort,
      page: issuePage,
      page_size: 20,
    },
  );

  const { data: filterOptionsData, isLoading: filtersLoading } =
    useIssueFilterOptions(activeProjectId);

  const syncIssues = useSyncProjectIssues(activeProjectId);

  const issues = issuesData?.items ?? [];
  const issueTotal = issuesData?.total ?? 0;
  const issuePageSize = issuesData?.page_size ?? 20;
  const issueTotalPages = Math.max(Math.ceil(issueTotal / issuePageSize), 1);

  const labels = filterOptionsData?.labels ?? [];
  const issueSync = activeProject?.issue_sync;

  const updateSearchParams = (
    updates: Record<string, string | null>,
    resetPage = false,
  ) => {
    const next = new URLSearchParams(searchParams);
    for (const [key, value] of Object.entries(updates)) {
      if (!value || value === "all") {
        next.delete(key);
      } else {
        next.set(key, value);
      }
    }
    if (resetPage) {
      next.delete("page");
    }
    setSearchParams(next, { replace: true });
  };

  const handleProjectChange = (value: string | null) => {
    const nextValue = value ?? "";
    setSelectedProjectId(nextValue);
    setLastSelectedProjectId(nextValue || null);
    const next = new URLSearchParams();
    if (nextValue) {
      next.set("project", nextValue);
    }
    setSearchParams(next, { replace: true });
  };

  const handleResetFilters = () => {
    const next = new URLSearchParams();
    if (activeProjectId) {
      next.set("project", activeProjectId);
    }
    setSearchParams(next, { replace: true });
  };

  const canResetFilters =
    issueQuery.trim().length > 0 ||
    issueLabelFilter !== "all" ||
    issueSourceFilter !== "all" ||
    issueWorkflowFilter !== "all" ||
    issueSort !== "updated_desc" ||
    issueStateFilter !== "open" ||
    issuePage > 1;

  const handleSync = async () => {
    try {
      const res = await syncIssues.mutateAsync();
      toast.success(
        `同步完成：${res.data.synced_issue_count} 个问题，${res.data.synced_comment_count} 条评论`,
      );
    } catch {
      toast.error("同步失败，请检查 GitHub 仓库配置和 Token");
    }
  };

  const hasActiveFilters =
    issueStateFilter !== "all" ||
    issueLabelFilter !== "all" ||
    issueSourceFilter !== "all" ||
    issueWorkflowFilter !== "all" ||
    deferredIssueQuery.length > 0;
  const issueDetailSearch = buildIssueDetailSearchParams({
    state: issueStateFilter,
    q: issueQuery,
    label: issueLabelFilter,
    source: issueSourceFilter,
    workflowStatus: issueWorkflowFilter,
    sort: issueSort,
    page: issuePage,
  }).toString();

  return (
    <>
      <Header
        title="问题"
        actions={
          activeProjectId ? (
            <HeaderActions
              primary={
                <Button
                  variant="outline"
                  size="sm"
                  render={
                    <Link
                      to={{
                        pathname: `/projects/${activeProjectId}/issues/new`,
                        search: searchParams.toString()
                          ? `?${searchParams.toString()}`
                          : "",
                      }}
                    />
                  }
                >
                  <Plus className="mr-1.5 h-3.5 w-3.5" />
                  新建问题
                </Button>
              }
              secondary={[
                <Button
                  key="sync"
                  variant="outline"
                  size="sm"
                  onClick={() => {
                    if (ensureGitHubLinked(activeProject, "同步 Issue")) handleSync();
                  }}
                  disabled={syncIssues.isPending}
                >
                  {syncIssues.isPending ? (
                    <RefreshCw className="mr-1.5 h-3.5 w-3.5 animate-spin" />
                  ) : (
                    <svg
                      className="mr-1.5 h-3.5 w-3.5"
                      viewBox="0 0 24 24"
                      fill="currentColor"
                      aria-hidden="true"
                    >
                      <path d="M12 .297c-6.63 0-12 5.373-12 12 0 5.303 3.438 9.8 8.205 11.385.6.113.82-.258.82-.577 0-.285-.01-1.04-.015-2.04-3.338.724-4.042-1.61-4.042-1.61C4.422 18.07 3.633 17.7 3.633 17.7c-1.087-.744.084-.729.084-.729 1.205.084 1.838 1.236 1.838 1.236 1.07 1.835 2.809 1.305 3.495.998.108-.776.417-1.305.76-1.605-2.665-.3-5.466-1.332-5.466-5.93 0-1.31.465-2.38 1.235-3.22-.135-.303-.54-1.523.105-3.176 0 0 1.005-.322 3.3 1.23.96-.267 1.98-.399 3-.405 1.02.006 2.04.138 3 .405 2.28-1.552 3.285-1.23 3.285-1.23.645 1.653.24 2.873.12 3.176.765.84 1.23 1.91 1.23 3.22 0 4.61-2.805 5.625-5.475 5.92.42.36.81 1.096.81 2.22 0 1.606-.015 2.896-.015 3.286 0 .315.21.69.825.57C20.565 22.092 24 17.592 24 12.297c0-6.627-5.373-12-12-12" />
                    </svg>
                  )}
                  {syncIssues.isPending ? "同步中..." : "同步"}
                </Button>,
                ...(hasGitHubRepo(activeProject)
                  ? [
                      <Button
                        key="github"
                        variant="outline"
                        size="icon-sm"
                        aria-label="在 GitHub 打开仓库"
                        render={
                          <a
                            href={`https://github.com/${activeProject!.github_owner}/${activeProject!.github_repo}`}
                            target="_blank"
                            rel="noopener noreferrer"
                          />
                        }
                      >
                        <ExternalLink className="h-3.5 w-3.5" />
                      </Button>,
                    ]
                  : []),
              ]}
            />
          ) : undefined
        }
      />
      <div className="p-4 md:p-6 space-y-6">
        {/* Project selector + sync status */}
        <div className="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
          <div className="flex flex-col gap-3 sm:flex-row sm:items-center">
            {projectsLoading ? (
              <Skeleton className="h-10 w-48 rounded-md" />
            ) : projects.length === 0 ? (
              <p className="text-sm text-muted-foreground">暂无项目</p>
            ) : (
              <Select
                value={activeProjectId}
                onValueChange={handleProjectChange}
              >
                <SelectTrigger className="w-auto min-w-32">
                  <SelectValue placeholder="请选择项目">
                    {activeProject?.name}
                  </SelectValue>
                </SelectTrigger>
                <SelectContent>
                  {projects.map((project) => (
                    <SelectItem key={project.id} value={project.id}>
                      {project.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}

            {activeProject && issueSync && (
              <span
                className={cn(
                  "max-w-xs truncate text-xs text-muted-foreground",
                  issueSync.status === "failed" && "text-destructive",
                )}
                title={
                  issueSync.status === "failed" && issueSync.last_error
                    ? issueSync.last_error
                    : undefined
                }
              >
                {issueSync.status === "running"
                  ? "同步中…"
                  : issueSync.status === "failed"
                    ? `同步失败${issueSync.last_error ? ` · ${issueSync.last_error}` : ""}`
                    : issueSync.status === "completed" &&
                        issueSync.last_synced_at
                      ? `${formatRelativeTime(issueSync.last_synced_at)}同步完成`
                      : "未同步"}
              </span>
            )}
          </div>
        </div>

        {/* Filters */}
        {activeProjectId && (
          <div className="flex flex-wrap items-center gap-2">
            <div className="relative">
              <Search className="pointer-events-none absolute top-1/2 left-2.5 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={issueQuery}
                onChange={(e) =>
                  updateSearchParams(
                    { q: e.target.value.trim() ? e.target.value : null },
                    true,
                  )
                }
                placeholder="搜索标题、编号或作者"
                className="h-8 w-full border-0 bg-muted/50 pl-8 text-sm placeholder:text-muted-foreground/70 hover:bg-muted sm:w-56"
              />
            </div>
            <Select
              value={issueStateFilter}
              onValueChange={(value) =>
                updateSearchParams({ state: value ?? null }, true)
              }
            >
              <SelectTrigger className="h-8 border-0 bg-muted/50 text-sm hover:bg-muted data-[state=open]:bg-muted">
                <SelectValue placeholder="全部状态">
                  {{
                    all: "状态",
                    open: "开启",
                    closed: "关闭",
                  }[issueStateFilter] ?? issueStateFilter}
                </SelectValue>
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">全部状态</SelectItem>
                <SelectItem value="open">开启</SelectItem>
                <SelectItem value="closed">关闭</SelectItem>
              </SelectContent>
            </Select>
            <Select
              value={issueLabelFilter}
              onValueChange={(value) =>
                updateSearchParams({ label: value ?? null }, true)
              }
              disabled={filtersLoading || labels.length === 0}
            >
              <SelectTrigger className="h-8 border-0 bg-muted/50 text-sm hover:bg-muted data-[state=open]:bg-muted">
                <SelectValue placeholder="全部标签">
                  {issueLabelFilter === "all" ? "标签" : issueLabelFilter}
                </SelectValue>
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">全部标签</SelectItem>
                {labels.map((label) => (
                  <SelectItem key={label} value={label}>
                    {label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Select
              value={issueSourceFilter}
              onValueChange={(value) =>
                updateSearchParams({ source: value ?? null }, true)
              }
            >
              <SelectTrigger className="h-8 border-0 bg-muted/50 text-sm hover:bg-muted data-[state=open]:bg-muted">
                <SelectValue placeholder="全部来源">
                  {issueSourceFilter === "all"
                    ? "来源"
                    : issueSourceFilter === "github"
                      ? "GitHub"
                      : "内部"}
                </SelectValue>
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">全部来源</SelectItem>
                <SelectItem value="internal">内部</SelectItem>
                <SelectItem value="github">GitHub</SelectItem>
              </SelectContent>
            </Select>
            <Select
              value={issueWorkflowFilter}
              onValueChange={(value) =>
                updateSearchParams({ workflow_status: value ?? null }, true)
              }
            >
              <SelectTrigger className="h-8 border-0 bg-muted/50 text-sm hover:bg-muted data-[state=open]:bg-muted">
                <SelectValue placeholder="内部状态">
                  {issueWorkflowFilter === "all"
                    ? "内部状态"
                    : issueWorkflowFilter === "unset"
                      ? "未设置"
                      : ISSUE_WORKFLOW_STATUS_LABELS[
                          issueWorkflowFilter as keyof typeof ISSUE_WORKFLOW_STATUS_LABELS
                        ] ?? issueWorkflowFilter}
                </SelectValue>
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">全部内部状态</SelectItem>
                <SelectItem value="unset">未设置</SelectItem>
                {ISSUE_WORKFLOW_STATUS_SELECT_OPTIONS.map((option) => (
                  <SelectItem key={option.value} value={option.value}>
                    {option.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Select
              value={issueSort}
              onValueChange={(value) =>
                updateSearchParams({ sort: value ?? null }, true)
              }
            >
              <SelectTrigger className="h-8 border-0 bg-muted/50 text-sm hover:bg-muted data-[state=open]:bg-muted">
                <SelectValue placeholder="排序方式">
                  {{
                    updated_desc: "最近更新",
                    updated_asc: "最早更新",
                    created_desc: "最新创建",
                    created_asc: "最早创建",
                    comments_desc: "评论最多",
                    comments_asc: "评论最少",
                  }[issueSort] ?? "排序"}
                </SelectValue>
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="updated_desc">最近更新</SelectItem>
                <SelectItem value="updated_asc">最早更新</SelectItem>
                <SelectItem value="created_desc">最新创建</SelectItem>
                <SelectItem value="created_asc">最早创建</SelectItem>
                <SelectItem value="comments_desc">评论最多</SelectItem>
                <SelectItem value="comments_asc">评论最少</SelectItem>
              </SelectContent>
            </Select>
            <Button
              variant={canResetFilters ? "outline" : "ghost"}
              size="sm"
              onClick={handleResetFilters}
              disabled={!canResetFilters}
              className="h-8 px-2"
            >
              <RotateCcw className="mr-1.5 h-3.5 w-3.5" />
              重置
            </Button>
          </div>
        )}

        {/* Issues list */}
        {projectsLoading || issuesLoading ? (
          <div className="space-y-3">
            {Array.from({ length: 4 }).map((_, i) => (
              <Skeleton key={i} className="h-24 rounded-lg" />
            ))}
          </div>
        ) : projects.length === 0 ? (
          <Card>
            <CardContent className="flex flex-col items-center justify-center py-16 text-center">
              <Package className="mb-4 h-12 w-12 text-muted-foreground/50" />
              <h3 className="text-lg font-medium">暂无项目</h3>
              <p className="mt-1 text-sm text-muted-foreground">
                创建项目后即可同步和管理 GitHub Issues
              </p>
            </CardContent>
          </Card>
        ) : !activeProjectId ? (
          <Card>
            <CardContent className="flex flex-col items-center justify-center py-16 text-center">
              <Package className="mb-4 h-12 w-12 text-muted-foreground/50" />
              <h3 className="text-lg font-medium">请选择项目</h3>
              <p className="mt-1 text-sm text-muted-foreground">
                从上方下拉菜单选择一个项目以查看问题列表
              </p>
            </CardContent>
          </Card>
        ) : issues.length === 0 ? (
          <Card>
            <CardContent className="flex flex-col items-center justify-center py-16 text-center">
              <Bug className="mb-4 h-12 w-12 text-muted-foreground/50" />
              <h3 className="text-lg font-medium">
                {hasActiveFilters ? "没有匹配的问题" : "暂无问题"}
              </h3>
              <p className="mt-1 text-sm text-muted-foreground">
                {hasActiveFilters
                  ? "调整筛选条件后再试"
                  : "可以新建内部问题，或从 GitHub 拉取问题数据"}
              </p>
            </CardContent>
          </Card>
        ) : (
          <div className="space-y-3">
            {issues.map((issue) => (
              <Link
                key={issue.id}
                to={{
                  pathname: `/projects/${activeProjectId}/issues/${issue.id}`,
                  search: issueDetailSearch ? `?${issueDetailSearch}` : "",
                }}
                className="group block"
              >
                <Card className="transition-all hover:border-primary/50 hover:shadow-sm">
                  <CardContent className="relative py-4 pr-16 md:pr-20">
                    {/* Hover actions */}
                    <div className="absolute top-3 right-3 flex items-center gap-1 md:opacity-0 md:transition-opacity md:group-hover:opacity-100">
                      <IssueCardActions
                        issue={issue}
                        issueDetailSearch={issueDetailSearch}
                        syncIssues={syncIssues}
                      />
                    </div>

                    <div className="flex flex-col gap-3 md:flex-row md:items-start md:justify-between">
                      <div className="min-w-0 flex-1 space-y-2">
                        {/* Title + badges */}
                        <div className="flex flex-wrap items-center gap-2">
                          <span className="font-medium">
                            {issue.reference} {issue.title}
                          </span>
                          <Badge variant="outline">
                            {ISSUE_SOURCE_LABELS[issue.source]}
                          </Badge>
                          <Badge
                            variant={
                              issue.state === "open" ? "default" : "secondary"
                            }
                          >
                            {issue.state === "open" ? "Open" : "Closed"}
                          </Badge>
                          <IssueProgressBadge
                            progress={issue.internal_meta?.progress_percent}
                          />
                          <IssueWorkflowStatusBadge
                            status={issue.internal_meta?.workflow_status}
                          />
                          <IssueShipHookBadge hook={issue.ship_hook} />
                          {(issue.source === "github"
                            ? issue.github?.labels ?? []
                            : issue.internal_meta?.labels ?? []
                          ).slice(0, 4).map((label) => (
                            <span
                              key={label.name}
                              className="rounded-full px-2 py-0.5 text-xs"
                              style={{
                                backgroundColor: `#${label.color}20`,
                                color: `#${label.color}`,
                              }}
                            >
                              {label.name}
                            </span>
                          ))}
                          {(issue.source === "github"
                            ? (issue.github?.labels?.length ?? 0)
                            : (issue.internal_meta?.labels?.length ?? 0)
                          ) > 4 && (
                            <span className="text-xs text-muted-foreground">
                              +{(issue.source === "github"
                                ? (issue.github?.labels?.length ?? 0)
                                : (issue.internal_meta?.labels?.length ?? 0)
                              ) - 4}
                            </span>
                          )}
                        </div>

                        {/* Meta row */}
                        <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
                          <span className="inline-flex items-center gap-1">
                            <span className="font-medium text-foreground">
                              @{issue.author.login}
                            </span>
                            创建于 {formatRelativeTime(issue.created_at)}
                          </span>
                          {(issue.github?.assignees?.length ?? 0) > 0 && (
                            <span className="inline-flex items-center gap-1">
                              <CheckCircle2 className="h-3 w-3" />
                              指派给 {issue.github?.assignees.map((a) => a.login).join(", ")}
                            </span>
                          )}
                          {issue.github?.milestone && (
                            <span className="inline-flex items-center gap-1">
                              <Clock className="h-3 w-3" />
                              {issue.github.milestone.title}
                            </span>
                          )}
                          {issue.github && (
                            <span className="inline-flex items-center gap-1">
                              <MessageSquare className="h-3 w-3" />
                              {issue.github.comments_count} 条评论
                            </span>
                          )}
                        </div>
                      </div>

                    </div>

                    {/* Bottom-right: updated time */}
                    <div className="absolute bottom-4 right-3 text-right">
                      <p className="text-xs text-muted-foreground">
                        更新于 {formatRelativeTime(issue.updated_at)}
                      </p>
                    </div>
                  </CardContent>
                </Card>
              </Link>
            ))}
          </div>
        )}

        {/* Pagination */}
        {issues.length > 0 && (
          <div className="flex items-center justify-between gap-3">
            <p className="text-sm text-muted-foreground">
              第 {issuePage} / {issueTotalPages} 页，共 {issueTotal} 条
            </p>
            <div className="flex gap-2">
              <Button
                variant="outline"
                size="sm"
                onClick={() =>
                  updateSearchParams(
                    { page: issuePage > 1 ? String(issuePage - 1) : null },
                    false,
                  )
                }
                disabled={issuePage <= 1}
              >
                <ChevronLeft className="mr-1 h-3.5 w-3.5" />
                上一页
              </Button>
              <Button
                variant="outline"
                size="sm"
                onClick={() =>
                  updateSearchParams(
                    {
                      page:
                        issuePage < issueTotalPages
                          ? String(issuePage + 1)
                          : String(issuePage),
                    },
                    false,
                  )
                }
                disabled={issuePage >= issueTotalPages}
              >
                下一页
                <ChevronRight className="ml-1 h-3.5 w-3.5" />
              </Button>
            </div>
          </div>
        )}
      </div>

    </>
  );
}
