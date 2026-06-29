import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { logApi } from "@/lib/api/logs";

interface UseLogsFilters {
  batch_id?: string;
  run_id?: string;
  level?: string;
  entry_source?: string;
  batch_source?: string;
  q?: string;
  from?: string;
  to?: string;
  page?: number;
  page_size?: number;
  sort?: string;
}

export function useLogs(projectId: string, filters: UseLogsFilters = {}) {
  return useQuery({
    queryKey: [
      "logs",
      "entries",
      projectId,
      filters.batch_id ?? "",
      filters.run_id ?? "",
      filters.level ?? "",
      filters.entry_source ?? "",
      filters.batch_source ?? "",
      filters.q ?? "",
      filters.from ?? "",
      filters.to ?? "",
      filters.page ?? 1,
      filters.page_size ?? 50,
      filters.sort ?? "timestamp_desc",
    ],
    queryFn: async () => {
      const res = await logApi.listEntries(projectId, filters);
      return res.data;
    },
    enabled: !!projectId,
  });
}

export function useDeleteLogBatch(projectId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (batchId: string) => logApi.deleteBatch(batchId),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["logs", "entries", projectId] });
    },
  });
}

export function useClearProjectLogs(projectId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => logApi.deleteByProject(projectId),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["logs", "entries", projectId] });
    },
  });
}
