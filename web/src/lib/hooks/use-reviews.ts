import {
  useEffect,
  useRef,
} from "react";
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

// PR 审查查询（INT-135/INT-141）。缓存按用户隔离（与 use-threads 同口径）。
const ROOT = "reviews";

/** 有进行中 PR 时的自动刷新间隔。 */
export const REVIEW_POLL_INTERVAL = 10_000;

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
  pullRequests: (
    userId: string | null,
    projectId: string | null,
    limit: number,
    offset: number,
  ) => [ROOT, userId, "pull-requests", projectId ?? "", limit, offset],
  pullRequestDetail: (
    userId: string | null,
    projectId: string,
    prNumber: number,
  ) => [ROOT, userId, "pull-request", projectId, prNumber],
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

/**
 * 跨项目 PR 动态（/reviews 首页）。活动响应 counts.in_progress>0 时 10s
 * 轮询，否则不轮询；翻页用固定 limit + offset。切换项目/翻页时不保留上一份
 * 数据，避免把上一个筛选的行画出来并被点击（重新审查会打到错误的 PR）。
 */
export function useReviewPullRequests(
  projectId?: string | null,
  limit = 50,
  offset = 0,
) {
  const userId = useUserId();
  return useQuery({
    queryKey: keys.pullRequests(userId, projectId ?? null, limit, offset),
    queryFn: async () =>
      (
        await reviewApi.listPullRequests({
          projectId: projectId ?? undefined,
          limit,
          offset,
        })
      ).data,
    enabled: Boolean(userId),
    refetchInterval: (query) =>
      (query.state.data?.counts.in_progress ?? 0) > 0
        ? REVIEW_POLL_INTERVAL
        : false,
  });
}

/**
 * 抽屉里的单个 PR 审查详情。live=true（选中项属于进行中组）时 10s 轮询；
 * 组件卸载（抽屉关闭）后自动停止。
 */
export function useReviewPullRequestDetail(
  projectId: string | null,
  prNumber: number | null,
  live: boolean,
) {
  const userId = useUserId();
  const query = useQuery({
    queryKey: keys.pullRequestDetail(userId, projectId ?? "", prNumber ?? 0),
    queryFn: async () =>
      (
        await reviewApi.getPullRequestDetail(
          projectId as string,
          prNumber as number,
        )
      ).data,
    enabled: Boolean(userId && projectId && prNumber),
    refetchInterval: live ? REVIEW_POLL_INTERVAL : false,
  });
  // 从进行中转终态：停止轮询前补取一次，拿到最终结论（列表轮询与详情轮询
  // 不同步，列表先观察到完成时不能只靠停掉定时器）。
  const wasLive = useRef(live);
  const refetch = query.refetch;
  useEffect(() => {
    if (wasLive.current && !live) void refetch();
    wasLive.current = live;
  }, [live, refetch]);
  return query;
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
