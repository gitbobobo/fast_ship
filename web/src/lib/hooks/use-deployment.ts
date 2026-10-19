import {
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query";
import { deploymentApi } from "@/lib/api/deployment";
import { useAuthStore } from "@/lib/store/auth-store";

const ROOT = "deployment";

function useUserId() {
  return useAuthStore((state) => state.user?.id ?? null);
}

/**
 * 用户身份变化（登出、切换账号）时清掉部署设置与部署级 App 缓存，
 * 后一个用户看不到前一个用户会话里的 App ID、密钥状态与检测信息。
 */
export function watchDeploymentIdentity(queryClient: QueryClient) {
  return useAuthStore.subscribe((state, previous) => {
    if (state.user?.id === previous.user?.id) return;
    void queryClient.cancelQueries({ queryKey: [ROOT] });
    queryClient.removeQueries({ queryKey: [ROOT] });
  });
}

// 部署级配置所有登录用户可读、仅管理员可写；缓存按登录用户隔离（与
// github-tokens 同一口径），非管理员拿到的 40305 结果也不串号。
export function useDeploymentSettings() {
  const userId = useUserId();
  return useQuery({
    queryKey: [ROOT, userId, "settings"],
    queryFn: async () => {
      const res = await deploymentApi.getSettings();
      return res.data;
    },
    enabled: Boolean(userId),
  });
}

export function useGitHubAppDeployment() {
  const userId = useUserId();
  return useQuery({
    queryKey: [ROOT, userId, "github-app"],
    queryFn: async () => {
      const res = await deploymentApi.getGitHubApp();
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

export function useUpdateDeploymentSettings() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: UpdateDeploymentSettingsRequest) =>
      deploymentApi.updateSettings(data),
    onMutate: () => useAuthStore.getState().user?.id ?? null,
    // 响应是回读后的设置，直接写缓存；已切换账号时丢弃。
    onSuccess: (res, _variables, requesterId) => {
      if (!sameUserSignedIn(requesterId)) return;
      queryClient.setQueryData([ROOT, requesterId, "settings"], res.data);
    },
  });
}

export function useUpdateGitHubAppDeployment() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: UpdateGitHubAppDeploymentRequest) =>
      deploymentApi.updateGitHubApp(data),
    onMutate: () => useAuthStore.getState().user?.id ?? null,
    // 响应已含保存后的检测状态，直接写缓存，无需再拉一次。
    // 已切换账号时丢弃：A 的慢响应不能写进 B 的缓存。
    onSuccess: (res, _variables, requesterId) => {
      if (!sameUserSignedIn(requesterId)) return;
      queryClient.setQueryData([ROOT, requesterId, "github-app"], res.data);
    },
  });
}

export function useTestGitHubAppDeployment() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => deploymentApi.testGitHubApp(),
    onMutate: () => useAuthStore.getState().user?.id ?? null,
    // source=config 时检测结果不落库：GET 仍是 unchecked，只能用本次响应更新缓存。
    // 与保存同一口径：身份已变化时丢弃，不写给另一个用户。
    onSuccess: (res, _variables, requesterId) => {
      if (!sameUserSignedIn(requesterId)) return;
      queryClient.setQueryData([ROOT, requesterId, "github-app"], res.data);
    },
  });
}

// 创建 manifest 不落任何缓存：响应交给页面做表单 POST 跳转。
// 完成 manifest（回调页）同理不经 hook：它是一次性动作，结果只用于跳转，
// 页面重新进入时会重新拉取，不存在需要维护的缓存。
export function useCreateGitHubAppManifest() {
  return useMutation({
    mutationFn: (input: GitHubAppManifestRequest = {}) =>
      deploymentApi.createManifest(input),
  });
}
