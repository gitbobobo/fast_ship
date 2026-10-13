import { api, decodeApiError } from "./client";

export type EnvironmentStatus = "ok" | "unreachable" | "repair" | "degraded";

/** 写操作只看 access：operate 才能发消息、新建线程、应答请求。 */
export type EnvironmentAccess = "read-only" | "operate";

export interface Environment {
  id: string;
  name: string;
  address: string;
  status: EnvironmentStatus;
  /** 配对时服务端授予的完整 T3 scope 列表，只读展示。 */
  scopes: string[];
  access: EnvironmentAccess;
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
  | "error"
  | "approval_request"
  | "user_input_request"
  | "secret_request";

export interface ChatAttachment {
  type: "image";
  id: string;
  name: string;
  mime_type: string;
  size_bytes: number;
}

export interface ApprovalOption {
  decision: string;
  label: string;
  warning?: string;
}

export interface UserInputQuestion {
  id: string;
  header: string;
  question: string;
  options: { label: string; description: string; value?: string }[];
  multi_select?: boolean;
  allow_custom_answer?: boolean;
  initial_answer?: string;
  required?: boolean;
}

export interface ThreadItem {
  id: string;
  type: ThreadItemType;
  text: string;
  summary: string;
  status?: string;
  /** fork 继承前缀行：只展示，不能在本线程作答。 */
  inherited?: boolean;
  source_thread_id?: string;
  attachments?: ChatAttachment[];
  request_id?: string;
  request_kind?: string;
  prompt?: string;
  app_name?: string;
  options?: ApprovalOption[];
  decision?: string;
  questions?: UserInputQuestion[];
  answers?: Record<string, unknown>;
  response_mode?: string;
  response_capability?: string;
  request_status?: string;
  label?: string;
  reason?: string;
  placeholder?: string;
  secret_status?: string;
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

export interface ThreadQueueItem {
  run_id: string;
  message_id: string;
  text: string;
  attachments?: ChatAttachment[];
}

export interface ThreadQueue {
  held: boolean;
  can_reorder: boolean;
  can_steer: boolean;
  items: ThreadQueueItem[];
}

export interface ThreadActivity {
  active_run_id?: string;
  active_run_status?: string;
  stopping: boolean;
  has_pending_requests: boolean;
  pending_background_task_count: number;
}

export interface ThreadDetail {
  thread: ThreadSummary;
  items: ThreadItem[];
  queue: ThreadQueue;
  activity: ThreadActivity;
  /** 该环境会话能力，与环境列表 access 同口径；详情已带，不必等全量探测。 */
  access: EnvironmentAccess;
}

export interface ModelOption {
  id: string;
  value: string | boolean;
}

export interface ModelSelection {
  instance_id: string;
  model: string;
  /** canonical 数组 [{id,value}]，不是对象。 */
  options?: ModelOption[];
}

export interface ComposerProject {
  id: string;
  title: string;
  workspace_root?: string;
  default_model_selection?: ModelSelection;
}

/** config/settings 为 T3 原样透传（camelCase），由 lib/threads-compose 按需解析。 */
export interface ComposerOptions {
  projects: ComposerProject[];
  config: Record<string, unknown>;
  settings: Record<string, unknown>;
}

export interface WorkspaceRef {
  name: string;
  kind: string;
  is_default?: boolean;
  worktree_path?: string;
}

export interface WorkspaceOptions {
  project_id: string;
  cwd: string;
  is_repo?: boolean;
  ref_name?: string;
  has_working_tree_changes?: boolean;
  has_primary_remote?: boolean;
  refs: WorkspaceRef[];
  next_cursor?: number | null;
  total_count?: number;
}

export type ThreadWorkspace =
  | { type: "root" }
  | {
      type: "worktree";
      base_ref: string;
      branch?: string;
      start_from_origin?: boolean;
    }
  | { type: "existing_worktree"; worktree_path: string; branch?: string };

export interface CreateThreadInput {
  command_id: string;
  thread_id: string;
  project_id: string;
  message: { message_id: string; text: string; attachments?: ChatAttachment[] };
  model_selection?: ModelSelection;
  runtime_mode: "full-access";
  workspace: ThreadWorkspace;
}

export interface SendMessageInput {
  command_id: string;
  message_id: string;
  text: string;
  attachments?: ChatAttachment[];
}

export type RespondInput =
  | { type: "approval"; decision: string }
  | {
      type: "user_input";
      answers: Record<string, string | string[]>;
      attachments_by_question_id?: Record<string, ChatAttachment[]>;
    };

export type SecretAnswerInput =
  | { type: "save"; secret: string }
  | { type: "decline" };

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

const envPath = (environmentId: string) =>
  `environments/${encodeURIComponent(environmentId)}`;

const queuePath = (environmentId: string, threadId: string, runId: string) =>
  `${threadPath(environmentId, threadId)}/queue/${encodeURIComponent(runId)}`;

/** 写接口：同样 35 秒超时、不重试，错误在边界解码成 ApiRequestError。 */
function write<T>(promise: Promise<ApiResponse<T>>) {
  return decodeApiError(promise).then((res) => res.data);
}

/** X-File-Name 只能是 Latin-1；含其他字符的文件名交给服务端用默认名。 */
function fileNameHeader(name: string): Record<string, string> {
  return /^[\x20-\xff]+$/.test(name) ? { "X-File-Name": name } : {};
}

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

  composerOptions: (environmentId: string, signal?: AbortSignal) =>
    decodeApiError(
      api
        .get(`${envPath(environmentId)}/composer-options`, options(signal))
        .json<ApiResponse<ComposerOptions>>(),
    ),

