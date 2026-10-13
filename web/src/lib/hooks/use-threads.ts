import {
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query";
import {
  environmentApi,
  threadApi,
  type EnvironmentInput,
} from "@/lib/api/threads";
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

/** 详情只在线程运行中每隔几秒轮询，状态变为非运行后自动停止。 */
export function useThreadDetail(environmentId: string, threadId: string) {
  const userId = useUserId();
  return useQuery({
    queryKey: keys.detail(userId, environmentId, threadId),
    queryFn: async ({ signal }) =>
      (await threadApi.detail(environmentId, threadId, signal)).data,
    enabled: Boolean(userId && environmentId && threadId),
    refetchOnMount: "always",
    refetchInterval: (query) =>
      query.state.data?.thread.status === "running"
        ? THREAD_DETAIL_POLL_MS
        : false,
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
