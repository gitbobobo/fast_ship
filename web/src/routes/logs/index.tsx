import { useDeferredValue, useEffect, useMemo, useState } from "react";
import { useSearchParams } from "react-router";
import {
  ChevronLeft,
  ChevronRight,
  Inbox,
  RotateCcw,
  ScrollText,
  Search,
  Trash2,
} from "lucide-react";
import { Header } from "@/components/layout/header";
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
  useClearProjectLogs,
  useDeleteLogBatch,
  useLogs,
} from "@/lib/hooks/use-logs";
import { useProjectPreferenceStore } from "@/lib/store/project-preference-store";
import { getActiveProjectId } from "@/routes/board/lib/utils";
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

function datetimeLocalToISO(value: string): string | undefined {
  if (!value) return undefined;
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return undefined;
  return date.toISOString();
}

function formatLogMetadata(raw: string): string {
  try {
    return JSON.stringify(JSON.parse(raw), null, 2);
  } catch {
    return raw;
  }
}

export default function LogsPage() {
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

  const runIdFilter = searchParams.get("run_id") ?? "";
  const levelFilter = searchParams.get("level") ?? "all";
  const entrySourceFilter = searchParams.get("entry_source") ?? "";
  const batchSourceFilter = searchParams.get("batch_source") ?? "";
  const fromFilter = searchParams.get("from_local") ?? "";
  const toFilter = searchParams.get("to_local") ?? "";
  const queryFilter = searchParams.get("q") ?? "";
  const page = Math.max(Number(searchParams.get("page") ?? "1") || 1, 1);
  const deferredQuery = useDeferredValue(queryFilter.trim());

  const { data: logsData, isLoading: logsLoading, isError: logsError } = useLogs(activeProjectId, {
    run_id: runIdFilter || undefined,
    level: levelFilter === "all" ? undefined : levelFilter,
    entry_source: entrySourceFilter || undefined,
    batch_source: batchSourceFilter || undefined,
    q: deferredQuery || undefined,
    from: datetimeLocalToISO(fromFilter),
    to: datetimeLocalToISO(toFilter),
    page,
    page_size: 50,
  });

  const [pendingDeleteBatchId, setPendingDeleteBatchId] = useState<string | null>(null);

  const deleteBatch = useDeleteLogBatch(activeProjectId);
  const clearProjectLogs = useClearProjectLogs(activeProjectId);

  const entries = logsData?.items ?? [];
  const total = logsData?.total ?? 0;
  const pageSize = logsData?.page_size ?? 50;
  const totalPages = Math.max(Math.ceil(total / pageSize), 1);

  const updateSearchParams = (
    updates: Record<string, string | null>,
    resetPage = false,
  ) => {
    const next = new URLSearchParams(searchParams);
    for (const [key, value] of Object.entries(updates)) {
      if (!value) {
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
    runIdFilter.length > 0 ||
    levelFilter !== "all" ||
    entrySourceFilter.length > 0 ||
    batchSourceFilter.length > 0 ||
    fromFilter.length > 0 ||
    toFilter.length > 0 ||
    queryFilter.trim().length > 0 ||
    page > 1;

  const handleDeleteBatch = async (batchId: string) => {
    try {
      await deleteBatch.mutateAsync(batchId);
      toast.success("已删除该批次日志");
      setPendingDeleteBatchId(null);
    } catch {
      toast.error("删除失败，请稍后重试");
    }
  };

  const handleClearProject = async () => {
    try {
      await clearProjectLogs.mutateAsync();
      toast.success("已清空项目日志");
    } catch {
      toast.error("清空失败，请稍后重试");
    }
  };

  return (
    <>
      <Header title="日志" />
      <div className="p-4 md:p-6 space-y-6">
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
          </div>

          {activeProjectId && (
            <AlertDialog>
              <AlertDialogTrigger
                render={
                  <Button variant="outline" size="sm" className="text-destructive">
                    <Trash2 className="h-4 w-4" />
                    清空项目日志
                  </Button>
                }
              />
              <AlertDialogContent>
                <AlertDialogHeader>
                  <AlertDialogTitle>清空项目日志？</AlertDialogTitle>
                  <AlertDialogDescription>
                    将删除「{activeProject?.name}」下的全部日志批次与条目，此操作不可撤销。
                  </AlertDialogDescription>
                </AlertDialogHeader>
                <AlertDialogFooter>
                  <AlertDialogCancel>取消</AlertDialogCancel>
                  <AlertDialogAction
                    onClick={() => void handleClearProject()}
                    disabled={clearProjectLogs.isPending}
                  >
                    确认清空
                  </AlertDialogAction>
                </AlertDialogFooter>
              </AlertDialogContent>
            </AlertDialog>
          )}
        </div>

        {!activeProjectId ? (
          <Card>
            <CardContent className="flex flex-col items-center justify-center gap-2 py-16 text-muted-foreground">
              <ScrollText className="h-10 w-10 opacity-40" />
              <p className="text-sm">请先选择项目以查看日志</p>
            </CardContent>
          </Card>
        ) : (
          <>
            <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-4">
              <div className="relative">
                <Search className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
                <Input
                  className="pl-9"
                  placeholder="搜索消息内容"
                  value={queryFilter}
                  onChange={(e) =>
                    updateSearchParams({ q: e.target.value || null }, true)
                  }
                />
              </div>
              <Input
                placeholder="run_id"
                value={runIdFilter}
                onChange={(e) =>
                  updateSearchParams({ run_id: e.target.value || null }, true)
                }
              />
              <Select
                value={levelFilter}
                onValueChange={(value) =>
                  updateSearchParams(
                    { level: value === "all" ? null : value },
                    true,
                  )
                }
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {LOG_LEVELS.map((item) => (
                    <SelectItem key={item.value} value={item.value}>
                      {item.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <Input
                placeholder="条目 source"
                value={entrySourceFilter}
                onChange={(e) =>
                  updateSearchParams(
                    { entry_source: e.target.value || null },
                    true,
                  )
                }
              />
              <Input
                placeholder="批次 source"
                value={batchSourceFilter}
                onChange={(e) =>
                  updateSearchParams(
                    { batch_source: e.target.value || null },
                    true,
                  )
                }
              />
              <Input
                type="datetime-local"
                value={fromFilter}
                onChange={(e) =>
                  updateSearchParams({ from_local: e.target.value || null }, true)
                }
              />
              <Input
                type="datetime-local"
                value={toFilter}
                onChange={(e) =>
                  updateSearchParams({ to_local: e.target.value || null }, true)
                }
              />
              {canResetFilters && (
                <Button variant="ghost" size="sm" onClick={handleResetFilters}>
                  <RotateCcw className="h-4 w-4" />
                  重置筛选
                </Button>
              )}
            </div>

            {logsLoading ? (
              <div className="space-y-3">
                {Array.from({ length: 5 }).map((_, i) => (
                  <Skeleton key={i} className="h-20 w-full rounded-lg" />
                ))}
              </div>
            ) : logsError ? (
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
              <div className="space-y-3">
                {entries.map((entry) => (
                  <Card key={entry.id}>
                    <CardContent className="p-4 space-y-2">
                      <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                        <LogLevelBadge level={entry.level} />
                        <span>{new Date(entry.timestamp).toLocaleString()}</span>
                        <span className="font-mono">run:{entry.run_id}</span>
                        {entry.source && <span>src:{entry.source}</span>}
                        {entry.batch_source && (
                          <span>batch:{entry.batch_source}</span>
                        )}
                        <div className="ml-auto">
                          <Button
                            variant="ghost"
                            size="sm"
                            className="h-7 text-destructive"
                            disabled={deleteBatch.isPending}
                            onClick={() => setPendingDeleteBatchId(entry.batch_id)}
                          >
                            <Trash2 className="h-3.5 w-3.5" />
                            删除批次
                          </Button>
                        </div>
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
              </div>
            )}

            {totalPages > 1 && (
              <div className="flex items-center justify-between">
                <p className="text-sm text-muted-foreground">
                  共 {total} 条，第 {page} / {totalPages} 页
                </p>
                <div className="flex items-center gap-2">
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={page <= 1}
                    onClick={() =>
                      updateSearchParams({ page: String(page - 1) })
                    }
                  >
                    <ChevronLeft className="h-4 w-4" />
                    上一页
                  </Button>
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={page >= totalPages}
                    onClick={() =>
                      updateSearchParams({ page: String(page + 1) })
                    }
                  >
                    下一页
                    <ChevronRight className="h-4 w-4" />
                  </Button>
                </div>
              </div>
            )}
          </>
        )}
      </div>

      <AlertDialog
        open={pendingDeleteBatchId != null}
        onOpenChange={(open) => {
          if (!open) setPendingDeleteBatchId(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>删除该批次日志？</AlertDialogTitle>
            <AlertDialogDescription>
              将删除 run_id 对应的整批日志条目，此操作不可撤销。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>取消</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => void handleDeleteBatch(pendingDeleteBatchId!)}
              disabled={deleteBatch.isPending || pendingDeleteBatchId == null}
            >
              确认删除
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
