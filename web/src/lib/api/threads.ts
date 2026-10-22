import type { components } from "@/types/api.gen";
import { api, decodeApiError } from "./client";

/**
 * 线程域契约类型：直接引用 api.gen.ts（server/api/openapi.yaml 生成），
 * 字段含义看生成类型自带的 @description；与前端预期不一致时以契约为准。
 * 本文件其余类型是前端自有类型（query 过滤、聚合信封等契约没有的形状）。
 */
type Schemas = components["schemas"];

export type EnvironmentStatus = Schemas["T3EnvironmentStatus"];

/** 写操作只看 access：operate 才能发消息、新建线程、应答请求。 */
export type EnvironmentAccess = Schemas["T3EnvironmentAccess"];

export type Environment = Schemas["T3Environment"];

export type ThreadStatus = Schemas["T3ThreadSummary"]["status"];

export type ThreadSummary = Schemas["T3ThreadSummary"];

export type ThreadItemType = Schemas["T3ThreadItem"]["type"];

/** 契约名为 T3ChatAttachment。 */
export type ChatAttachment = Schemas["T3ChatAttachment"];

/** 契约名为 T3ApprovalOption。 */
export type ApprovalOption = Schemas["T3ApprovalOption"];

/** 契约名为 T3UserInputQuestion。 */
export type UserInputQuestion = Schemas["T3UserInputQuestion"];

/** 契约名为 T3ThreadItem。 */
export type ThreadItem = Schemas["T3ThreadItem"];

/** 聚合信封 {items, environments}，契约内联、无 schema。 */
export interface ThreadList {
  items: ThreadSummary[];
  environments: Environment[];
}

/** 契约名为 T3ThreadQueueItem。 */
export type ThreadQueueItem = Schemas["T3ThreadQueueItem"];

/** 契约名为 T3ThreadQueue。 */
export type ThreadQueue = Schemas["T3ThreadQueue"];

/** 契约名为 T3ThreadActivity。 */
export type ThreadActivity = Schemas["T3ThreadActivity"];

/** 契约名为 T3ThreadDetail。 */
export type ThreadDetail = Schemas["T3ThreadDetail"];

/**
 * 模型选项保留手写：契约 T3ModelOption.value 声明为 string，但描述自承
 * 「字符串或布尔（T3 ProviderOptionSelectionValue）」，运行时确有布尔值；
 * 生成类型表达不了，前端读取与回传都需要 string | boolean。
 */
export interface ModelOption {
  id: string;
  value: string | boolean;
}

/** canonical 数组 [{id,value}]，不是对象。 */
export interface ModelSelection {
  instance_id: string;
  model: string;
  options?: ModelOption[];
}

/** T3ComposerOptions.projects 的元素（契约内联、无独立 schema）。 */
export interface ComposerProject {
  id: string;
  title: string;
  workspace_root?: string;
  default_model_selection?: ModelSelection;
}

/** 契约名为 T3ComposerOptions；config/settings 为 T3 原样透传（camelCase）。 */
export type ComposerOptions = Schemas["T3ComposerOptions"];

/** 契约名为 T3WorkspaceRef。 */
export type WorkspaceRef = Schemas["T3WorkspaceRef"];

/** 契约名为 T3WorkspaceOptions。 */
export type WorkspaceOptions = Schemas["T3WorkspaceOptions"];

/**
 * 保留手写判别联合：契约 T3ThreadWorkspace 是扁平对象（type + 全可选字段），
 * 表达不了「existing_worktree 必须带 worktree_path」这类按 variant 的必填约束。
 */
export type ThreadWorkspace =
  | { type: "root" }
  | {
      type: "worktree";
      /** 缺省时由服务端取项目主检出的当前分支（启动弹框用）。 */
      base_ref?: string;
      branch?: string;
      start_from_origin?: boolean;
    }
  | { type: "existing_worktree"; worktree_path: string; branch?: string };

