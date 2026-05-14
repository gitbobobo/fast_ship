import { useMemo, useState, useEffect, useCallback } from "react";
import { useSearchParams } from "react-router";
import {
  DndContext,
  DragOverlay,
  pointerWithin,
  type DragEndEvent,
  type DragStartEvent,
} from "@dnd-kit/core";
import { Kanban, Package } from "lucide-react";
import { Header } from "@/components/layout/header";
import { Card, CardContent } from "@/components/ui/card";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { useProjects } from "@/lib/hooks/use-projects";
import {
  useIssues,
  useUpdateIssueWorkflowStatus,
} from "@/lib/hooks/use-issues";
import { useProjectPreferenceStore } from "@/lib/store/project-preference-store";
import { toast } from "sonner";
import {
  COLUMNS,
  type ColumnId,
  getColumnIdByStatus,
  getColumnStatusValue,
  getActiveProjectId,
} from "@/routes/board/lib/utils";
import { BoardColumn } from "@/routes/board/components/board-column";
import {
  BoardIssueCardOverlay,
} from "@/routes/board/components/board-issue-card";

export default function BoardPage() {
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

  // Sync URL when active project changes
  useEffect(() => {
    if (activeProjectId && activeProjectId !== urlProjectId) {
      const next = new URLSearchParams(searchParams);
      next.set("project", activeProjectId);
      setSearchParams(next, { replace: true });
    }
  }, [activeProjectId, urlProjectId, searchParams, setSearchParams]);

  const { data: issuesData, isLoading: issuesLoading } = useIssues(
    activeProjectId,
    {
      state: "open",
      // 看板需要展示所有未关闭问题以支持跨列拖拽。
      // 500 为上限，超出时后续可接入虚拟滚动或分页加载。
      page_size: 500,
    },
  );

  const issues = useMemo(() => issuesData?.items ?? [], [issuesData]);
  const hasMoreIssues = (issuesData?.total ?? 0) > 500;

  const groupedIssues = useMemo(() => {
    const groups: Record<ColumnId, Issue[]> = {
      unset: [],
      todo: [],
      in_progress: [],
      done: [],
    };
    for (const issue of issues) {
      const status = issue.internal_meta?.workflow_status ?? "";
      const columnId = getColumnIdByStatus(status);
      groups[columnId].push(issue);
    }
    return groups;
  }, [issues]);

  const [activeId, setActiveId] = useState<string | null>(null);
  const activeIssue = useMemo(
    () => (activeId ? issues.find((i) => i.id === activeId) : null),
    [activeId, issues],
  );

  const updateWorkflowStatus = useUpdateIssueWorkflowStatus();

  const handleDragStart = useCallback((event: DragStartEvent) => {
    setActiveId(event.active.id as string);
  }, []);

  const handleDragEnd = useCallback(
    async (event: DragEndEvent) => {
      setActiveId(null);
      const { active, over } = event;
      if (!over) return;

      const issueId = active.id as string;
      if (over.data.current?.type !== "column") return;
      const columnId = over.id as string;

      const issue = issues.find((i) => i.id === issueId);
      if (!issue) return;

      const currentStatus = issue.internal_meta?.workflow_status ?? "";
      const targetStatus = getColumnStatusValue(columnId as ColumnId);

      if (currentStatus === targetStatus) return;

      try {
        await updateWorkflowStatus.mutateAsync({
          issueId,
          projectId: issue.project_id,
          workflow_status: targetStatus,
        });
        toast.success("状态已更新");
      } catch {
        toast.error("状态更新失败");
      }
    },
    [issues, updateWorkflowStatus],
  );

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

  const isEmptyProject = !projectsLoading && projects.length === 0;
  const noProjectSelected = !projectsLoading && !activeProjectId;

  return (
    <div className="flex h-full flex-col">
      <Header title="看板" />
      <div className="flex flex-1 flex-col overflow-hidden p-4 md:p-6">
        {/* Project selector */}
        <div className="mb-4 flex flex-wrap items-center gap-3">
          {projectsLoading ? (
            <Skeleton className="h-10 w-48 rounded-md" />
          ) : isEmptyProject ? (
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
          {hasMoreIssues && (
            <p className="text-xs text-amber-600 dark:text-amber-400">
              问题数量超过 500，看板仅展示前 500 条
            </p>
          )}
        </div>

        {/* Board */}
        {projectsLoading || issuesLoading ? (
          <div className="flex flex-1 gap-4 overflow-hidden">
            {COLUMNS.map((col) => (
              <div
                key={col.id}
                className="flex min-w-[280px] max-w-[320px] flex-1 flex-col rounded-lg border bg-muted/20"
              >
                <div className="border-b px-3 py-2.5">
                  <Skeleton className="h-5 w-24" />
                </div>
                <div className="space-y-2 p-2.5">
                  {Array.from({ length: 3 }).map((_, i) => (
                    <Skeleton key={i} className="h-24 rounded-md" />
                  ))}
                </div>
              </div>
            ))}
          </div>
        ) : isEmptyProject ? (
          <Card className="flex-1">
            <CardContent className="flex flex-col items-center justify-center py-16 text-center">
              <Package className="mb-4 h-12 w-12 text-muted-foreground/50" />
              <h3 className="text-lg font-medium">暂无项目</h3>
              <p className="mt-1 text-sm text-muted-foreground">
                创建项目后即可同步和管理 GitHub Issues
              </p>
            </CardContent>
          </Card>
        ) : noProjectSelected ? (
          <Card className="flex-1">
            <CardContent className="flex flex-col items-center justify-center py-16 text-center">
              <Package className="mb-4 h-12 w-12 text-muted-foreground/50" />
              <h3 className="text-lg font-medium">请选择项目</h3>
              <p className="mt-1 text-sm text-muted-foreground">
                从上方下拉菜单选择一个项目以查看看板
              </p>
            </CardContent>
          </Card>
        ) : issues.length === 0 ? (
          <Card className="flex-1">
            <CardContent className="flex flex-col items-center justify-center py-16 text-center">
              <Kanban className="mb-4 h-12 w-12 text-muted-foreground/50" />
              <h3 className="text-lg font-medium">暂无问题</h3>
              <p className="mt-1 text-sm text-muted-foreground">
                该项目下没有未关闭的问题
              </p>
            </CardContent>
          </Card>
        ) : (
          <DndContext
            collisionDetection={pointerWithin}
            autoScroll={false}
            onDragStart={handleDragStart}
            onDragEnd={handleDragEnd}
          >
            <div className="flex flex-1 gap-4 overflow-x-auto py-1 pb-2">
              {COLUMNS.map((column) => (
                <BoardColumn
                  key={column.id}
                  columnId={column.id}
                  issues={groupedIssues[column.id]}
                />
              ))}
            </div>
            <DragOverlay dropAnimation={null}>
              {activeIssue ? (
                <BoardIssueCardOverlay issue={activeIssue} />
              ) : null}
            </DragOverlay>
          </DndContext>
        )}
      </div>
    </div>
  );
}
