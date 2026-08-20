import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { logApi } from "@/lib/api/logs";

interface UseLogRunsFilters {
  run_id?: string;
  source?: string;
  from?: string;
  to?: string;
  page?: number;
  page_size?: number;
}

interface UseInfiniteLogEntriesFilters {
  run_id: string;
  level?: string;
  q?: string;
  page_size?: number;
}

export function useLogRuns(
  projectId: string,
  filters: UseLogRunsFilters = {},
) {
  return useQuery({
    queryKey: [
      "logs",
      "runs",
      projectId,
      filters.run_id ?? "",
      filters.source ?? "",
      filters.from ?? "",
      filters.to ?? "",
      filters.page ?? 1,
      filters.page_size ?? 50,
    ],
    queryFn: async () => {
      const res = await logApi.listRuns(projectId, filters);
      return res.data;
    },
    enabled: !!projectId,
  });
}

export function useLogRun(projectId: string, runId: string) {
  return useQuery({
    queryKey: ["logs", "run", projectId, runId],
    queryFn: async () => {
      const res = await logApi.getRun(projectId, runId);
      return res.data;
    },
    enabled: !!projectId && !!runId,
    retry: false,
  });
}

export function useInfiniteLogEntries(
  projectId: string,
  filters: UseInfiniteLogEntriesFilters,
) {
  const pageSize = filters.page_size ?? 50;
  return useInfiniteQuery({
    queryKey: [
      "logs",
      "entries",
      "infinite",
      projectId,
      filters.run_id,
      filters.level ?? "",
      filters.q ?? "",
      pageSize,
    ],
    initialPageParam: 1,
    queryFn: async ({ pageParam }) => {
      const res = await logApi.listEntries(projectId, {
        run_id: filters.run_id,
        level: filters.level,
        q: filters.q,
        page: pageParam,
        page_size: pageSize,
        sort: "timestamp_asc",
      });
      return res.data;
    },
    getNextPageParam: (lastPage) => {
      const totalPages = Math.max(
        Math.ceil(lastPage.total / lastPage.page_size),
        1,
      );
      return lastPage.page < totalPages ? lastPage.page + 1 : undefined;
    },
    enabled: !!projectId && !!filters.run_id,
  });
}

export function useDeleteLogRun(projectId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (runId: string) => logApi.deleteRun(projectId, runId),
    onSuccess: (_data, runId) => {
      void queryClient.invalidateQueries({
        queryKey: ["logs", "runs", projectId],
      });
      void queryClient.invalidateQueries({
        queryKey: ["logs", "run", projectId, runId],
      });
      void queryClient.invalidateQueries({ queryKey: ["logs", "entries"] });
    },
  });
}

export function useClearProjectLogs(projectId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => logApi.deleteByProject(projectId),
    onSuccess: () => {
      void queryClient.invalidateQueries({
        queryKey: ["logs", "runs", projectId],
      });
      void queryClient.invalidateQueries({ queryKey: ["logs", "run"] });
      void queryClient.invalidateQueries({ queryKey: ["logs", "entries"] });
    },
  });
}
