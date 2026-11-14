import type { components } from "@/types/api.gen";
import { api, decodeApiError } from "./client";

/** 契约类型直接引用 api.gen.ts；字段语义见 openapi.yaml 的 @description。 */
type Schemas = components["schemas"];

type OAuthAuthorizePreview = Schemas["OAuthAuthorizePreview"];
type OAuthAuthorizeRequest = Schemas["OAuthAuthorizeRequest"];
type OAuthAuthorizeResult = Schemas["OAuthAuthorizeResult"];
type OAuthAuthorization = Schemas["OAuthAuthorization"];

/** GET /oauth/authorize 的 query 参数（scope 不参与预取）。 */
export interface OAuthAuthorizeParams {
  client_id: string;
  redirect_uri: string;
  code_challenge: string;
  code_challenge_method: "S256";
}

export const oauthApi = {
  /** 授权预览：客户端名 + 当前用户项目清单。 */
  preview: (params: OAuthAuthorizeParams) =>
    api
      .get("oauth/authorize", { searchParams: { ...params } })
      .json<ApiResponse<OAuthAuthorizePreview>>(),

  /** 提交授权，返回服务端拼好的 redirect（含 code 与 state）。 */
  authorize: (data: OAuthAuthorizeRequest) =>
    decodeApiError(
      api
        .post("oauth/authorize", { json: data })
        .json<ApiResponse<OAuthAuthorizeResult>>(),
    ),

  list: () =>
    api.get("oauth/authorizations").json<ApiResponse<OAuthAuthorization[]>>(),

  revoke: (id: string) =>
    api.delete(`oauth/authorizations/${id}`).json<ApiResponse<null>>(),
};
