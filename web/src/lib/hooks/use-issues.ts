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
  source?: string;
  workflow_status?: string;
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
      filters.source ?? "",
      filters.workflow_status ?? "",
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

export function useIssueRepoLabels(projectId: string) {
  return useQuery({
    queryKey: ["projects", projectId, "issues", "repo-labels"],
    queryFn: async () => {
      const res = await issueApi.repoLabels(projectId);
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
      queryClient.invalidateQueries({
        queryKey: ["projects", projectId, "issues", "repo-labels"],
      });
      queryClient.invalidateQueries({ queryKey: ["projects"] });
      queryClient.invalidateQueries({ queryKey: ["projects", projectId] });
      queryClient.invalidateQueries({ queryKey: ["issues"] });
    },
  });
}

export function useCreateIssue(projectId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: Parameters<typeof issueApi.create>[1]) =>
      issueApi.create(projectId, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["projects", projectId, "issues"] });
      queryClient.invalidateQueries({
        queryKey: ["projects", projectId, "issues", "filter-options"],
      });
      queryClient.invalidateQueries({ queryKey: ["issues"] });
    },
  });
}

export function useUpdateIssue(issueId: string, projectId?: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: Parameters<typeof issueApi.update>[1]) =>
      issueApi.update(issueId, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["issues", issueId] });
      if (projectId) {
        queryClient.invalidateQueries({ queryKey: ["projects", projectId, "issues"] });
        queryClient.invalidateQueries({ queryKey: ["projects", projectId, "issues", "filter-options"] });
      }
    },
  });
}

export function useUploadIssueAsset(issueId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (formData: FormData) => issueApi.uploadAsset(issueId, formData),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["issues", issueId] });
    },
  });
}

export function useUploadDraftIssueAsset(projectId: string) {
  return useMutation({
    mutationFn: (formData: FormData) => issueApi.uploadDraftAsset(projectId, formData),
  });
}

export function useCreateIssueComment(issueId: string, projectId?: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: Parameters<typeof issueApi.createComment>[1]) =>
      issueApi.createComment(issueId, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["issues", issueId] });
      queryClient.invalidateQueries({ queryKey: ["issues", issueId, "comments"] });
      if (projectId) {
        queryClient.invalidateQueries({ queryKey: ["projects", projectId, "issues"] });
      }
    },
  });
}

export function useUpdateIssueInternalMeta(issueId: string, projectId?: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: Parameters<typeof issueApi.updateInternalMeta>[1]) =>
      issueApi.updateInternalMeta(issueId, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["issues", issueId] });
      if (projectId) {
        queryClient.invalidateQueries({ queryKey: ["projects", projectId, "issues"] });
      }
    },
  });
}

export function useReplaceIssueChecklist(issueId: string, projectId?: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: Parameters<typeof issueApi.replaceChecklist>[1]) =>
      issueApi.replaceChecklist(issueId, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["issues", issueId] });
      if (projectId) {
        queryClient.invalidateQueries({ queryKey: ["projects", projectId, "issues"] });
      }
    },
  });
}

export function useUpdateIssueWorkflowStatus() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({
      issueId,
      workflow_status,
    }: {
      issueId: string;
      projectId: string;
      workflow_status: "" | "todo" | "in_progress" | "done";
    }) => {
      return issueApi.updateInternalMeta(issueId, { workflow_status });
    },
    onSuccess: (_, variables) => {
      queryClient.invalidateQueries({
        queryKey: ["projects", variables.projectId, "issues"],
      });
      queryClient.invalidateQueries({
        queryKey: ["issues", variables.issueId],
      });
    },
  });
}

export function useCloseIssuesBatch(projectId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (issueIds: string[]) => {
      const results = await Promise.allSettled(
        issueIds.map((id) =>
          issueApi.update(id, { state: "closed", state_reason: "completed" }),
        ),
      );
      const succeeded = results.filter((r) => r.status === "fulfilled").length;
      const failed = results.length - succeeded;
      return { succeeded, failed, total: issueIds.length };
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["projects", projectId, "issues"] });
      queryClient.invalidateQueries({
        queryKey: ["projects", projectId, "issues", "filter-options"],
      });
      queryClient.invalidateQueries({ queryKey: ["issues"] });
    },
  });
}
