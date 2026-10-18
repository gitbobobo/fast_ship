import {
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query";
import {
  integrationApi,
  type UpdateGitHubAppIntegrationInput,
} from "@/lib/api/integrations";
import { useAuthStore } from "@/lib/store/auth-store";

const ROOT = "integrations";

function useUserId() {
  return useAuthStore((state) => state.user?.id ?? null);
}

/**
 * 用户身份变化（登出、切换账号）时清掉集成缓存，
 * 后一个用户看不到前一个用户的 App ID、密钥状态与检测信息。
 */
export function watchIntegrationsIdentity(queryClient: QueryClient) {
  return useAuthStore.subscribe((state, previous) => {
    if (state.user?.id === previous.user?.id) return;
    void queryClient.cancelQueries({ queryKey: [ROOT] });
    queryClient.removeQueries({ queryKey: [ROOT] });
  });
}

// 用户级配置，接口按当前 JWT 用户读写；缓存按登录用户隔离（与 github-tokens 同一口径）。
export function useGitHubAppIntegration() {
  const userId = useUserId();
  return useQuery({
    queryKey: [ROOT, userId],
    queryFn: async () => {
      const res = await integrationApi.getGitHubApp();
      return res.data;
    },
    enabled: Boolean(userId),
  });
}

// 响应落地时当前登录用户是否仍是发起请求的人：onMutate 在 mutate 时
// 同步执行，把当时的 userId 存进 mutation context；不能用渲染期闭包——
// mutation 在飞期间组件重渲染会把 onSuccess 换成新 userId 的闭包。
function sameUserSignedIn(requesterId: string | null) {
  return requesterId === (useAuthStore.getState().user?.id ?? null);
}

export function useUpdateGitHubAppIntegration() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: UpdateGitHubAppIntegrationInput) =>
      integrationApi.updateGitHubApp(data),
    onMutate: () => useAuthStore.getState().user?.id ?? null,
    // 响应已含保存后的检测状态，直接写缓存，无需再拉一次。
    // 已切换账号时丢弃：A 的慢响应不能写进 B 的缓存。
    onSuccess: (res, _variables, requesterId) => {
      if (!sameUserSignedIn(requesterId)) return;
      queryClient.setQueryData([ROOT, requesterId], res.data);
    },
  });
}

export function useTestGitHubAppIntegration() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => integrationApi.testGitHubApp(),
    onMutate: () => useAuthStore.getState().user?.id ?? null,
    // source=config 时检测结果不落库：GET 仍是 unchecked，只能用本次响应更新缓存。
    // 与保存同一口径：身份已变化时丢弃，不写给另一个用户。
    onSuccess: (res, _variables, requesterId) => {
      if (!sameUserSignedIn(requesterId)) return;
      queryClient.setQueryData([ROOT, requesterId], res.data);
    },
  });
}
