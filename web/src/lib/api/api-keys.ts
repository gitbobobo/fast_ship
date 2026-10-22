import type { components } from "@/types/api.gen";
import { api } from "./client";

/** 契约类型直接引用 api.gen.ts。 */
type Schemas = components["schemas"];

type CreateApiKeyRequest = Schemas["CreateApiKeyRequest"];

/** 契约名为 ApiKeyCreated；明文 key 仅创建响应出现一次，契约已标 required。 */
type CreateApiKeyResponse = Schemas["ApiKeyCreated"];

export const apiKeyApi = {
  list: () => api.get("api-keys").json<ApiResponse<ApiKey[]>>(),

  create: (data: CreateApiKeyRequest) =>
    api
      .post("api-keys", { json: data })
      .json<ApiResponse<CreateApiKeyResponse>>(),

  delete: (id: string) =>
    api.delete(`api-keys/${id}`).json<ApiResponse<null>>(),
};
