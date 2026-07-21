import { useEffect, useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router";
import {
  ChevronLeft,
  ChevronRight,
  Copy,
  Inbox,
  RotateCcw,
  ScrollText,
  Trash2,
} from "lucide-react";
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
  useClearProjectLogs,
  useDeleteLogBatch,
  useLogBatches,
} from "@/lib/hooks/use-logs";
import { useProjectPreferenceStore } from "@/lib/store/project-preference-store";
import { getActiveProjectId } from "@/routes/board/lib/utils";
import { copyWithToast } from "@/lib/copy";
import { toast } from "sonner";

function datetimeLocalToISO(value: string): string | undefined {
  if (!value) return undefined;
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return undefined;
  return date.toISOString();
}

function formatTime(value: string | null): string {
  if (!value) return "—";
  return new Date(value).toLocaleString();
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
  const batchSourceFilter = searchParams.get("batch_source") ?? "";
  const fromFilter = searchParams.get("from_local") ?? "";
  const toFilter = searchParams.get("to_local") ?? "";
  const page = Math.max(Number(searchParams.get("page") ?? "1") || 1, 1);

  const {
    data: batchesData,
    isLoading: batchesLoading,
    isError: batchesError,
  } = useLogBatches(activeProjectId, {
    run_id: runIdFilter || undefined,
    batch_source: batchSourceFilter || undefined,
    from: datetimeLocalToISO(fromFilter),
    to: datetimeLocalToISO(toFilter),
    page,
    page_size: 50,
  });

  const [pendingDeleteBatchId, setPendingDeleteBatchId] = useState<string | null>(
    null,
  );

  const deleteBatch = useDeleteLogBatch(activeProjectId);
  const clearProjectLogs = useClearProjectLogs(activeProjectId);

  const batches = batchesData?.items ?? [];
  const total = batchesData?.total ?? 0;
  const pageSize = batchesData?.page_size ?? 50;
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
    batchSourceFilter.length > 0 ||
    fromFilter.length > 0 ||
    toFilter.length > 0 ||
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

  const handleCopyBatchId = (batchId: string) => {
    void copyWithToast(batchId, "已复制批次 ID");
  };

  return (
    <>
      <Header
        title="日志"
        actions={
          activeProjectId ? (
            <HeaderActions
              primary={
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
              }
            />
          ) : undefined
        }
      />
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
        </div>

        {!activeProjectId ? (
          <Card>
            <CardContent className="flex flex-col items-center justify-center gap-2 py-16 text-muted-foreground">
              <ScrollText className="h-10 w-10 opacity-40" />
              <p className="text-sm">请先选择项目以查看日志批次</p>
            </CardContent>
          </Card>
        ) : (
          <>
            <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-4">
              <Input
                placeholder="run_id"
                value={runIdFilter}
                onChange={(e) =>
                  updateSearchParams({ run_id: e.target.value || null }, true)
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

            {batchesLoading ? (
              <div className="space-y-3">
                {Array.from({ length: 5 }).map((_, i) => (
                  <Skeleton key={i} className="h-24 w-full rounded-lg" />
                ))}
              </div>
            ) : batchesError ? (
              <Card>
                <CardContent className="flex flex-col items-center justify-center gap-2 py-16 text-destructive">
                  <p className="text-sm">加载批次失败，请稍后重试</p>
                </CardContent>
              </Card>
            ) : batches.length === 0 ? (
              <Card>
                <CardContent className="flex flex-col items-center justify-center gap-2 py-16 text-muted-foreground">
                  <Inbox className="h-10 w-10 opacity-40" />
                  <p className="text-sm">暂无匹配的日志批次</p>
                </CardContent>
              </Card>
            ) : (
              <div className="space-y-3" data-testid="log-batch-list">
                {batches.map((batch) => (
                  <Card key={batch.id}>
                    <CardContent className="p-4 space-y-3">
                      <div className="flex flex-wrap items-start gap-3">
                        <Link
                          to={`/logs/${batch.id}?project=${activeProjectId}`}
                          className="min-w-0 flex-1 space-y-1 hover:opacity-90"
                        >
                          <p className="whitespace-pre-wrap text-sm font-medium">
                            {batch.description || "（无说明）"}
                          </p>
                          <div className="flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted-foreground">
                            <span>{batch.entry_count} 条</span>
                            <span className="font-mono">run:{batch.run_id}</span>
                            {batch.source && <span>src:{batch.source}</span>}
                            <span>最近 {formatTime(batch.last_entry_at)}</span>
                          </div>
                        </Link>
                        <div className="flex items-center gap-1">
                          <Button
                            variant="ghost"
                            size="sm"
                            className="h-7"
                            title={batch.id}
                            onClick={() => handleCopyBatchId(batch.id)}
                          >
                            <Copy className="h-3.5 w-3.5" />
                            复制批次 ID
                          </Button>
                          <Button
                            variant="ghost"
                            size="sm"
                            className="h-7 text-destructive"
                            disabled={deleteBatch.isPending}
                            onClick={() => setPendingDeleteBatchId(batch.id)}
                          >
                            <Trash2 className="h-3.5 w-3.5" />
                            删除
                          </Button>
                        </div>
                      </div>
                      <p
                        className="truncate font-mono text-xs text-muted-foreground"
                        title={batch.id}
                      >
                        批次 ID：{batch.id}
                      </p>
                    </CardContent>
                  </Card>
                ))}
              </div>
            )}

            {totalPages > 1 && (
              <div className="flex items-center justify-between">
                <p className="text-sm text-muted-foreground">
                  共 {total} 批，第 {page} / {totalPages} 页
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
              将删除该批次及其全部日志条目，此操作不可撤销。
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
