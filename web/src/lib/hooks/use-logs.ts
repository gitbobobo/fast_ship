import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { logApi } from "@/lib/api/logs";

interface UseLogBatchesFilters {
  run_id?: string;
  batch_source?: string;
  from?: string;
  to?: string;
  page?: number;
  page_size?: number;
}

interface UseInfiniteLogEntriesFilters {
  batch_id: string;
  level?: string;
  q?: string;
  page_size?: number;
}

export function useLogBatches(
  projectId: string,
  filters: UseLogBatchesFilters = {},
) {
  return useQuery({
    queryKey: [
      "logs",
      "batches",
      projectId,
      filters.run_id ?? "",
      filters.batch_source ?? "",
      filters.from ?? "",
      filters.to ?? "",
      filters.page ?? 1,
      filters.page_size ?? 50,
    ],
    queryFn: async () => {
      const res = await logApi.listBatches(projectId, filters);
      return res.data;
    },
    enabled: !!projectId,
  });
}

export function useLogBatch(batchId: string) {
  return useQuery({
    queryKey: ["logs", "batch", batchId],
    queryFn: async () => {
      const res = await logApi.getBatch(batchId);
      return res.data;
    },
    enabled: !!batchId,
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
      filters.batch_id,
      filters.level ?? "",
      filters.q ?? "",
      pageSize,
    ],
    initialPageParam: 1,
    queryFn: async ({ pageParam }) => {
      const res = await logApi.listEntries(projectId, {
        batch_id: filters.batch_id,
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
    enabled: !!projectId && !!filters.batch_id,
  });
}

export function useDeleteLogBatch(projectId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (batchId: string) => logApi.deleteBatch(batchId),
    onSuccess: (_data, batchId) => {
      void queryClient.invalidateQueries({
        queryKey: ["logs", "batches", projectId],
      });
      void queryClient.invalidateQueries({
        queryKey: ["logs", "batch", batchId],
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
        queryKey: ["logs", "batches", projectId],
      });
      void queryClient.invalidateQueries({ queryKey: ["logs", "batch"] });
      void queryClient.invalidateQueries({ queryKey: ["logs", "entries"] });
    },
  });
}
