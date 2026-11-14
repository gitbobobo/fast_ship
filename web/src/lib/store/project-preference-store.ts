import { create } from "zustand";
import { persist } from "zustand/middleware";

interface ProjectPreferenceState {
  lastSelectedProjectId: string | null;
  setLastSelectedProjectId: (id: string | null) => void;
  /** 详情页来源列表在宽屏（xl+）是否展开为固定左栏；仅存开关，不存来源或列表数据 */
  issueSourceListExpanded: boolean;
  setIssueSourceListExpanded: (expanded: boolean) => void;
}

export const useProjectPreferenceStore = create<ProjectPreferenceState>()(
  persist(
    (set) => ({
      lastSelectedProjectId: null,
      setLastSelectedProjectId: (id) => set({ lastSelectedProjectId: id }),
      issueSourceListExpanded: true,
      setIssueSourceListExpanded: (expanded) =>
        set({ issueSourceListExpanded: expanded }),
    }),
    {
      name: "fast-ship-project-preference",
    },
  ),
);
