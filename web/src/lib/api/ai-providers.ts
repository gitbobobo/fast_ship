import type { components } from "@/types/api.gen";
import { api, decodeApiError } from "./client";

// 统一 AI 提供商（INT-133）：类型引用 api.gen.ts（契约第 4 节）。
// 密钥任何响应都不回传，仅返回末四位；catalog 来自 models.dev 内置子集，
// 由服务端随二进制发布，前端只读不写。

/** 自定义提供商的 catalog_id 哨兵值，与契约一致（自定义不出现在内置目录里）。 */
export const AI_PROVIDER_CUSTOM_ID = "custom";

type Schemas = components["schemas"];

export type AIProvider = Schemas["AIProvider"];

/** 契约名为 CatalogProviderModel（窗口/输出缺失为 0，不是缺省）。 */
export type CatalogModel = Schemas["CatalogProviderModel"];

export type CatalogProvider = Schemas["CatalogProvider"];

/** 测试结果 checks 数组的元素（契约内联在 AIProviderTestResult.checks 里，无独立 schema）。 */
export interface AIProviderTestCheck {
  name: string;
  passed: boolean;
  /** 失败原因（截断；不含密钥），通过时服务端省略。 */
  detail?: string;
}

export type AIProviderTestResult = Schemas["AIProviderTestResult"];

/** 契约名为 CreateAIProviderRequest；catalog_id === "custom" 时才接受 base_url。 */
export type CreateAIProviderInput = Schemas["CreateAIProviderRequest"];

/** 契约名为 UpdateAIProviderRequest（指针语义：出现才更新，api_key 省略保留已存值）。 */
export type UpdateAIProviderInput = Schemas["UpdateAIProviderRequest"];

/** 契约名为 TestAIProviderRequest；不落库的连通性测试，custom 必须给 base_url 与 model。 */
export type TestAIProviderInput = Schemas["TestAIProviderRequest"];

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
