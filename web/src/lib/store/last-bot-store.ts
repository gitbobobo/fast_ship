import { create } from "zustand";
import { persist } from "zustand/middleware";

interface LastBotState {
  /** 最近打开的机器人聊天：侧边栏「机器人」菜单项跳到它而不是空工作台。 */
  lastBotId: string | null;
  setLastBot: (botId: string | null) => void;
}

export const useLastBotStore = create<LastBotState>()(
  persist(
    (set) => ({
      lastBotId: null,
      setLastBot: (botId) => set({ lastBotId: botId }),
    }),
    {
      name: "fast-ship-last-bot",
    },
  ),
);
