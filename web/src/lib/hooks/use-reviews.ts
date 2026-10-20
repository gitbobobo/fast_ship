import {
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query";
import {
  reviewApi,
  type ReviewSettingsInput,
} from "@/lib/api/reviews";
import { useAuthStore } from "@/lib/store/auth-store";

// PR 审查查询（INT-135）。缓存按用户隔离（与 use-threads 同口径）。
const ROOT = "reviews";

function useUserId() {
  return useAuthStore((state) => state.user?.id ?? null);
}

const keys = {
  projects: (userId: string | null) => [ROOT, userId, "projects"],
  settings: (userId: string | null, projectId: string) => [
    ROOT,
    userId,
    "settings",
    projectId,
  ],
  records: (userId: string | null, projectId: string, limit: number) => [
    ROOT,
    userId,
    "records",
    projectId,
    limit,
  ],
};

/** 用户身份变化时清空本功能缓存，保证后一个用户看不到前一个用户的数据。 */
export function watchReviewsIdentity(queryClient: QueryClient) {
  return useAuthStore.subscribe((state, previous) => {
    if (state.user?.id === previous.user?.id) return;
    void queryClient.cancelQueries({ queryKey: [ROOT] });
    queryClient.removeQueries({ queryKey: [ROOT] });
  });
}

/** 当前用户所有已关联 GitHub 仓库项目的审查总览。 */
export function useReviewProjects() {
  const userId = useUserId();
  return useQuery({
    queryKey: keys.projects(userId),
    queryFn: async () => (await reviewApi.listProjects()).data.items,
    enabled: Boolean(userId),
  });
}

// 设置详情 gcTime: 0 —— 离开设置页即丢弃缓存。设置表单只用首次取到的详情
// 初始化（保存成功后不重置输入），若复用旧缓存，别处对同一设置的写就会被
// 表单里的过期字段（如 enabled）覆盖写回。
export function useReviewSettings(projectId: string | null) {
  const userId = useUserId();
  return useQuery({
    queryKey: keys.settings(userId, projectId ?? ""),
    queryFn: async () =>
      (await reviewApi.getSettings(projectId as string)).data,
    enabled: Boolean(projectId && userId),
    gcTime: 0,
  });
}

export function useReviewRecords(projectId: string | null, limit = 20) {
  const userId = useUserId();
  return useQuery({
    queryKey: keys.records(userId, projectId ?? "", limit),
    queryFn: async () =>
      (await reviewApi.listRecords(projectId as string, limit)).data.items,
    enabled: Boolean(projectId && userId),
  });
}

function useInvalidateReviews() {
  const queryClient = useQueryClient();
  const userId = useUserId();
  return () => {
    queryClient.invalidateQueries({ queryKey: [ROOT, userId] });
  };
}

export function useUpdateReviewSettings(projectId: string) {
  const invalidate = useInvalidateReviews();
  return useMutation({
    mutationFn: (input: ReviewSettingsInput) =>
      reviewApi.updateSettings(projectId, input),
    onSuccess: invalidate,
  });
}

export function useRerunReview(projectId: string) {
  const invalidate = useInvalidateReviews();
  return useMutation({
    mutationFn: (input: { prNumber: number; repoFullName: string }) =>
      reviewApi.rerun(projectId, input.prNumber, input.repoFullName),
    onSuccess: invalidate,
  });
}
