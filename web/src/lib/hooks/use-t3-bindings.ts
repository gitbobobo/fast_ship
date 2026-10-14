import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { t3BindingApi } from "@/lib/api/t3-bindings";
import { useAuthStore } from "@/lib/store/auth-store";

/**
 * 与线程功能共用根键 [ROOT, userId, ...]：身份切换时
 * watchThreadsIdentity 会一并清空缓存，保证数据按用户隔离。
 */
const ROOT = "t3-threads";

const keys = {
  discovery: (userId: string | null) => [ROOT, userId, "t3-projects"],
  bindings: (userId: string | null, projectId: string) => [
    ROOT,
    userId,
    "t3-bindings",
    projectId,
  ],
};

function useUserId() {
  return useAuthStore((state) => state.user?.id ?? null);
}

/** 各环境已发现的 T3 项目（含单环境失败标记）。 */
export function useT3EnvironmentProjects() {
  const userId = useUserId();
  return useQuery({
    queryKey: keys.discovery(userId),
    queryFn: ({ signal }) => t3BindingApi.environmentProjects(signal),
    enabled: Boolean(userId),
  });
}

export function useProjectT3Bindings(projectId: string) {
  const userId = useUserId();
  return useQuery({
    queryKey: keys.bindings(userId, projectId),
    queryFn: ({ signal }) => t3BindingApi.list(projectId, signal),
    enabled: Boolean(userId && projectId),
  });
}

export function useBindT3Project(projectId: string) {
  const queryClient = useQueryClient();
  const userId = useUserId();
  return useMutation({
    mutationFn: (input: { environmentId: string; t3ProjectId: string }) =>
      t3BindingApi.bind(projectId, input.environmentId, input.t3ProjectId),
    onSettled: () => {
      // 绑定写入快照后重读绑定与发现，让失效判定基于最新数据。
      void queryClient.invalidateQueries({
        queryKey: keys.bindings(userId, projectId),
      });
      void queryClient.invalidateQueries({
        queryKey: keys.discovery(userId),
      });
    },
  });
}

export function useUnbindT3Project(projectId: string) {
  const queryClient = useQueryClient();
  const userId = useUserId();
  return useMutation({
    mutationFn: (environmentId: string) =>
      t3BindingApi.unbind(projectId, environmentId),
    onSettled: () =>
      queryClient.invalidateQueries({
        queryKey: keys.bindings(userId, projectId),
      }),
  });
}
