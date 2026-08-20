import { api } from "./client";

interface LogEntryListParams {
  run_id?: string;
  level?: string;
  entry_source?: string;
  q?: string;
  from?: string;
  to?: string;
  page?: number;
  page_size?: number;
  sort?: string;
}

interface LogRunListParams {
  run_id?: string;
  source?: string;
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
          ...(params.run_id ? { run_id: params.run_id } : {}),
          ...(params.level ? { level: params.level } : {}),
          ...(params.entry_source ? { entry_source: params.entry_source } : {}),
          ...(params.q ? { q: params.q } : {}),
          ...(params.from ? { from: params.from } : {}),
          ...(params.to ? { to: params.to } : {}),
        },
      })
      .json<ApiResponse<PaginatedData<LogEntry>>>(),

  listRuns: (projectId: string, params: LogRunListParams = {}) =>
    api
      .get(`projects/${projectId}/log-runs`, {
        searchParams: {
          page: params.page ?? 1,
          page_size: params.page_size ?? 50,
          ...(params.run_id ? { run_id: params.run_id } : {}),
          ...(params.source ? { source: params.source } : {}),
          ...(params.from ? { from: params.from } : {}),
          ...(params.to ? { to: params.to } : {}),
        },
      })
      .json<ApiResponse<PaginatedData<LogRun>>>(),

  getRun: (projectId: string, runId: string) =>
    api
      .get(`projects/${projectId}/log-runs/${runId}`)
      .json<ApiResponse<LogRun>>(),

  deleteRun: (projectId: string, runId: string) =>
    api
      .delete(`projects/${projectId}/log-runs/${runId}`)
      .json<ApiResponse<null>>(),

  deleteByProject: (projectId: string) =>
    api.delete(`projects/${projectId}/logs`).json<ApiResponse<null>>(),
};
