import { api, decodeApiError } from "./client";

// 统一 AI 提供商（INT-133）：类型与端点按契约第 4 节手写。
// 密钥任何响应都不回传，仅返回末四位；catalog 来自 models.dev 内置子集，
// 由服务端随二进制发布，前端只读不写。

export interface AIProvider {
  id: string;
  /** models.dev 提供商 id；自定义固定为 "custom"。 */
  catalog_id: string;
  name: string;
  base_url: string;
  api_key_last4: string;
  /** 引用该提供商的机器人数；删除时服务端据此返回 409。 */
  bot_count: number;
  created_at: string;
  updated_at: string;
}

export interface CatalogModel {
  id: string;
  name: string;
  context_window?: number;
  max_output_tokens?: number;
  reasoning?: boolean;
}

export interface CatalogProvider {
  id: string;
  name: string;
  /** 目录给出的 OpenAI 兼容 API 地址；目录提供商的 base_url 以此为准。 */
  api: string;
  models: CatalogModel[];
}

export interface AIProviderTestCheck {
  name: string;
  passed: boolean;
  /** 失败原因（截断；不含密钥），通过时服务端省略。 */
  detail?: string;
}

export interface AIProviderTestResult {
  ok: boolean;
  /** models 列表可达 + 一次最小 chat 调用等逐项检查。 */
  checks: AIProviderTestCheck[];
  /** 测试过程中拿到的模型清单（服务端可选返回）。 */
  models?: string[];
}

/** catalog_id === "custom" 时才接受 base_url；name 缺省服务端用目录名/「自定义」。 */
export interface CreateAIProviderInput {
  catalog_id: string;
  name?: string;
  base_url?: string;
  api_key: string;
}

/** 指针语义：出现才更新；catalog_id 不可改，api_key 省略保留已存值。 */
export interface UpdateAIProviderInput {
  name?: string;
  base_url?: string;
  api_key?: string;
}

/**
 * 不落库的连通性测试：catalog 提供商只传 catalog_id + api_key（model 缺省
 * 用目录第一个模型）；custom 必须给 base_url 与 model。
 */
export interface TestAIProviderInput {
  catalog_id?: string;
  base_url?: string;
  api_key?: string;
  model?: string;
}

/** 与 bot 请求同一口径：35 秒超时、不重试（测试连接本身是一次模型调用）。 */
export const AI_PROVIDER_REQUEST_TIMEOUT_MS = 35_000;

function options(signal?: AbortSignal) {
  return { signal, timeout: AI_PROVIDER_REQUEST_TIMEOUT_MS, retry: 0 } as const;
}

/** 写接口：错误在边界解码成 ApiRequestError。 */
function write<T>(promise: Promise<ApiResponse<T>>) {
  return decodeApiError(promise).then((res) => res.data);
}

const providerPath = (providerId: string) =>
  `ai-providers/${encodeURIComponent(providerId)}`;

export const aiProviderApi = {
  list: (signal?: AbortSignal) =>
    decodeApiError(
      api
        .get("ai-providers", options(signal))
        .json<ApiResponse<{ items: AIProvider[] }>>(),
    ),

  create: (input: CreateAIProviderInput) =>
    write(
      api
        .post("ai-providers", { json: input, ...options() })
        .json<ApiResponse<AIProvider>>(),
    ),

  update: (providerId: string, input: UpdateAIProviderInput) =>
    write(
      api
        .put(providerPath(providerId), { json: input, ...options() })
        .json<ApiResponse<AIProvider>>(),
    ),

  /** 被机器人引用时服务端返回 409，调用方按 ApiRequestError.status 处理。 */
  delete: (providerId: string) =>
    write(api.delete(providerPath(providerId), options()).json<ApiResponse<null>>()),

  test: (input: TestAIProviderInput) =>
    write(
      api
        .post("ai-providers/test", { json: input, ...options() })
        .json<ApiResponse<AIProviderTestResult>>(),
    ),

  catalog: (signal?: AbortSignal) =>
    decodeApiError(
      api
        .get("ai-providers/catalog", options(signal))
        .json<ApiResponse<{ items: CatalogProvider[] }>>(),
    ),
};
