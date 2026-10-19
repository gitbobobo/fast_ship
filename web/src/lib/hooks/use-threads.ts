import { useEffect } from "react";
import {
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
  type QueryFilters,
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
import {
  subscribeThreadStream,
  type ThreadStreamEvent,
} from "@/lib/thread-stream";
import { useAuthStore } from "@/lib/store/auth-store";
import { useThreadListStore } from "@/lib/store/thread-list-store";

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

/**
 * 先取消在飞请求、再失效。TanStack Query 5.96 的 cancelRefetch 只在「已有
 * data」时取消重写；首次加载（data 仍为 undefined）会复用原 promise，成功
 * 响应又把 isInvalidated 清回 false——服务端取旧快照、返回前推送到达时，
 * 这次失效会被吞掉，页面停在旧数据上。先取消再失效才能保证推送不丢。
 */
function refreshThreadQueries(queryClient: QueryClient, filters: QueryFilters) {
  void queryClient
    .cancelQueries(filters)
    .then(() => queryClient.invalidateQueries(filters));
}

/**
 * SSE 事件 → 查询失效的映射。纯函数便于单测；只用 invalidateQueries，
 * removeQueries 会让已打开页面闪白。
 * - 无 environment_id：全局刷新（含连接建立首帧的 full），整个用户子树失效。
 * - 有 environment_id：列表总是失效；full 或带 state（连接状态翻转，可能
 *   与同环境的线程增量合并在同一条事件里）时该环境全部详情失效；否则只失效
 *   thread_ids 命中的详情，不碰无关环境。thread_ids 缺失按全量处理。
 */
export function applyThreadStreamEvent(
  queryClient: QueryClient,
  userId: string,
  event: ThreadStreamEvent,
) {
  if (!event.environment_id) {
    refreshThreadQueries(queryClient, { queryKey: [ROOT, userId] });
    return;
  }
  const environmentId = event.environment_id;
  refreshThreadQueries(queryClient, { queryKey: keys.threads(userId) });
  const detailPrefix = [ROOT, userId, "thread", environmentId];
  // state 变化说明连接刚恢复/断开：断连期间失败的详情重读要全部重试，
  // 不能因为同一事件里还合进了别的线程增量就只刷那一两个线程。
  if (event.full || event.state) {
    refreshThreadQueries(queryClient, { queryKey: detailPrefix });
    return;
  }
  refreshThreadQueries(queryClient, {
    queryKey: detailPrefix,
    predicate: (query) =>
      !event.thread_ids?.length ||
      event.thread_ids.includes(String(query.queryKey[4])),
  });
}

/**
 * 订阅线程壳 SSE（应用根部挂一次）：登录期间保持连接，服务端推送经
 * applyThreadStreamEvent 失效对应查询并触发重读；卸载、登出、换用户都关闭订阅。
 */
export function useThreadStream(queryClient: QueryClient) {
  const userId = useUserId();
  useEffect(() => {
    if (!userId) return;
    const subscription = subscribeThreadStream({
      onEvent: (event) => applyThreadStreamEvent(queryClient, userId, event),
    });
    return () => subscription.close();
  }, [queryClient, userId]);
}

/**
 * 列表在打开、窗口重新聚焦、手动刷新时更新；运行中的变化由线程壳 SSE
 * 推送失效（见 useThreadStream），不做定时轮询。
 */
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
 * 详情在打开、窗口重新聚焦、写操作结算与 SSE 推送失效时重读，不做定时轮询。
 * 读取失败保留旧内容并由页面标陈旧，用户手动重试恢复。
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

/**
 * 新线程只在发送首条消息时创建；详情由跳转后的页面读取。
 *
 * 创建受理后 mutation 立即结束，调用方据此马上反馈/跳转；列表刷新只在后台进行，
 * 不再让按钮等待整轮列表重读。成功与失败都触发刷新：写入可能已被上游受理而响应
 * 未达，列表要能自行核对（稳定 command_id/thread_id 的幂等重试语义不变）。
 */
export function useCreateThread(environmentId: string) {
  const queryClient = useQueryClient();
  const userId = useUserId();
  const refreshList = () => {
    void queryClient.invalidateQueries({ queryKey: keys.threads(userId) });
  };
  return useMutation({
    mutationFn: (input: CreateThreadInput) =>
      threadApi.create(environmentId, input),
    onSuccess: refreshList,
    onError: refreshList,
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
