import {
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query";
import {
  botApi,
  type BotConnectionTestInput,
  type CreateBotInput,
  type UpdateBotInput,
} from "@/lib/api/bots";
import { useAuthStore } from "@/lib/store/auth-store";

/**
 * 机器人管理查询都挂在 [ROOT, userId, ...] 下：缓存按用户身份隔离，
 * 没有用户（未登录或正在引导）时不读取。主线（消息/调用/run）的键
 * 由 use-bot-chat 复用同一约定。
 */
export const BOTS_ROOT = "bots";

export const botKeys = {
  list: (userId: string | null) => [BOTS_ROOT, userId, "list"],
  detail: (userId: string | null, botId: string) => [
    BOTS_ROOT,
    userId,
    "detail",
    botId,
  ],
  messages: (userId: string | null, botId: string) => [
    BOTS_ROOT,
    userId,
    "messages",
    botId,
  ],
  calls: (userId: string | null, botId: string) => [
    BOTS_ROOT,
    userId,
    "calls",
    botId,
  ],
  runs: (userId: string | null, botId: string) => [
    BOTS_ROOT,
    userId,
    "runs",
    botId,
  ],
  authorizations: (userId: string | null, botId: string) => [
    BOTS_ROOT,
    userId,
    "authorizations",
    botId,
  ],
};

function useUserId() {
  return useAuthStore((state) => state.user?.id ?? null);
}

/** 用户身份变化时清空本功能缓存，保证后一个用户看不到前一个用户的数据。 */
export function watchBotsIdentity(queryClient: QueryClient) {
  return useAuthStore.subscribe((state, previous) => {
    if (state.user?.id === previous.user?.id) return;
    void queryClient.cancelQueries({ queryKey: [BOTS_ROOT] });
    queryClient.removeQueries({ queryKey: [BOTS_ROOT] });
  });
}

export function useBots() {
  const userId = useUserId();
  return useQuery({
    queryKey: botKeys.list(userId),
    queryFn: async ({ signal }) => (await botApi.list(signal)).data.items,
    enabled: Boolean(userId),
    refetchOnMount: "always",
  });
}

export function useBot(botId: string) {
  const userId = useUserId();
  return useQuery({
    queryKey: botKeys.detail(userId, botId),
    queryFn: async ({ signal }) => (await botApi.get(botId, signal)).data,
    enabled: Boolean(userId && botId),
    refetchOnMount: "always",
  });
}

function useInvalidateBots() {
  const queryClient = useQueryClient();
  const userId = useUserId();
  return () => queryClient.invalidateQueries({ queryKey: [BOTS_ROOT, userId] });
}

export function useCreateBot() {
  const invalidate = useInvalidateBots();
  return useMutation({
    mutationFn: (input: CreateBotInput) => botApi.create(input),
    onSuccess: invalidate,
  });
}

/** 更新成功后配置对下个 run 生效；当前 run 使用服务端快照，无需特殊处理。 */
export function useUpdateBot(botId: string) {
  const invalidate = useInvalidateBots();
  return useMutation({
    mutationFn: (input: UpdateBotInput) => botApi.update(botId, input),
    onSuccess: invalidate,
  });
}

export function useDeleteBot() {
  const invalidate = useInvalidateBots();
  return useMutation({
    mutationFn: (botId: string) => botApi.delete(botId),
    onSuccess: invalidate,
  });
}

/** 测试连接不落库、不进主线；调用方自行决定用表单值还是已存配置。 */
export function useTestBotConnection() {
  return useMutation({
    mutationFn: (input: BotConnectionTestInput) => botApi.testConnection(input),
  });
}

export function useBotAuthorizations(botId: string) {
  const userId = useUserId();
  return useQuery({
    queryKey: botKeys.authorizations(userId, botId),
    queryFn: async ({ signal }) =>
      (await botApi.authorizations(botId, signal)).data.items,
    enabled: Boolean(userId && botId),
    refetchOnMount: "always",
  });
}

export function useRevokeAuthorization(botId: string) {
  const queryClient = useQueryClient();
  const userId = useUserId();
  return useMutation({
    mutationFn: (authorizationId: string) =>
      botApi.revokeAuthorization(botId, authorizationId),
    onSettled: () =>
      queryClient.invalidateQueries({
        queryKey: botKeys.authorizations(userId, botId),
      }),
  });
}
