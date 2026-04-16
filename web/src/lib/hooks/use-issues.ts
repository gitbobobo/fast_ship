import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { issueApi } from "@/lib/api/issues";

interface UseIssuesFilters {
  state?: string;
  q?: string;
  label?: string;
  assignee?: string;
  milestone?: string;
  sort?: string;
  page?: number;
  page_size?: number;
}

export function useIssues(projectId: string, filters: UseIssuesFilters = {}) {
  return useQuery({
    queryKey: [
      "projects",
      projectId,
      "issues",
      filters.state ?? "all",
      filters.q ?? "",
      filters.label ?? "",
      filters.assignee ?? "",
      filters.milestone ?? "",
      filters.sort ?? "updated_desc",
      filters.page ?? 1,
      filters.page_size ?? 20,
    ],
    queryFn: async () => {
      const res = await issueApi.list(projectId, filters);
      return res.data;
    },
    enabled: !!projectId,
  });
}

export function useIssueFilterOptions(projectId: string) {
  return useQuery({
    queryKey: ["projects", projectId, "issues", "filter-options"],
    queryFn: async () => {
      const res = await issueApi.filterOptions(projectId);
      return res.data;
    },
    enabled: !!projectId,
  });
}

export function useIssue(issueId: string) {
  return useQuery({
    queryKey: ["issues", issueId],
    queryFn: async () => {
      const res = await issueApi.get(issueId);
      return res.data;
    },
    enabled: !!issueId,
  });
}

export function useIssueComments(issueId: string, page = 1, pageSize = 20) {
  return useQuery({
    queryKey: ["issues", issueId, "comments", page, pageSize],
    queryFn: async () => {
      const res = await issueApi.comments(issueId, page, pageSize);
      return res.data;
    },
    enabled: !!issueId,
  });
}

export function useInfiniteIssueComments(issueId: string, pageSize = 20) {
  return useInfiniteQuery({
    queryKey: ["issues", issueId, "comments", "infinite", pageSize],
    initialPageParam: 1,
    queryFn: async ({ pageParam }) => {
      const res = await issueApi.comments(issueId, pageParam, pageSize);
      return res.data;
    },
    getNextPageParam: (lastPage) => {
      const totalPages = Math.max(
        Math.ceil(lastPage.total / lastPage.page_size),
        1,
      );
      return lastPage.page < totalPages ? lastPage.page + 1 : undefined;
    },
    enabled: !!issueId,
  });
}

export function useIssueTimeline(issueId: string, page = 1, pageSize = 20) {
  return useQuery({
    queryKey: ["issues", issueId, "timeline", page, pageSize],
    queryFn: async () => {
      const res = await issueApi.timeline(issueId, page, pageSize);
      return res.data;
    },
    enabled: !!issueId,
  });
}

export function useInfiniteIssueTimeline(issueId: string, pageSize = 20) {
  return useInfiniteQuery({
    queryKey: ["issues", issueId, "timeline", "infinite", pageSize],
    initialPageParam: 1,
    queryFn: async ({ pageParam }) => {
      const res = await issueApi.timeline(issueId, pageParam, pageSize);
      return res.data;
    },
    getNextPageParam: (lastPage) => {
      const totalPages = Math.max(
        Math.ceil(lastPage.total / lastPage.page_size),
        1,
      );
      return lastPage.page < totalPages ? lastPage.page + 1 : undefined;
    },
    enabled: !!issueId,
  });
}

export function useSyncProjectIssues(projectId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => issueApi.sync(projectId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["projects", projectId, "issues"] });
      queryClient.invalidateQueries({
        queryKey: ["projects", projectId, "issues", "filter-options"],
      });
      queryClient.invalidateQueries({ queryKey: ["projects"] });
      queryClient.invalidateQueries({ queryKey: ["projects", projectId] });
      queryClient.invalidateQueries({ queryKey: ["issues"] });
    },
  });
}
