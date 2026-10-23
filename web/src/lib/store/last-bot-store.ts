import { create } from "zustand";
import { persist } from "zustand/middleware";
import { useAuthStore } from "@/lib/store/auth-store";

interface LastBotState {
  /** 各账号「上次打开的机器人聊天」记忆（userId → botId）。同浏览器
   *  切换账号时各记各的、互不覆盖（INT-157 复审）；消费方用
   *  useLastBotId 读取当前用户的记忆。 */
  byUser: Record<string, string>;
  setLastBot: (botId: string | null) => void;
}

export const useLastBotStore = create<LastBotState>()(
  persist(
    (set, get) => ({
      byUser: {},
      setLastBot: (botId) => {
        // user 尚未拉取时不写——记录按 userId 归属，写一条没有归属的
        // 记录只会留垃圾。
        const userId = useAuthStore.getState().user?.id;
        if (!userId) return;
        const byUser = { ...get().byUser };
        if (botId) {
          byUser[userId] = botId;
        } else {
          delete byUser[userId];
        }
        set({ byUser });
      },
    }),
    {
      name: "fast-ship-last-bot",
      version: 2,
      // v0 存 { lastBotId }、v1 存单槽 { lastBot: {userId,botId} }；
      // 能按 userId 归位的并入 map，其余作废。
      migrate: (persisted) => {
        const old = persisted as {
          lastBot?: { userId?: string; botId?: string } | null;
        } | null;
        return old?.lastBot?.userId && old.lastBot.botId
          ? { byUser: { [old.lastBot.userId]: old.lastBot.botId } }
          : { byUser: {} };
      },
    },
  ),
);

/** 当前用户的「上次打开的机器人」——还没登录或没记忆时返回 null。 */
export function useLastBotId(): string | null {
  const byUser = useLastBotStore((s) => s.byUser);
  const userId = useAuthStore((s) => s.user?.id);
  return userId ? (byUser[userId] ?? null) : null;
}
