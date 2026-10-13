import { api, decodeApiError } from "./client";

export type EnvironmentStatus = "ok" | "unreachable" | "repair" | "degraded";

export interface Environment {
  id: string;
  name: string;
  address: string;
  status: EnvironmentStatus;
  error?: string;
  updated_at: string;
}

export type ThreadStatus = "running" | "waiting" | "idle" | "archived";

export interface ThreadSummary {
  id: string;
  environment_id: string;
  environment_name: string;
  project_id: string;
  project_name: string;
  title: string;
  status: ThreadStatus;
  branch: string;
  provider: string;
  model: string;
  updated_at: string;
  pull_request_count: number;
  /** 仅详情且为子代理线程时给出：父线程 ID，用于「返回父线程」导航。 */
  parent_thread_id?: string;
}

export type ThreadItemType =
  | "user_message"
  | "assistant_message"
  | "reasoning"
  | "command_execution"
  | "dynamic_tool"
  | "file_change"
  | "subagent"
  | "error";

export interface ThreadItem {
  id: string;
  type: ThreadItemType;
  text: string;
  summary: string;
  status?: string;
  child_thread_id?: string;
  /** 为 true 时正文需要展开后按需通过 itemDetail 读取完整内容。 */
  detail_available?: boolean;
  /** 上游条目自身的更新时间；缺失或为空时调用方退回线程更新时间作为版本。 */
  updated_at?: string;
}

export interface ThreadList {
  items: ThreadSummary[];
  environments: Environment[];
}

export interface ThreadDetail {
  thread: ThreadSummary;
  items: ThreadItem[];
}

export interface EnvironmentInput {
  name?: string;
  pairing_url?: string;
}

/** 每个请求独立 35 秒超时，不重试（重试由 react-query 负责）；signal 用于身份切换时取消。 */
export const THREAD_REQUEST_TIMEOUT_MS = 35_000;

function options(signal?: AbortSignal) {
  return { signal, timeout: THREAD_REQUEST_TIMEOUT_MS, retry: 0 } as const;
}

const threadPath = (environmentId: string, threadId: string) =>
  `environments/${encodeURIComponent(environmentId)}/threads/${encodeURIComponent(threadId)}`;

export const environmentApi = {
  list: (signal?: AbortSignal) =>
    api
      .get("environments", options(signal))
      .json<ApiResponse<{ items: Environment[] }>>(),

  create: (data: { name: string; pairing_url: string }) =>
    api
      .post("environments", { json: data, ...options() })
      .json<ApiResponse<Environment>>(),

  update: (id: string, data: EnvironmentInput) =>
    api
      .put(`environments/${encodeURIComponent(id)}`, {
        json: data,
        ...options(),
      })
      .json<ApiResponse<Environment>>(),

  delete: (id: string) =>
    api
      .delete(`environments/${encodeURIComponent(id)}`, options())
      .json<ApiResponse<null>>(),
};

export const threadApi = {
  list: (signal?: AbortSignal) =>
    decodeApiError(
      api.get("threads", options(signal)).json<ApiResponse<ThreadList>>(),
    ),

  detail: (environmentId: string, threadId: string, signal?: AbortSignal) =>
    decodeApiError(
      api
        .get(threadPath(environmentId, threadId), options(signal))
        .json<ApiResponse<ThreadDetail>>(),
    ),

  itemDetail: (
    environmentId: string,
    threadId: string,
    itemId: string,
    signal?: AbortSignal,
  ) =>
    decodeApiError(
      api
        .get(
          `${threadPath(environmentId, threadId)}/items/${encodeURIComponent(itemId)}`,
          options(signal),
        )
        .json<ApiResponse<ThreadItem>>(),
    ),
};
