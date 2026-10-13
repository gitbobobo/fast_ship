import {
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query";
import { githubTokenApi } from "@/lib/api/github-tokens";
import { useAuthStore } from "@/lib/store/auth-store";

const ROOT = "github-tokens";

function useUserId() {
  return useAuthStore((state) => state.user?.id ?? null);
}

/**
 * 用户身份变化（登出、切换账号）时清掉 Token 库缓存，
 * 后一个用户看不到前一个用户的标题、前缀和检测信息。
 */
export function watchGithubTokensIdentity(queryClient: QueryClient) {
  return useAuthStore.subscribe((state, previous) => {
    if (state.user?.id === previous.user?.id) return;
    void queryClient.cancelQueries({ queryKey: [ROOT] });
    queryClient.removeQueries({ queryKey: [ROOT] });
  });
}

export function useGithubTokens() {
  const userId = useUserId();
  return useQuery({
    queryKey: [ROOT, userId],
    queryFn: async () => {
      const res = await githubTokenApi.list();
      return res.data;
    },
    enabled: Boolean(userId),
  });
}

export function useCreateGithubToken() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: githubTokenApi.create,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: [ROOT] });
    },
  });
}

export function useUpdateGithubToken() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, ...data }: { id: string; title?: string; token?: string }) =>
      githubTokenApi.update(id, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: [ROOT] });
    },
  });
}

export function useDeleteGithubToken() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: githubTokenApi.delete,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: [ROOT] });
    },
  });
}

export function useDetectGithubToken() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: githubTokenApi.detect,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: [ROOT] });
    },
  });
}
