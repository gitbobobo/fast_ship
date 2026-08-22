import { useMemo, useState, useEffect, useCallback } from "react";
import { useSearchParams, Link } from "react-router";
import {
  DndContext,
  DragOverlay,
  pointerWithin,
  type DragEndEvent,
  type DragStartEvent,
} from "@dnd-kit/core";
import { Package, Plus } from "lucide-react";
import { Header } from "@/components/layout/header";
import { HeaderActions } from "@/components/layout/header-actions";
import { Button } from "@/components/ui/button";
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
import { useUpdateIssueWorkflowStatus } from "@/lib/hooks/use-issues";
import { useProjectPreferenceStore } from "@/lib/store/project-preference-store";
import { toast } from "sonner";
import {
  COLUMNS,
  type ColumnId,
  getColumnStatusValue,
  getActiveProjectId,
} from "@/routes/board/lib/utils";
import { usePersistedScroll } from "@/lib/hooks/use-persisted-scroll";
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

  const [activeIssue, setActiveIssue] = useState<Issue | null>(null);

  const updateWorkflowStatus = useUpdateIssueWorkflowStatus();

  const handleDragStart = useCallback((event: DragStartEvent) => {
    setActiveIssue(event.active.data.current?.issue ?? null);
  }, []);

  const handleDragEnd = useCallback(
    async (event: DragEndEvent) => {
      const { over } = event;
      const issue = event.active.data.current?.issue;
      setActiveIssue(null);
      if (!issue) return;
      if (!over) return;
      if (over.data.current?.type !== "column") return;

      const columnId = over.id as ColumnId;
      const currentStatus = issue.internal_meta?.workflow_status ?? "";
      const targetStatus = getColumnStatusValue(columnId);

      if (currentStatus === targetStatus) return;

      try {
        await updateWorkflowStatus.mutateAsync({
          issueId: issue.id,
          projectId: issue.project_id,
          workflow_status: targetStatus,
        });
        toast.success("状态已更新");
      } catch {
        toast.error("状态更新失败");
      }
    },
    [updateWorkflowStatus],
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
  const boardReady = Boolean(activeProjectId) && !projectsLoading;
  const boardScrollRef = usePersistedScroll<HTMLDivElement>(
    `board-x:${activeProjectId}`,
    { ready: boardReady, axis: "left" },
  );

  return (
    <div className="flex h-full flex-col">
      <Header
        title="看板"
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
                      }}
                    />
                  }
                >
                  <Plus className="mr-1.5 h-3.5 w-3.5" />
                  新建问题
                </Button>
              }
            />
          ) : undefined
        }
      />
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
        </div>

        {/* Board */}
        {projectsLoading ? (
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
        ) : (
          <DndContext
            collisionDetection={pointerWithin}
            autoScroll={false}
            onDragStart={handleDragStart}
            onDragEnd={handleDragEnd}
          >
            <div
              ref={boardScrollRef}
              className="flex flex-1 gap-4 overflow-x-auto py-1 pb-2"
            >
              {COLUMNS.map((column) => (
                <BoardColumn
                  key={column.id}
                  columnId={column.id}
                  projectId={activeProjectId}
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
