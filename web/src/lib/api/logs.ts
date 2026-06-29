import { api } from "./client";

interface LogListParams {
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

export const logApi = {
  listEntries: (projectId: string, params: LogListParams = {}) =>
    api
      .get(`projects/${projectId}/logs`, {
        searchParams: {
          page: params.page ?? 1,
          page_size: params.page_size ?? 50,
          sort: params.sort ?? "timestamp_desc",
          ...(params.batch_id ? { batch_id: params.batch_id } : {}),
          ...(params.run_id ? { run_id: params.run_id } : {}),
          ...(params.level ? { level: params.level } : {}),
          ...(params.entry_source ? { entry_source: params.entry_source } : {}),
          ...(params.batch_source ? { batch_source: params.batch_source } : {}),
          ...(params.q ? { q: params.q } : {}),
          ...(params.from ? { from: params.from } : {}),
          ...(params.to ? { to: params.to } : {}),
        },
      })
      .json<ApiResponse<PaginatedData<LogEntry>>>(),

  deleteBatch: (batchId: string) =>
    api.delete(`log-batches/${batchId}`).json<ApiResponse<null>>(),

  deleteByProject: (projectId: string) =>
    api.delete(`projects/${projectId}/logs`).json<ApiResponse<null>>(),
};
