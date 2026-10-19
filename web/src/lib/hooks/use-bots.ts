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
  /** 聊天页一致快照：messages/calls/runs/detail 缓存的水合来源。 */
  snapshot: (userId: string | null, botId: string) => [
    BOTS_ROOT,
    userId,
    "snapshot",
    botId,
  ],
  scheduleTasks: (userId: string | null, botId: string) => [
    BOTS_ROOT,
    userId,
    "schedule-tasks",
    botId,
  ],
  scheduleTaskRuns: (userId: string | null, botId: string, taskId: string) => [
    BOTS_ROOT,
    userId,
    "schedule-task-runs",
    botId,
    taskId,
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

/**
 * 左栏列表的轮询间隔：预览（last_message）由服务端计算，而客户端只订阅
 * 当前选中机器人的主线流——停在别的机器人上时，另一个机器人跑完的回合没有
 * 任何事件通知我们（换机器人、多标签页同理），左栏那条预览会停在旧消息。
 * 与定时任务区同一节奏轮询，让列表自己跟上；查询已沿
 * (bot_id, session_kind, seq) 索引早退，一次轮询很便宜。
 */
export const BOT_LIST_POLL_MS = 15_000;

export function useBots() {
  const userId = useUserId();
  return useQuery({
    queryKey: botKeys.list(userId),
    queryFn: async ({ signal }) => (await botApi.list(signal)).data.items,
    enabled: Boolean(userId),
    refetchOnMount: "always",
    // 预览只能由服务端算，轮询补上「没订阅到的那些机器人」的变化。
    refetchInterval: BOT_LIST_POLL_MS,
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

/** 详情弹窗固定拉最近一页运行记录；不做翻页。 */
export const BOT_SCHEDULE_RUNS_PAGE = 20;

/** 列表轮询间隔：任务状态由机器人/调度器在服务端改变，前端没有推送。 */
export const BOT_SCHEDULE_TASKS_POLL_MS = 15_000;

export function useBotScheduleTasks(botId: string) {
  const userId = useUserId();
  return useQuery({
    queryKey: botKeys.scheduleTasks(userId, botId),
    queryFn: async ({ signal }) =>
      (await botApi.scheduleTasks(botId, signal)).data.items,
    enabled: Boolean(userId && botId),
    refetchOnMount: "always",
    // 任务的增改由机器人经工具完成，计划运行也不在聊天事件里：
    // 停在详情页时轮询，保证「下次运行」「最近一次结果」自己跟上。
    refetchInterval: BOT_SCHEDULE_TASKS_POLL_MS,
  });
}

/** taskId 为空不读：详情弹窗关闭时不发请求。 */
export function useBotScheduleTaskRuns(botId: string, taskId: string | null) {
  const userId = useUserId();
  return useQuery({
    queryKey: botKeys.scheduleTaskRuns(userId, botId, taskId ?? ""),
    queryFn: async ({ signal }) =>
      (
        await botApi.scheduleTaskRuns(
          botId,
          taskId as string,
          { limit: BOT_SCHEDULE_RUNS_PAGE },
          signal,
        )
      ).data.items,
    enabled: Boolean(userId && botId && taskId),
    // 弹窗打开期间运行记录也会变（运行中的那次会落终态）：与任务列表同一
    // 节奏轮询，不然界面上的「运行中」会一直停在那里。弹窗关闭即停。
    refetchInterval: BOT_SCHEDULE_TASKS_POLL_MS,
  });
}

/** 清空状态后任务行整体失效：state/state_updated_at 与列表一并刷新。 */
export function useClearBotScheduleTaskState(botId: string) {
  const queryClient = useQueryClient();
  const userId = useUserId();
  return useMutation({
    mutationFn: (taskId: string) =>
      botApi.clearScheduleTaskState(botId, taskId),
    onSettled: () =>
      queryClient.invalidateQueries({
        queryKey: botKeys.scheduleTasks(userId, botId),
      }),
  });
}

/** 启停成功后任务行整体失效：enabled 与 last_run/next_run_at 一并刷新。 */
export function useSetBotScheduleTaskEnabled(botId: string) {
  const queryClient = useQueryClient();
  const userId = useUserId();
  return useMutation({
    mutationFn: ({ taskId, enabled }: { taskId: string; enabled: boolean }) =>
      botApi.setScheduleTaskEnabled(botId, taskId, enabled),
    onSettled: () =>
      queryClient.invalidateQueries({
        queryKey: botKeys.scheduleTasks(userId, botId),
      }),
  });
}
