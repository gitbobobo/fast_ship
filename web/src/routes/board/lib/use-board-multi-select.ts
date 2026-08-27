import { useCallback, useEffect, useRef, useState } from "react";
import { COLUMNS, type ColumnId } from "@/routes/board/lib/utils";

export interface BoardIssueSelectModifiers {
  shiftKey: boolean;
}

interface SelectionAnchor {
  columnId: ColumnId;
  issueId: string;
}

type ColumnIssuesRef = Partial<Record<ColumnId, Issue[]>>;

function findColumnIdByIssueId(
  columnIssues: ColumnIssuesRef,
  issueId: string,
): ColumnId | null {
  for (const column of COLUMNS) {
    const issues = columnIssues[column.id];
    if (issues?.some((issue) => issue.id === issueId)) {
      return column.id;
    }
  }
  return null;
}

/**
 * 看板多选模式状态管理。
 *
 * 模式内整卡点击切换勾选；同列 Shift 做范围选（按列内已加载顺序）；
 * 模式外按住 Ctrl/Cmd 时进入选择预览（出示勾选框，尚未进入模式）；
 * 列筛选/翻页变化时只剪枝该列消失的勾选 id。所有状态仅存在于组件生命周期内，
 * 不做任何持久化。
 */
export function useBoardMultiSelect(activeProjectId: string) {
  const [multiSelectMode, setMultiSelectMode] = useState(false);
  const [modifierHeld, setModifierHeld] = useState(false);
  const [selectedIssueIds, setSelectedIssueIds] = useState<ReadonlySet<string>>(
    () => new Set<string>(),
  );

  const anchorRef = useRef<SelectionAnchor | null>(null);
  const columnIssuesRef = useRef<ColumnIssuesRef>({});
  const prevProjectIdRef = useRef(activeProjectId);

  const resetSelection = useCallback(() => {
    anchorRef.current = null;
    setSelectedIssueIds(new Set<string>());
  }, []);

  const toggleMode = useCallback(() => {
    setMultiSelectMode((mode) => !mode);
    resetSelection();
  }, [resetSelection]);

  const exit = useCallback(() => {
    setMultiSelectMode(false);
    resetSelection();
  }, [resetSelection]);

  const clearSelection = useCallback(() => {
    setSelectedIssueIds(new Set<string>());
  }, []);

  const selectIssueInMode = useCallback(
    (issue: Issue, modifiers: BoardIssueSelectModifiers) => {
      const columnId = findColumnIdByIssueId(columnIssuesRef.current, issue.id);
      const anchor = anchorRef.current;

      // 同列 Shift 范围选：把锚点到当前卡之间的 id 全部置为选中，
      // 不清其他列、不反向取消范围外的勾选；锚点列不同或找不到时退化为普通切换。
      if (
        modifiers.shiftKey &&
        anchor &&
        columnId &&
        anchor.columnId === columnId
      ) {
        const columnIssues = columnIssuesRef.current[columnId] ?? [];
        const anchorIndex = columnIssues.findIndex(
          (item) => item.id === anchor.issueId,
        );
        const currentIndex = columnIssues.findIndex(
          (item) => item.id === issue.id,
        );
        if (anchorIndex >= 0 && currentIndex >= 0) {
          const start = Math.min(anchorIndex, currentIndex);
          const end = Math.max(anchorIndex, currentIndex);
          setSelectedIssueIds((prev) => {
            const next = new Set(prev);
            for (const item of columnIssues.slice(start, end + 1)) {
              next.add(item.id);
            }
            return next;
          });
          return;
        }
      }

      // 普通切换（含 Cmd/Ctrl，模式内修饰键与普通点击行为一致），并移动锚点
      anchorRef.current = columnId
        ? { columnId, issueId: issue.id }
        : null;
      setSelectedIssueIds((prev) => {
        const next = new Set(prev);
        if (next.has(issue.id)) {
          next.delete(issue.id);
        } else {
          next.add(issue.id);
        }
        return next;
      });
    },
    [],
  );

  const selectIssue = useCallback(
    (issue: Issue, modifiers: BoardIssueSelectModifiers) => {
      if (!multiSelectMode) {
        setMultiSelectMode(true);
        const columnId = findColumnIdByIssueId(
          columnIssuesRef.current,
          issue.id,
        );
        anchorRef.current = columnId
          ? { columnId, issueId: issue.id }
          : null;
        setSelectedIssueIds(new Set<string>([issue.id]));
        return;
      }

      selectIssueInMode(issue, modifiers);
    },
    [multiSelectMode, selectIssueInMode],
  );

  const handleColumnIssuesChange = useCallback(
    (columnId: ColumnId, issues: Issue[]) => {
      const prevIssues = columnIssuesRef.current[columnId];
      columnIssuesRef.current[columnId] = issues;

      // 剪枝：旧列表里有、新列表没有的 id，从勾选里去掉；新增的 id 不自动勾选
      if (!prevIssues) return;
      const nextIds = new Set(issues.map((issue) => issue.id));
      const disappearedIds = prevIssues
        .filter((issue) => !nextIds.has(issue.id))
        .map((issue) => issue.id);
      if (disappearedIds.length === 0) return;

      setSelectedIssueIds((current) => {
        const next = new Set(current);
        let changed = false;
        for (const id of disappearedIds) {
          if (next.delete(id)) {
            changed = true;
          }
        }
        return changed ? next : current;
      });
    },
    [],
  );

  // 从各列已加载列表现算，不进 state，避免滚动加载触发重渲染
  const getOrderedSelectedIssues = useCallback((): Issue[] => {
    const result: Issue[] = [];
    for (const column of COLUMNS) {
      for (const issue of columnIssuesRef.current[column.id] ?? []) {
        if (selectedIssueIds.has(issue.id)) {
          result.push(issue);
        }
      }
    }
    return result;
  }, [selectedIssueIds]);

  // Esc 退出（仅模式内生效；已被 preventDefault 的事件让打开中的菜单先自己处理）
  useEffect(() => {
    if (!multiSelectMode) return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      if (event.defaultPrevented) return;
      exit();
    };
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [multiSelectMode, exit]);

  // 模式外按住 Ctrl/Cmd 时进入选择预览；窗口失焦时清掉，避免键抬起丢失
  useEffect(() => {
    const syncModifier = (event: KeyboardEvent) => {
      setModifierHeld(event.ctrlKey || event.metaKey);
    };
    const clearModifier = () => setModifierHeld(false);
    window.addEventListener("keydown", syncModifier);
    window.addEventListener("keyup", syncModifier);
    window.addEventListener("blur", clearModifier);
    return () => {
      window.removeEventListener("keydown", syncModifier);
      window.removeEventListener("keyup", syncModifier);
      window.removeEventListener("blur", clearModifier);
    };
  }, []);

  // 切换项目（含下拉切换和回退 effect）时退出并清空
  useEffect(() => {
    if (prevProjectIdRef.current === activeProjectId) return;
    prevProjectIdRef.current = activeProjectId;
    setMultiSelectMode(false);
    resetSelection();
  }, [activeProjectId, resetSelection]);

  return {
    multiSelectMode,
    selectPreview: modifierHeld && !multiSelectMode,
    selectedIssueIds,
    selectedCount: selectedIssueIds.size,
    toggleMode,
    selectIssue,
    exit,
    clearSelection,
    handleColumnIssuesChange,
    getOrderedSelectedIssues,
  };
}
