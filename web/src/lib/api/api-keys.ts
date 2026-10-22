import type { components } from "@/types/api.gen";
import { api } from "./client";

/** 契约类型直接引用 api.gen.ts。 */
type Schemas = components["schemas"];

type CreateApiKeyRequest = Schemas["CreateApiKeyRequest"];

/**
 * 契约 ApiKeyCreated 把 key 标成可选，但服务端创建响应恒携带明文 key
 * （仅此一次下发），这里收窄为必有。
 */
type CreateApiKeyResponse = Schemas["ApiKeyCreated"] & { key: string };

export const apiKeyApi = {
  list: () => api.get("api-keys").json<ApiResponse<ApiKey[]>>(),

  create: (data: CreateApiKeyRequest) =>
    api
      .post("api-keys", { json: data })
      .json<ApiResponse<CreateApiKeyResponse>>(),

  delete: (id: string) =>
    api.delete(`api-keys/${id}`).json<ApiResponse<null>>(),
};
