import {
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query";
import {
  aiProviderApi,
  type CreateAIProviderInput,
  type TestAIProviderInput,
  type UpdateAIProviderInput,
} from "@/lib/api/ai-providers";
import { useAuthStore } from "@/lib/store/auth-store";

/**
 * AI 提供商查询挂在 [ROOT, userId, ...] 下：缓存按用户身份隔离，
 * 没有用户（未登录或正在引导）时不读取。约定与 use-bots 一致。
 */
export const AI_PROVIDERS_ROOT = "ai-providers";

export const aiProviderKeys = {
  list: (userId: string | null) => [AI_PROVIDERS_ROOT, userId, "list"],
  catalog: (userId: string | null) => [AI_PROVIDERS_ROOT, userId, "catalog"],
};

function useUserId() {
  return useAuthStore((state) => state.user?.id ?? null);
}

/** 用户身份变化时清空提供商缓存，保证后一个用户看不到前一个用户的数据。 */
export function watchAIProvidersIdentity(queryClient: QueryClient) {
  return useAuthStore.subscribe((state, previous) => {
    if (state.user?.id === previous.user?.id) return;
    void queryClient.cancelQueries({ queryKey: [AI_PROVIDERS_ROOT] });
    queryClient.removeQueries({ queryKey: [AI_PROVIDERS_ROOT] });
  });
}

export function useAIProviders() {
  const userId = useUserId();
  return useQuery({
    queryKey: aiProviderKeys.list(userId),
    queryFn: async ({ signal }) => (await aiProviderApi.list(signal)).data.items,
    enabled: Boolean(userId),
    refetchOnMount: "always",
  });
}

/**
 * 内置目录（models.dev 子集）随服务端二进制发布，运行期不变：
 * 会话内取一次即可，不随挂载重取。
 */
export function useAIProviderCatalog() {
  const userId = useUserId();
  return useQuery({
    queryKey: aiProviderKeys.catalog(userId),
    queryFn: async ({ signal }) =>
      (await aiProviderApi.catalog(signal)).data.items,
    enabled: Boolean(userId),
    staleTime: Infinity,
  });
}

function useInvalidateAIProviders() {
  const queryClient = useQueryClient();
  const userId = useUserId();
  return () =>
    queryClient.invalidateQueries({ queryKey: aiProviderKeys.list(userId) });
}

export function useCreateAIProvider() {
  const invalidate = useInvalidateAIProviders();
  return useMutation({
    mutationFn: (input: CreateAIProviderInput) => aiProviderApi.create(input),
    onSuccess: invalidate,
  });
}

export function useUpdateAIProvider(providerId: string) {
  const invalidate = useInvalidateAIProviders();
  return useMutation({
    mutationFn: (input: UpdateAIProviderInput) =>
      aiProviderApi.update(providerId, input),
    onSuccess: invalidate,
  });
}

export function useDeleteAIProvider() {
  const invalidate = useInvalidateAIProviders();
  return useMutation({
    mutationFn: (providerId: string) => aiProviderApi.delete(providerId),
    onSuccess: invalidate,
  });
}

/** 测试连接不落库；用表单里的候选配置直接测。 */
export function useTestAIProvider() {
  return useMutation({
    mutationFn: (input: TestAIProviderInput) => aiProviderApi.test(input),
  });
}
