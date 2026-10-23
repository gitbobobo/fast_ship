import { create } from "zustand";
import { persist } from "zustand/middleware";
import { useAuthStore } from "@/lib/store/auth-store";

interface LastBotState {
  /** 最近打开的机器人聊天，绑定写入时的登录用户（INT-157 复审：
   *  同浏览器切换账号不能读串到上一账号的记忆）。侧边栏「机器人」
   *  菜单项跳到它而不是空工作台；消费方用 useLastBotId 读取。 */
  lastBot: { userId: string; botId: string } | null;
  setLastBot: (botId: string | null) => void;
}

export const useLastBotStore = create<LastBotState>()(
  persist(
    (set) => ({
      lastBot: null,
      setLastBot: (botId) => {
        if (!botId) {
          set({ lastBot: null });
          return;
        }
        // user 尚未拉取时不写——跨账号校验靠 userId 成立，写一条没有
        // 归属的记录只会留垃圾。
        const userId = useAuthStore.getState().user?.id;
        if (userId) set({ lastBot: { userId, botId } });
      },
    }),
    {
      name: "fast-ship-last-bot",
      version: 1,
      // v0 存的是 { lastBotId } 且不带用户维度，直接作废。
      migrate: () => ({ lastBot: null }),
    },
  ),
);

/** 当前用户的「上次打开的机器人」——记忆属于别的账号或还没写入时返回 null。 */
export function useLastBotId(): string | null {
  const lastBot = useLastBotStore((s) => s.lastBot);
  const userId = useAuthStore((s) => s.user?.id);
  return lastBot && userId && lastBot.userId === userId ? lastBot.botId : null;
}
