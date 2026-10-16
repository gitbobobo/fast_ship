import {
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query";
import {
  quotaApi,
  type CreateQuotaAccountInput,
  type QuotaProbeInput,
  type UpdateQuotaAccountInput,
} from "@/lib/api/quota";
import { useAuthStore } from "@/lib/store/auth-store";

const ROOT = "quota";

/** 额度数据变化周期（服务端每 10 分钟抓一轮）：页面停留时每分钟对齐一次。 */
const ACCOUNTS_REFETCH_INTERVAL_MS = 60_000;

function useUserId() {
  return useAuthStore((state) => state.user?.id ?? null);
}

/**
 * 用户身份变化（登出、切换账号）时清掉套餐额度缓存，
 * 后一个用户看不到前一个用户的账号标签与用量数据。
 */
export function watchQuotaIdentity(queryClient: QueryClient) {
  return useAuthStore.subscribe((state, previous) => {
    if (state.user?.id === previous.user?.id) return;
    void queryClient.cancelQueries({ queryKey: [ROOT] });
    queryClient.removeQueries({ queryKey: [ROOT] });
  });
}

/** 供应商元数据（凭据表单结构）：全局内容，跟随会话。 */
export function useQuotaProviders() {
  const userId = useUserId();
  return useQuery({
    queryKey: [ROOT, userId, "providers"],
    queryFn: async () => (await quotaApi.listProviders()).data,
    enabled: Boolean(userId),
    staleTime: 5 * 60_000,
  });
}

export function useQuotaAccounts() {
  const userId = useUserId();
  return useQuery({
    queryKey: [ROOT, userId, "accounts"],
    queryFn: async () => (await quotaApi.listAccounts()).data,
    enabled: Boolean(userId),
    refetchOnMount: "always",
    refetchInterval: ACCOUNTS_REFETCH_INTERVAL_MS,
  });
}

function useInvalidateQuota() {
  const queryClient = useQueryClient();
  const userId = useUserId();
  return () => queryClient.invalidateQueries({ queryKey: [ROOT, userId] });
}

export function useCreateQuotaAccount() {
  const invalidate = useInvalidateQuota();
  return useMutation({
    mutationFn: (input: CreateQuotaAccountInput) => quotaApi.createAccount(input),
    onSuccess: invalidate,
  });
}

export function useUpdateQuotaAccount() {
  const invalidate = useInvalidateQuota();
  return useMutation({
    mutationFn: ({ id, ...input }: { id: string } & UpdateQuotaAccountInput) =>
      quotaApi.updateAccount(id, input),
    onSuccess: invalidate,
  });
}

export function useDeleteQuotaAccount() {
  const invalidate = useInvalidateQuota();
  return useMutation({
    mutationFn: (id: string) => quotaApi.deleteAccount(id),
    onSuccess: invalidate,
  });
}

/** 手动刷新：立即抓一次落库；距上次抓取过近服务端返回 42900。 */
export function useRefreshQuotaAccount() {
  const invalidate = useInvalidateQuota();
  return useMutation({
    mutationFn: (id: string) => quotaApi.refreshAccount(id),
    onSettled: invalidate,
  });
}

/** 凭据连通性测试（不落库、不改账号状态），供添加/编辑对话框就地展示结果。 */
export function useQuotaProbe() {
  return useMutation({
    mutationFn: (input: QuotaProbeInput) => quotaApi.probe(input),
  });
}
