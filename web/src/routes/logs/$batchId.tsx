import { useDeferredValue, useEffect, useMemo, useState } from "react";
import {
  Link,
  useNavigate,
  useParams,
  useSearchParams,
} from "react-router";
import {
  ArrowLeft,
  Copy,
  Inbox,
  RotateCcw,
  Search,
  Trash2,
} from "lucide-react";
import { HTTPError } from "ky";
import { Header } from "@/components/layout/header";
import { HeaderActions } from "@/components/layout/header-actions";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Card, CardContent } from "@/components/ui/card";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Skeleton } from "@/components/ui/skeleton";
import { useProjects } from "@/lib/hooks/use-projects";
import {
  useDeleteLogBatch,
  useInfiniteLogEntries,
  useLogBatch,
} from "@/lib/hooks/use-logs";
import { useProjectPreferenceStore } from "@/lib/store/project-preference-store";
import { copyWithToast } from "@/lib/copy";
import { cn } from "@/lib/utils";
import { toast } from "sonner";

const LOG_LEVELS = [
  { value: "all", label: "全部级别" },
  { value: "debug", label: "Debug" },
  { value: "info", label: "Info" },
  { value: "warn", label: "Warn" },
  { value: "error", label: "Error" },
  { value: "fatal", label: "Fatal" },
] as const;

function LogLevelBadge({ level }: { level: string }) {
  const className =
    level === "error" || level === "fatal"
      ? "border-red-500/20 bg-red-500/10 text-red-700 dark:text-red-300"
      : level === "warn"
        ? "border-amber-500/20 bg-amber-500/10 text-amber-700 dark:text-amber-300"
        : level === "debug"
          ? "border-slate-500/20 bg-slate-500/10 text-slate-600 dark:text-slate-400"
          : "border-blue-500/20 bg-blue-500/10 text-blue-700 dark:text-blue-300";

  return (
    <span
      className={cn(
        "inline-flex items-center rounded-full border px-2 py-0.5 text-xs font-medium uppercase",
        className,
      )}
    >
      {level}
    </span>
  );
}

function formatLogMetadata(raw: string): string {
  try {
    return JSON.stringify(JSON.parse(raw), null, 2);
  } catch {
    return raw;
  }
}

function formatTime(value: string | null): string {
  if (!value) return "—";
  return new Date(value).toLocaleString();
}

