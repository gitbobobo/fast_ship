import { createContext, useContext } from "react";
import type { BoardIssueSelectModifiers } from "@/routes/board/lib/use-board-multi-select";

export interface BoardSelectionContextValue {
  multiSelectMode: boolean;
  /** 模式外按住 Ctrl/Cmd：卡片出示勾选预览，尚未进入多选模式。 */
  selectPreview: boolean;
  selectedIssueIds: ReadonlySet<string>;
  selectIssue: (issue: Issue, modifiers: BoardIssueSelectModifiers) => void;
}

const BoardSelectionContext = createContext<BoardSelectionContextValue | null>(
  null,
);

export function BoardSelectionProvider({
  value,
  children,
}: {
  value: BoardSelectionContextValue;
  children: React.ReactNode;
}) {
  return (
    <BoardSelectionContext.Provider value={value}>
      {children}
    </BoardSelectionContext.Provider>
  );
}

export function useBoardSelection() {
  return useContext(BoardSelectionContext);
}
