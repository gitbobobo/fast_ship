import { api } from "./client";

interface CreateApiKeyRequest {
  name: string;
}

interface CreateApiKeyResponse extends ApiKey {
  key: string;
}

export const apiKeyApi = {
  list: () => api.get("api-keys").json<ApiResponse<ApiKey[]>>(),

  create: (data: CreateApiKeyRequest) =>
    api
      .post("api-keys", { json: data })
      .json<ApiResponse<CreateApiKeyResponse>>(),

  delete: (id: string) =>
    api.delete(`api-keys/${id}`).json<ApiResponse<null>>(),
};