export default function LogBatchDetailPage() {
  const { batchId = "" } = useParams<{ batchId: string }>();
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const { setLastSelectedProjectId } = useProjectPreferenceStore();
  const { data: projectsData } = useProjects();
  const projects = useMemo(() => projectsData?.items ?? [], [projectsData]);

  const {
    data: batch,
    isLoading: batchLoading,
    isError: batchError,
    error: batchQueryError,
  } = useLogBatch(batchId);

  const projectId = batch?.project_id ?? "";
  const activeProject = useMemo(
    () => projects.find((p) => p.id === projectId),
    [projects, projectId],
  );

  useEffect(() => {
    if (!batch) return;
    setLastSelectedProjectId(batch.project_id);
    const urlProject = searchParams.get("project");
    if (urlProject !== batch.project_id) {
      const next = new URLSearchParams(searchParams);
      next.set("project", batch.project_id);
      setSearchParams(next, { replace: true });
    }
  }, [batch, searchParams, setSearchParams, setLastSelectedProjectId]);

  const levelFilter = searchParams.get("level") ?? "all";
  const queryFilter = searchParams.get("q") ?? "";
  const deferredQuery = useDeferredValue(queryFilter.trim());

  const {
    data: infiniteData,
    isLoading: entriesLoading,
    isError: entriesError,
    fetchNextPage,
    hasNextPage,
    isFetchingNextPage,
  } = useInfiniteLogEntries(projectId, {
    batch_id: batchId,
    level: levelFilter === "all" ? undefined : levelFilter,
    q: deferredQuery || undefined,
  });

  const entries = useMemo(
    () => infiniteData?.pages.flatMap((page) => page.items) ?? [],
    [infiniteData?.pages],
  );
  const total = infiniteData?.pages[0]?.total ?? 0;

  const [loadMoreRef, setLoadMoreRef] = useState<HTMLDivElement | null>(null);

  useEffect(() => {
    if (!loadMoreRef || !hasNextPage) return;
    if (typeof IntersectionObserver === "undefined") return;

    const observer = new IntersectionObserver(
      ([entry]) => {
        if (entry.isIntersecting && hasNextPage && !isFetchingNextPage) {
          void fetchNextPage();
        }
      },
      { rootMargin: "200px" },
    );
    observer.observe(loadMoreRef);
    return () => observer.disconnect();
  }, [loadMoreRef, hasNextPage, isFetchingNextPage, fetchNextPage]);

  const deleteBatch = useDeleteLogBatch(projectId);

  const updateSearchParams = (updates: Record<string, string | null>) => {
    const next = new URLSearchParams(searchParams);
    for (const [key, value] of Object.entries(updates)) {
      if (!value) next.delete(key);
      else next.set(key, value);
    }
    setSearchParams(next, { replace: true });
  };

  const handleResetFilters = () => {
    const next = new URLSearchParams();
    if (projectId) next.set("project", projectId);
    setSearchParams(next, { replace: true });
  };

  const canResetFilters =
    levelFilter !== "all" || queryFilter.trim().length > 0;

  const handleProjectChange = (value: string | null) => {
    const nextValue = value ?? "";
    setLastSelectedProjectId(nextValue || null);
    navigate(nextValue ? `/logs?project=${nextValue}` : "/logs");
  };

  const handleDeleteBatch = async () => {
    try {
      await deleteBatch.mutateAsync(batchId);
      toast.success("已删除该批次日志");
      navigate(projectId ? `/logs?project=${projectId}` : "/logs");
    } catch {
      toast.error("删除失败，请稍后重试");
    }
  };

  const notFound =
    batchError &&
    batchQueryError instanceof HTTPError &&
    batchQueryError.response.status === 404;

  if (batchLoading) {
    return (
      <>
        <Header title="日志批次" />
        <div className="p-4 md:p-6 space-y-4">
          <Skeleton className="h-10 w-64" />
          <Skeleton className="h-32 w-full" />
          <Skeleton className="h-20 w-full" />
        </div>
      </>
    );
  }

  if (notFound || (!batchLoading && batchError)) {
    return (
      <>
        <Header title="日志批次" />
        <div className="p-4 md:p-6">
          <Card>
            <CardContent className="flex flex-col items-center justify-center gap-3 py-16 text-muted-foreground">
              <p className="text-sm">
                {notFound ? "批次不存在" : "加载批次失败，请稍后重试"}
              </p>
              <Button
                render={<Link to="/logs" />}
                variant="outline"
                size="sm"
              >
                <ArrowLeft className="h-4 w-4" />
                返回列表
              </Button>
            </CardContent>
          </Card>
        </div>
      </>
    );
  }

  if (!batch) return null;

  return (
    <>
      <Header
        title="日志批次"
        actions={
          <HeaderActions
            primary={
              <AlertDialog>
                <AlertDialogTrigger
                  render={
                    <Button variant="outline" size="sm" className="text-destructive">
                      <Trash2 className="h-4 w-4" />
                      删除批次
                    </Button>
                  }
                />
                <AlertDialogContent>
                  <AlertDialogHeader>
                    <AlertDialogTitle>删除该批次日志？</AlertDialogTitle>
                    <AlertDialogDescription>
                      将删除该批次及其全部日志条目，此操作不可撤销。
                    </AlertDialogDescription>
                  </AlertDialogHeader>
                  <AlertDialogFooter>
                    <AlertDialogCancel>取消</AlertDialogCancel>
                    <AlertDialogAction
                      onClick={() => void handleDeleteBatch()}
                      disabled={deleteBatch.isPending}
                    >
                      确认删除
                    </AlertDialogAction>
                  </AlertDialogFooter>
                </AlertDialogContent>
              </AlertDialog>
            }
          />
        }
      />
      <div className="p-4 md:p-6 space-y-6">
        <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <div className="flex items-center gap-2">
            <Button
              render={<Link to={`/logs?project=${projectId}`} />}
              variant="ghost"
              size="sm"
            >
              <ArrowLeft className="h-4 w-4" />
              返回列表
            </Button>
            {projects.length > 0 && (
              <Select value={projectId} onValueChange={handleProjectChange}>
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
          </div>
        </div>

        <Card>
          <CardContent className="p-4 space-y-3">
            <p className="whitespace-pre-wrap text-base font-medium">
              {batch.description || "（无说明）"}
            </p>
            <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
              <span>{batch.entry_count} 条</span>
              <span className="font-mono">run:{batch.run_id}</span>
              {batch.source && <span>src:{batch.source}</span>}
              <span>首条 {formatTime(batch.first_entry_at)}</span>
              <span>最近 {formatTime(batch.last_entry_at)}</span>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <p
                className="truncate font-mono text-xs text-muted-foreground"
                title={batch.id}
              >
                批次 ID：{batch.id}
              </p>
              <Button
                variant="ghost"
                size="sm"
                className="h-7"
                onClick={() => void copyWithToast(batch.id, "已复制批次 ID")}
              >
                <Copy className="h-3.5 w-3.5" />
                复制批次 ID
              </Button>
            </div>
          </CardContent>
        </Card>

        <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
          <div className="relative">
            <Search className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              className="pl-9"
              placeholder="搜索消息内容"
              value={queryFilter}
              onChange={(e) =>
                updateSearchParams({ q: e.target.value || null })
              }
            />
          </div>
          <Select
            value={levelFilter}
            onValueChange={(value) =>
              updateSearchParams({
                level: value === "all" ? null : value,
              })
            }
          >
            <SelectTrigger>
              <SelectValue>
                {LOG_LEVELS.find((item) => item.value === levelFilter)?.label ??
                  levelFilter}
              </SelectValue>
            </SelectTrigger>
            <SelectContent>
              {LOG_LEVELS.map((item) => (
                <SelectItem key={item.value} value={item.value}>
                  {item.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {canResetFilters && (
            <Button variant="ghost" size="sm" onClick={handleResetFilters}>
              <RotateCcw className="h-4 w-4" />
              重置筛选
            </Button>
          )}
        </div>

        {entriesLoading ? (
          <div className="space-y-3">
            {Array.from({ length: 5 }).map((_, i) => (
              <Skeleton key={i} className="h-20 w-full rounded-lg" />
            ))}
          </div>
        ) : entriesError ? (
          <Card>
            <CardContent className="flex flex-col items-center justify-center gap-2 py-16 text-destructive">
              <p className="text-sm">加载日志失败，请稍后重试</p>
            </CardContent>
          </Card>
        ) : entries.length === 0 ? (
          <Card>
            <CardContent className="flex flex-col items-center justify-center gap-2 py-16 text-muted-foreground">
              <Inbox className="h-10 w-10 opacity-40" />
              <p className="text-sm">暂无匹配的日志条目</p>
            </CardContent>
          </Card>
        ) : (
          <div className="space-y-3" data-testid="log-entry-list">
            <p className="text-sm text-muted-foreground">共 {total} 条（正序）</p>
            {entries.map((entry) => (
              <Card key={entry.id}>
                <CardContent className="p-4 space-y-2">
                  <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                    <LogLevelBadge level={entry.level} />
                    <span>{new Date(entry.timestamp).toLocaleString()}</span>
                    {entry.source && <span>src:{entry.source}</span>}
                  </div>
                  <pre className="whitespace-pre-wrap break-words text-sm font-sans">
                    {entry.message}
                  </pre>
                  {entry.metadata && (
                    <pre className="rounded-md bg-muted p-2 text-xs overflow-x-auto">
                      {formatLogMetadata(entry.metadata)}
                    </pre>
                  )}
                </CardContent>
              </Card>
            ))}
            <div ref={setLoadMoreRef} className="h-8" />
            {isFetchingNextPage && (
              <p className="text-center text-xs text-muted-foreground">
                加载更多…
              </p>
            )}
          </div>
        )}
      </div>
    </>
  );
}
