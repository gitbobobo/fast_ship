import { api } from "./client";

interface LogEntryListParams {
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

interface LogBatchListParams {
  run_id?: string;
  batch_source?: string;
  from?: string;
  to?: string;
  page?: number;
  page_size?: number;
}

export const logApi = {
  listEntries: (projectId: string, params: LogEntryListParams = {}) =>
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

  listBatches: (projectId: string, params: LogBatchListParams = {}) =>
    api
      .get(`projects/${projectId}/log-batches`, {
        searchParams: {
          page: params.page ?? 1,
          page_size: params.page_size ?? 50,
          ...(params.run_id ? { run_id: params.run_id } : {}),
          ...(params.batch_source ? { batch_source: params.batch_source } : {}),
          ...(params.from ? { from: params.from } : {}),
          ...(params.to ? { to: params.to } : {}),
        },
      })
      .json<ApiResponse<PaginatedData<LogBatch>>>(),

  getBatch: (batchId: string) =>
    api.get(`log-batches/${batchId}`).json<ApiResponse<LogBatch>>(),

  deleteBatch: (batchId: string) =>
    api.delete(`log-batches/${batchId}`).json<ApiResponse<null>>(),

  deleteByProject: (projectId: string) =>
    api.delete(`projects/${projectId}/logs`).json<ApiResponse<null>>(),
};
