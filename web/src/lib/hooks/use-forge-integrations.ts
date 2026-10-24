import {
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query";
import { forgeIntegrationsApi } from "@/lib/api/forge-integrations";
import { useAuthStore } from "@/lib/store/auth-store";

const ROOT = "forge-integrations";

function useUserId() {
  return useAuthStore((state) => state.user?.id ?? null);
}

/**
 * 用户身份变化（登出、切换账号）时清掉代码仓集成缓存，
 * 后一个用户看不到前一个用户会话里的集成列表与凭证状态。
 */
export function watchForgeIntegrationsIdentity(queryClient: QueryClient) {
  return useAuthStore.subscribe((state, previous) => {
    if (state.user?.id === previous.user?.id) return;
    void queryClient.cancelQueries({ queryKey: [ROOT] });
    queryClient.removeQueries({ queryKey: [ROOT] });
  });
}

// 集成列表所有登录用户可读、写操作仅管理员；缓存按登录用户隔离（与
// deployment 同一口径），非管理员的 40305 结果也不串号。
export function useForgeIntegrations() {
  const userId = useUserId();
  return useQuery({
    queryKey: [ROOT, userId, "list"],
    queryFn: async () => {
      const res = await forgeIntegrationsApi.list();
      return res.data;
    },
    enabled: Boolean(userId),
  });
}

// 响应落地时当前登录用户是否仍是发起请求的人：mutation 在飞期间组件
// 重渲染会把 onSuccess 换成新 userId 的闭包，context 绑定的才是发起时的人。
function sameUserSignedIn(requesterId: string | null) {
  return requesterId === (useAuthStore.getState().user?.id ?? null);
}

// 缓存存的是整张列表（内置行 + forgejo 行），写操作只动其中一行：
// 成功后失效重拉，不做局部拼接。
function useInvalidateOwnList() {
  const queryClient = useQueryClient();
  return (requesterId: string | null) => {
    if (!sameUserSignedIn(requesterId)) return;
    void queryClient.invalidateQueries({
      queryKey: [ROOT, requesterId, "list"],
    });
  };
}

export function useCreateForgeIntegration() {
  const invalidate = useInvalidateOwnList();
  return useMutation({
    mutationFn: (input: CreateForgeIntegrationRequest) =>
      forgeIntegrationsApi.create(input),
    onMutate: () => useAuthStore.getState().user?.id ?? null,
    onSuccess: (_res, _variables, requesterId) => invalidate(requesterId),
  });
}

export function useUpdateForgeIntegration() {
  const invalidate = useInvalidateOwnList();
  return useMutation({
    mutationFn: ({
      id,
      input,
    }: {
      id: string;
      input: UpdateForgeIntegrationRequest;
    }) => forgeIntegrationsApi.update(id, input),
    onMutate: () => useAuthStore.getState().user?.id ?? null,
    onSuccess: (_res, _variables, requesterId) => invalidate(requesterId),
  });
}

export function useDeleteForgeIntegration() {
  const invalidate = useInvalidateOwnList();
  return useMutation({
    mutationFn: (id: string) => forgeIntegrationsApi.remove(id),
    onMutate: () => useAuthStore.getState().user?.id ?? null,
    onSuccess: (_res, _variables, requesterId) => invalidate(requesterId),
  });
}

// 检测结论不落库（契约注明只随本次响应返回），调用方拿响应直接展示。
export function useTestForgeIntegration() {
  return useMutation({
    mutationFn: (id: string) => forgeIntegrationsApi.test(id),
  });
}
