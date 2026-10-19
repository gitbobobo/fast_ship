import { create } from "zustand";
import { persist } from "zustand/middleware";

interface BotDetailPanelState {
  /** 桌面右栏是否展开：默认收起，跨刷新记住用户上次的选择。 */
  expanded: boolean;
  setExpanded: (expanded: boolean) => void;
}

export const useBotDetailPanelStore = create<BotDetailPanelState>()(
  persist(
    (set) => ({
      expanded: false,
      setExpanded: (expanded) => set({ expanded }),
    }),
    {
      name: "fast-ship-bot-detail-panel",
    },
  ),
);
