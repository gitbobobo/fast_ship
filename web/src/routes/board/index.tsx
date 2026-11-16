import { useCallback, useEffect, useState } from "react";
import {
  Link,
} from "react-router";
import {
  DndContext,
  DragOverlay,
  pointerWithin,
  type DragEndEvent,
  type DragStartEvent,
} from "@dnd-kit/core";
import { Plus } from "lucide-react";
import { Header } from "@/components/layout/header";
import { HeaderActions } from "@/components/layout/header-actions";
import { Button } from "@/components/ui/button";
import { useUpdateIssueWorkflowStatus } from "@/lib/hooks/use-issues";
import { useRouteProject } from "@/lib/current-project";
import { toast } from "sonner";
import {
  ProjectBreadcrumb,
  ProjectNotFoundCard,
} from "@/components/projects/project-page-chrome";
import {
  COLUMNS,
  type ColumnId,
  getColumnStatusValue,
  getBoardWorkflowStatusParam,
} from "@/routes/board/lib/utils";
import { usePersistedScroll } from "@/lib/hooks/use-persisted-scroll";
import {
  type BoardIssueColumnContext,
} from "@/lib/issue-list-context";
import { BoardColumn } from "@/routes/board/components/board-column";
import {
  DEFAULT_BOARD_COLUMN_FILTER,
  type BoardColumnFilterValue,
} from "@/routes/board/components/board-column-filter";
import { RecommendedIssuesButton } from "@/routes/board/components/recommended-issues-button";
import {
  BoardIssueCardOverlay,
} from "@/routes/board/components/board-issue-card";

function createDefaultColumnFilters(): Record<ColumnId, BoardColumnFilterValue> {
  return Object.fromEntries(
    COLUMNS.map((column) => [column.id, DEFAULT_BOARD_COLUMN_FILTER]),
  ) as Record<ColumnId, BoardColumnFilterValue>;
}

export default function BoardPage() {
  // 项目上下文只认路径：无效 ID 整页空态，不再回退到偏好或静默换项目
  const { projectId, project, notFound } = useRouteProject();
  if (notFound) {
    return <ProjectNotFoundCard />;
  }
  return <BoardPageContent projectId={projectId} project={project} />;
}

function BoardPageContent({
  projectId,
  project,
}: {
  projectId: string;
  project: Project | undefined;
}) {

  const [activeIssue, setActiveIssue] = useState<Issue | null>(null);

  // 各列筛选提升到页面持有：卡片跳详情时要按所在列编码来源上下文；项目切换时重置
  const [columnFilters, setColumnFilters] = useState<Record<ColumnId, BoardColumnFilterValue>>(
    createDefaultColumnFilters,
  );
  useEffect(() => {
    setColumnFilters(createDefaultColumnFilters());
  }, [projectId]);

  const handleColumnFilterChange = useCallback(
    (columnId: ColumnId, next: BoardColumnFilterValue) => {
      setColumnFilters((prev) => ({ ...prev, [columnId]: next }));
    },
    [],
  );

  /** 列 = issue.internal_meta?.workflow_status 对应列；列未知（含数据未加载）返回 null */
  const getIssueColumnContext = useCallback(
    (issue: Issue): BoardIssueColumnContext | null => {
      const column = COLUMNS.find(
        (c) => c.statusValue === (issue.internal_meta?.workflow_status ?? ""),
      );
      if (!column) {
        return null;
      }
      const workflowStatusParam = getBoardWorkflowStatusParam(column.statusValue);
      if (!workflowStatusParam) {
        return null;
      }
      const filter = columnFilters[column.id] ?? DEFAULT_BOARD_COLUMN_FILTER;
      return {
        workflowStatus: workflowStatusParam,
        label: filter.label,
        source: filter.source,
      };
    },
    [columnFilters],
  );

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

  // projectId 来自路径，挂载即可恢复横向滚动位置，无需等项目列表
  const boardScrollRef = usePersistedScroll<HTMLDivElement>(
    `board-x:${projectId}`,
    { ready: true, axis: "left" },
  );

  return (
    <div className="flex h-full flex-col">
      <Header
        title={<ProjectBreadcrumb section="看板" project={project} />}
        actions={
          <HeaderActions
            primary={
              <Button
                variant="outline"
                size="sm"
                render={
                  <Link
                    to={{
                      pathname: `/projects/${projectId}/issues/new`,
                    }}
                  />
                }
              >
                <Plus className="mr-1.5 h-3.5 w-3.5" />
                新建问题
              </Button>
            }
          />
        }
      />
      <div className="flex flex-1 flex-col overflow-hidden p-4 md:p-6">
        {/* 工具行：项目选择已上移侧边栏，这里只留推荐问题入口 */}
        <div className="mb-4 flex flex-wrap items-center gap-3">
          <RecommendedIssuesButton
            projectId={projectId}
            getIssueColumnContext={getIssueColumnContext}
          />
        </div>

        {/* Board */}
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
                projectId={projectId}
                filter={columnFilters[column.id] ?? DEFAULT_BOARD_COLUMN_FILTER}
                onFilterChange={(next) => handleColumnFilterChange(column.id, next)}
                getIssueColumnContext={getIssueColumnContext}
              />
            ))}
          </div>
          <DragOverlay dropAnimation={null}>
            {activeIssue ? (
              <BoardIssueCardOverlay issue={activeIssue} />
            ) : null}
          </DragOverlay>
        </DndContext>
      </div>
    </div>
  );
}
