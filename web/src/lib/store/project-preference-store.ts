import { create } from "zustand";
import { persist } from "zustand/middleware";

interface ProjectPreferenceState {
  lastSelectedProjectId: string | null;
  setLastSelectedProjectId: (id: string | null) => void;
}

export const useProjectPreferenceStore = create<ProjectPreferenceState>()(
  persist(
    (set) => ({
      lastSelectedProjectId: null,
      setLastSelectedProjectId: (id) => set({ lastSelectedProjectId: id }),
    }),
    {
      name: "fast-ship-project-preference",
    },
  ),
);
