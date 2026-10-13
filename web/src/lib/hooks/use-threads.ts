import {
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query";
import {
  environmentApi,
  threadApi,
  type CreateThreadInput,
  type EnvironmentInput,
  type RespondInput,
  type SendMessageInput,
  type ChatAttachment,
} from "@/lib/api/threads";
import { shouldPollThread } from "@/lib/threads-compose";
import { useAuthStore } from "@/lib/store/auth-store";
import { useThreadListStore } from "@/lib/store/thread-list-store";

export const THREAD_DETAIL_POLL_MS = 3000;

/**
 * 本功能的所有查询都挂在 [ROOT, userId, ...] 下：缓存按用户身份隔离，
 * 没有用户（未登录或正在引导）时不读取。
 */
const ROOT = "t3-threads";

const keys = {
  environments: (userId: string | null) => [ROOT, userId, "environments"],
  threads: (userId: string | null) => [ROOT, userId, "threads"],
  detail: (userId: string | null, environmentId: string, threadId: string) => [
    ROOT,
    userId,
    "thread",
    environmentId,
    threadId,
  ],
  composer: (userId: string | null, environmentId: string) => [
    ROOT,
    userId,
    "composer",
    environmentId,
  ],
  workspace: (
    userId: string | null,
    environmentId: string,
    projectId: string,
    query: string,
  ) => [ROOT, userId, "workspace", environmentId, projectId, query],
  attachment: (
    userId: string | null,
    environmentId: string,
    attachmentId: string,
  ) => [ROOT, userId, "attachment", environmentId, attachmentId],
  item: (
    userId: string | null,
    environmentId: string,
    threadId: string,
    itemId: string,
    revision: string,
  ) => [ROOT, userId, "item", environmentId, threadId, itemId, revision],
};

/** 同一用户、环境、线程、条目（只有 revision 不同）才算同一份正文。 */
function sameItem(previous: readonly unknown[], current: readonly unknown[]) {
  return (
    previous.length === current.length &&
    previous.slice(0, -1).every((part, i) => part === current[i])
  );
}

function useUserId() {
  return useAuthStore((state) => state.user?.id ?? null);
}

/**
 * 用户身份变化（登出、切换账号）时，取消在飞请求、清掉本功能的缓存和筛选，
 * 保证后一个用户看不到前一个用户的数据。在应用根部订阅一次。
 */
export function watchThreadsIdentity(queryClient: QueryClient) {
  return useAuthStore.subscribe((state, previous) => {
    if (state.user?.id === previous.user?.id) return;
    void queryClient.cancelQueries({ queryKey: [ROOT] });
    queryClient.removeQueries({ queryKey: [ROOT] });
    useThreadListStore.getState().resetFilters();
  });
}

export function useEnvironments() {
  const userId = useUserId();
  return useQuery({
    queryKey: keys.environments(userId),
    queryFn: async ({ signal }) => (await environmentApi.list(signal)).data.items,
    enabled: Boolean(userId),
  });
}

function useInvalidateThreads() {
  const queryClient = useQueryClient();
  const userId = useUserId();
  return () => queryClient.invalidateQueries({ queryKey: [ROOT, userId] });
}

export function useCreateEnvironment() {
  const invalidate = useInvalidateThreads();
  return useMutation({
    mutationFn: environmentApi.create,
    onSuccess: invalidate,
  });
}

export function useUpdateEnvironment() {
  const invalidate = useInvalidateThreads();
  return useMutation({
    mutationFn: ({ id, ...data }: EnvironmentInput & { id: string }) =>
      environmentApi.update(id, data),
    onSuccess: invalidate,
  });
}

export function useDeleteEnvironment() {
  const invalidate = useInvalidateThreads();
  return useMutation({
    mutationFn: environmentApi.delete,
    onSuccess: invalidate,
  });
}

/** 列表在打开、窗口重新聚焦、手动刷新时更新，不做定时轮询。 */
export function useThreads() {
  const userId = useUserId();
  return useQuery({
    queryKey: keys.threads(userId),
    queryFn: async ({ signal }) => (await threadApi.list(signal)).data,
    enabled: Boolean(userId),
    refetchOnMount: "always",
    refetchOnWindowFocus: true,
  });
}

/**
 * 详情在线程有进展时每隔几秒轮询：运行中、停止中、有待作答请求、
 * 未暂停且队列有待推进项（见 shouldPollThread）。读取失败后自动停止，
 * 保留旧内容并由页面标陈旧，用户手动重试恢复；窗口重新聚焦时刷新。
 */
export function useThreadDetail(environmentId: string, threadId: string) {
  const userId = useUserId();
  return useQuery({
    queryKey: keys.detail(userId, environmentId, threadId),
    queryFn: async ({ signal }) =>
      (await threadApi.detail(environmentId, threadId, signal)).data,
    enabled: Boolean(userId && environmentId && threadId),
    refetchOnMount: "always",
    refetchOnWindowFocus: true,
    refetchInterval: (query) =>
      query.state.status !== "error" && shouldPollThread(query.state.data)
        ? THREAD_DETAIL_POLL_MS
        : false,
  });
}

/** 新建页用：启动器选项（项目表 + provider 配置），只在需要时读取。 */
export function useComposerOptions(environmentId: string) {
  const userId = useUserId();
  return useQuery({
    queryKey: keys.composer(userId, environmentId),
    queryFn: async ({ signal }) =>
      (await threadApi.composerOptions(environmentId, signal)).data,
    enabled: Boolean(userId && environmentId),
  });
}

export function useWorkspaceOptions(
  environmentId: string,
  projectId: string,
  query = "",
) {
  const userId = useUserId();
  return useQuery({
    queryKey: keys.workspace(userId, environmentId, projectId, query),
    queryFn: async ({ signal }) =>
      (await threadApi.workspaceOptions(environmentId, projectId, query, signal))
        .data,
    enabled: Boolean(userId && environmentId && projectId),
    placeholderData: (previous, previousQuery) => {
      if (!previousQuery) return undefined;
      const prev = previousQuery.queryKey;
      const curr = keys.workspace(userId, environmentId, projectId, query);
      // 同一用户/环境/项目才复用（筛选分支的 query 可变）；换项目绝不能露出上一份 cwd/分支。
      return prev[1] === curr[1] && prev[3] === curr[3] && prev[4] === curr[4]
        ? previous
        : undefined;
    },
  });
}

/** 附件缩略图：用 JWT 经 Fast Ship 代理拉成 Blob，调用方自行生成/回收 object URL。 */
export function useAttachmentBlob(environmentId: string, attachmentId: string) {
  const userId = useUserId();
  return useQuery({
    queryKey: keys.attachment(userId, environmentId, attachmentId),
    queryFn: ({ signal }) =>
      threadApi.attachmentContent(environmentId, attachmentId, signal),
    enabled: Boolean(userId && environmentId && attachmentId),
    staleTime: Infinity,
    retry: 0,
  });
}

/**
 * 每次写操作之后立刻重读该线程详情并让列表失效；成功、失败、超时都刷新，
 * 因为写入可能已被服务端受理。返回的 promise 在详情重读完成后才结束，
 * 所以 mutation 在刷新确认前保持 pending，不会在 RPC 返回时就当成已生效。
 */
export function useRefreshThread(environmentId: string, threadId: string) {
  const queryClient = useQueryClient();
  const userId = useUserId();
  return () =>
    Promise.all([
      queryClient.invalidateQueries({
        queryKey: keys.detail(userId, environmentId, threadId),
      }),
      queryClient.invalidateQueries({ queryKey: keys.threads(userId) }),
    ]);
}

function useThreadMutation<V, R>(
  environmentId: string,
  threadId: string,
  mutationFn: (variables: V) => Promise<R>,
  onSuccess?: () => void,
) {
  const refresh = useRefreshThread(environmentId, threadId);
  return useMutation({ mutationFn, onSuccess, onSettled: refresh });
}

export function useSendThreadMessage(environmentId: string, threadId: string) {
  return useThreadMutation(environmentId, threadId, (input: SendMessageInput) =>
    threadApi.send(environmentId, threadId, input),
  );
}

export function useStopThread(environmentId: string, threadId: string) {
  return useThreadMutation(environmentId, threadId, (_: void) =>
    threadApi.stop(environmentId, threadId),
  );
}

export function useResumeQueue(environmentId: string, threadId: string) {
  return useThreadMutation(environmentId, threadId, (_: void) =>
    threadApi.resumeQueue(environmentId, threadId),
  );
}

export function useSteerQueued(environmentId: string, threadId: string) {
  return useThreadMutation(environmentId, threadId, (runId: string) =>
    threadApi.steer(environmentId, threadId, runId),
  );
}

export function useCancelQueued(environmentId: string, threadId: string) {
  return useThreadMutation(environmentId, threadId, (runId: string) =>
    threadApi.cancelQueued(environmentId, threadId, runId),
  );
}

export function useEditQueued(environmentId: string, threadId: string) {
  return useThreadMutation(
    environmentId,
    threadId,
    (v: { runId: string; text: string; attachments?: ChatAttachment[] }) =>
      threadApi.editQueued(environmentId, threadId, v.runId, {
        text: v.text,
        attachments: v.attachments,
      }),
  );
}

export function useReorderQueued(environmentId: string, threadId: string) {
  return useThreadMutation(
    environmentId,
    threadId,
    (v: { runId: string; beforeRunId: string | null }) =>
      threadApi.reorderQueued(environmentId, threadId, v.runId, v.beforeRunId),
  );
}

/** onSuccess 在刷新前、且不依赖组件仍挂载时执行（应答后卡片会被刷新结果替换）。 */
export function useRespondRequest(
  environmentId: string,
  threadId: string,
  onSuccess?: () => void,
) {
  return useThreadMutation(
    environmentId,
    threadId,
    (v: { requestId: string; input: RespondInput }) =>
      threadApi.respond(environmentId, threadId, v.requestId, v.input),
    onSuccess,
  );
}

/** 新线程只在发送首条消息时创建；成功后让列表失效，详情由跳转后的页面读取。 */
export function useCreateThread(environmentId: string) {
  const queryClient = useQueryClient();
  const userId = useUserId();
  return useMutation({
    mutationFn: (input: CreateThreadInput) =>
      threadApi.create(environmentId, input),
    onSettled: () =>
      queryClient.invalidateQueries({ queryKey: keys.threads(userId) }),
  });
}

/**
 * 展开折叠项时才读取完整正文。revision 来自详情快照中条目自身的更新时间（缺失时退回
 * 线程更新时间）与条目状态、摘要；只有该条目变化才换新缓存键重新读取，线程里其他条目
 * 更新不会放大请求，运行中的条目也不会停留在旧正文；新正文到达前只对同一用户的
 * 同一条目保留旧内容，身份、环境、线程或条目变了一律不复用。
 */
export function useThreadItemDetail(
  environmentId: string,
  threadId: string,
  itemId: string,
  revision: string,
  enabled: boolean,
) {
  const userId = useUserId();
  const queryKey = keys.item(userId, environmentId, threadId, itemId, revision);
  return useQuery({
    queryKey,
    queryFn: async ({ signal }) =>
      (await threadApi.itemDetail(environmentId, threadId, itemId, signal)).data,
    enabled: Boolean(userId && enabled),
    placeholderData: (previousData, previousQuery) =>
      userId && previousQuery && sameItem(previousQuery.queryKey, queryKey)
        ? previousData
        : undefined,
  });
}
