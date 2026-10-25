import { api, decodeApiError } from "./client";

// 部署级代码仓集成（INT-166）：首条恒为内置 github-default（只读映射部署
// GitHub App），后接 forgejo 集成行。全部端点仅管理员（列表也挂 RequireAdmin，
// member 请求返回 40305）；token 等凭证只写不回显。
// test 端点会在服务端同步访问 Forgejo 实例，ky 默认 10s 超时不够；retry:0 防重复打。

/** 连接检测在服务端同步访问 Forgejo 实例（服务端默认 10s 超时）。 */
export const FORGE_LIVE_REQUEST_TIMEOUT_MS = 30_000;

const basePath = "deployment/integrations";

export const forgeIntegrationsApi = {
  list: () =>
    decodeApiError(api.get(basePath).json<ApiResponse<ForgeIntegration[]>>()),

  create: (input: CreateForgeIntegrationRequest) =>
    decodeApiError(
      api.post(basePath, { json: input }).json<ApiResponse<ForgeIntegration>>(),
    ),

  // 字段三态语义（保留/重置/清空）见契约 @description，由调用方组 payload。
  update: (id: string, input: UpdateForgeIntegrationRequest) =>
    decodeApiError(
      api
        .put(`${basePath}/${id}`, { json: input })
        .json<ApiResponse<ForgeIntegration>>(),
    ),

  // data 恒为 null；仍被项目引用时 40935（message 含项目名），由调用方展示。
  remove: (id: string) =>
    decodeApiError(api.delete(`${basePath}/${id}`).json<ApiResponse<null>>()),

  test: (id: string) =>
    decodeApiError(
      api
        .post(`${basePath}/${id}/test`, {
          timeout: FORGE_LIVE_REQUEST_TIMEOUT_MS,
          retry: 0,
        })
        .json<ApiResponse<ForgeIntegrationTest>>(),
    ),
};