  workspaceOptions: async (
    environmentId: string,
    projectId: string,
    query: string,
    signal?: AbortSignal,
  ) => {
    const collected: WorkspaceRef[] = [];
    let cursor: number | undefined;
    let last: ApiResponse<WorkspaceOptions> | undefined;
    // 服务端每次最多跟 8 页；这里再跟有限次。触顶时保留 last.next_cursor，不谎称已经读完。
    for (let page = 0; page < 32; page += 1) {
      const searchParams: Record<string, string | number> = {};
      if (query) searchParams.query = query;
      if (cursor != null) searchParams.cursor = cursor;
      const response = await decodeApiError(
        api
          .get(
            `${envPath(environmentId)}/projects/${encodeURIComponent(projectId)}/workspace-options`,
            {
              ...options(signal),
              searchParams:
                Object.keys(searchParams).length > 0 ? searchParams : undefined,
            },
          )
          .json<ApiResponse<WorkspaceOptions>>(),
      );
      last = response;
      if (!response.data) break;
      collected.push(...(response.data.refs ?? []));
      if (response.data.next_cursor == null) break;
      cursor = response.data.next_cursor;
    }
    if (!last?.data) return last as ApiResponse<WorkspaceOptions>;
    return {
      ...last,
      data: {
        ...last.data,
        refs: collected,
        next_cursor: last.data.next_cursor ?? null,
      },
    };
  },

  create: (environmentId: string, input: CreateThreadInput) =>
    write(
      api
        .post(`${envPath(environmentId)}/threads`, {
          json: input,
          ...options(),
        })
        .json<ApiResponse<{ thread_id: string; resumed: boolean }>>(),
    ),

  send: (environmentId: string, threadId: string, input: SendMessageInput) =>
    write(
      api
        .post(`${threadPath(environmentId, threadId)}/messages`, {
          json: input,
          ...options(),
        })
        .json<ApiResponse<{ sequence: number }>>(),
    ),

  /** holdQueue 由服务端固定为 true；停止是否生效以随后刷新的 activity 为准。 */
  stop: (environmentId: string, threadId: string) =>
    write(
      api
        .post(`${threadPath(environmentId, threadId)}/stop`, options())
        .json<ApiResponse<{ stopped: boolean; run_id?: string }>>(),
    ),

  resumeQueue: (environmentId: string, threadId: string) =>
    write(
      api
        .post(`${threadPath(environmentId, threadId)}/queue/resume`, options())
        .json<ApiResponse<{ sequence: number }>>(),
    ),

  steer: (environmentId: string, threadId: string, runId: string) =>
    write(
      api
        .post(`${queuePath(environmentId, threadId, runId)}/steer`, {
          json: {},
          ...options(),
        })
        .json<ApiResponse<{ sequence: number }>>(),
    ),

  cancelQueued: (environmentId: string, threadId: string, runId: string) =>
    write(
      api
        .post(`${queuePath(environmentId, threadId, runId)}/cancel`, options())
        .json<ApiResponse<{ sequence: number }>>(),
    ),

  /** attachments 缺省（undefined）= 保留原附件；[] = 清空；非空 = 整组替换。 */
  editQueued: (
    environmentId: string,
    threadId: string,
    runId: string,
    input: { text: string; attachments?: ChatAttachment[] },
  ) =>
    write(
      api
        .put(queuePath(environmentId, threadId, runId), {
          json: input,
          ...options(),
        })
        .json<ApiResponse<{ sequence: number }>>(),
    ),

  /** beforeRunId 为 null 表示移到队尾。 */
  reorderQueued: (
    environmentId: string,
    threadId: string,
    runId: string,
    beforeRunId: string | null,
  ) =>
    write(
      api
        .post(`${queuePath(environmentId, threadId, runId)}/reorder`, {
          json: { before_run_id: beforeRunId },
          ...options(),
        })
        .json<ApiResponse<{ sequence: number }>>(),
    ),

  respond: (
    environmentId: string,
    threadId: string,
    requestId: string,
    input: RespondInput,
  ) =>
    write(
      api
        .post(
          `${threadPath(environmentId, threadId)}/requests/${encodeURIComponent(requestId)}/respond`,
          { json: input, ...options() },
        )
        .json<ApiResponse<{ sequence: number }>>(),
    ),

  /** 调用方负责不把 secret 写进日志/提示；这里只做请求本身。 */
  answerSecret: (
    environmentId: string,
    threadId: string,
    itemId: string,
    input: SecretAnswerInput,
  ) =>
    write(
      api
        .post(
          `${threadPath(environmentId, threadId)}/items/${encodeURIComponent(itemId)}/secret-answer`,
          { json: input, ...options() },
        )
        .json<ApiResponse<null>>(),
    ),

  /** 原始字节 + Content-Type，不是 multipart；浏览器按 Blob 自动带 Content-Length。 */
  uploadAttachment: (environmentId: string, file: File) =>
    write(
      api
        .post(`${envPath(environmentId)}/attachments`, {
          body: file,
          headers: { "Content-Type": file.type, ...fileNameHeader(file.name) },
          ...options(),
        })
        .json<ApiResponse<ChatAttachment>>(),
    ),

  deleteAttachment: (environmentId: string, attachmentId: string) =>
    write(
      api
        .delete(
          `${envPath(environmentId)}/attachments/${encodeURIComponent(attachmentId)}`,
          options(),
        )
        .json<ApiResponse<null>>(),
    ),

  /** 经 JWT 代理读取附件字节，浏览器只拿到 blob，不接触 T3 签名地址。 */
  attachmentContent: (
    environmentId: string,
    attachmentId: string,
    signal?: AbortSignal,
  ) =>
    decodeApiError(
      api
        .get(
          `${envPath(environmentId)}/attachments/${encodeURIComponent(attachmentId)}/content`,
          options(signal),
        )
        .blob(),
    ),
};