/**
 * 新建线程请求保留手写：模型选项经 ModelSelection 携带（布尔选项值契约
 * 表达不了，见上）；command_id/thread_id/workspace 前端恒提供、
 * runtime_mode 恒为 "full-access"，比契约更严。
 */
export interface CreateThreadInput {
  command_id: string;
  thread_id: string;
  project_id: string;
  message: { message_id: string; text: string; attachments?: ChatAttachment[] };
  model_selection?: ModelSelection;
  runtime_mode: "full-access";
  workspace: ThreadWorkspace;
}

/**
 * 契约类型收窄：契约 command_id/message_id 可选（缺省服务端生成），前端为
 * 「同一操作重试必须带同一 ID」的幂等语义恒提供，收窄为必填。
 */
export type SendMessageInput = Schemas["SendT3ThreadMessageRequest"] & {
  command_id: string;
  message_id: string;
};

/**
 * 保留手写判别联合：契约 RespondT3ThreadRequest 是扁平对象，表达不了
 * 「type=approval 必须带 decision；type=user_input 必须带 answers」的约束。
 */
export type RespondInput =
  | { type: "approval"; decision: string }
  | {
      type: "user_input";
      answers: Record<string, string | string[]>;
      attachments_by_question_id?: Record<string, ChatAttachment[]>;
    };

/**
 * 保留手写判别联合：契约 AnswerT3SecretRequest 是扁平对象，表达不了
 * 「type=save 必须带 secret」。调用方负责不把 secret 写进日志。
 */
export type SecretAnswerInput =
  | { type: "save"; secret: string }
  | { type: "decline" };

/** 契约名为 UpdateT3EnvironmentRequest。 */
export type EnvironmentInput = Schemas["UpdateT3EnvironmentRequest"];

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

  create: (data: Schemas["CreateT3EnvironmentRequest"]) =>
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
        .json<ApiResponse<Schemas["T3ThreadLaunchResult"]>>(),
    ),

  send: (environmentId: string, threadId: string, input: SendMessageInput) =>
    write(
      api
        .post(`${threadPath(environmentId, threadId)}/messages`, {
          json: input,
          ...options(),
        })
        .json<ApiResponse<Schemas["T3DispatchResult"]>>(),
    ),

  /** holdQueue 由服务端固定为 true；停止是否生效以随后刷新的 activity 为准。 */
  stop: (environmentId: string, threadId: string) =>
    write(
      api
        .post(`${threadPath(environmentId, threadId)}/stop`, options())
        .json<ApiResponse<Schemas["T3ThreadStopResult"]>>(),
    ),

  resumeQueue: (environmentId: string, threadId: string) =>
    write(
      api
        .post(`${threadPath(environmentId, threadId)}/queue/resume`, options())
        .json<ApiResponse<Schemas["T3DispatchResult"]>>(),
    ),

  steer: (environmentId: string, threadId: string, runId: string) =>
    write(
      api
        .post(`${queuePath(environmentId, threadId, runId)}/steer`, {
          json: {},
          ...options(),
        })
        .json<ApiResponse<Schemas["T3DispatchResult"]>>(),
    ),

  cancelQueued: (environmentId: string, threadId: string, runId: string) =>
    write(
      api
        .post(`${queuePath(environmentId, threadId, runId)}/cancel`, options())
        .json<ApiResponse<Schemas["T3DispatchResult"]>>(),
    ),

  /** attachments 缺省（undefined）= 保留原附件；[] = 清空；非空 = 整组替换。 */
  editQueued: (
    environmentId: string,
    threadId: string,
    runId: string,
    input: Schemas["EditT3QueuedRunRequest"],
  ) =>
    write(
      api
        .put(queuePath(environmentId, threadId, runId), {
          json: input,
          ...options(),
        })
        .json<ApiResponse<Schemas["T3DispatchResult"]>>(),
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
        .json<ApiResponse<Schemas["T3DispatchResult"]>>(),
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
        .json<ApiResponse<Schemas["T3DispatchResult"]>>(),
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
