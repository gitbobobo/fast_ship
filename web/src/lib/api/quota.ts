import { api, decodeApiError } from "./client";

// 套餐额度（INT-109）：端点与字段按冻结契约 v1 §5。
// credentials 是 { "<credential_fields[].key>": "<value>" } 对象；
// PUT 省略 credentials 表示不改凭据，给了就是整组替换。

export interface CreateQuotaAccountInput {
  provider: string;
  label: string;
  credentials: Record<string, string>;
}

export interface UpdateQuotaAccountInput {
  label?: string;
  credentials?: Record<string, string>;
}

export interface QuotaProbeInput {
  provider: string;
  credentials: Record<string, string>;
}

/** 探测与手动刷新会真实访问供应商接口（服务端默认 20s 超时），ky 默认 10s 不够。 */
export const QUOTA_LIVE_REQUEST_TIMEOUT_MS = 30_000;

const accountPath = (id: string) => `quota/accounts/${encodeURIComponent(id)}`;

export const quotaApi = {
  listProviders: () =>
    decodeApiError(
      api.get("quota/providers").json<ApiResponse<QuotaProvider[]>>(),
    ),

  listAccounts: () =>
    decodeApiError(
      api.get("quota/accounts").json<ApiResponse<QuotaAccount[]>>(),
    ),

  createAccount: (input: CreateQuotaAccountInput) =>
    decodeApiError(
      api
        .post("quota/accounts", {
          json: input,
          timeout: QUOTA_LIVE_REQUEST_TIMEOUT_MS,
          retry: 0,
        })
        .json<ApiResponse<QuotaAccount>>(),
    ),

  // 编辑若带 credentials 会在服务端同步抓一次；PUT 在 ky 默认重试方法里，
  // 不关掉会在 5xx/网络错误时重复打供应商接口。
  updateAccount: (id: string, input: UpdateQuotaAccountInput) =>
    decodeApiError(
      api
        .put(accountPath(id), {
          json: input,
          timeout: QUOTA_LIVE_REQUEST_TIMEOUT_MS,
          retry: 0,
        })
        .json<ApiResponse<QuotaAccount>>(),
    ),

  deleteAccount: (id: string) =>
    decodeApiError(
      api.delete(accountPath(id)).json<ApiResponse<null>>(),
    ),

  refreshAccount: (id: string) =>
    decodeApiError(
      api
        .post(`${accountPath(id)}/refresh`, {
          timeout: QUOTA_LIVE_REQUEST_TIMEOUT_MS,
          retry: 0,
        })
        .json<ApiResponse<QuotaAccount>>(),
    ),

  probe: (input: QuotaProbeInput) =>
    decodeApiError(
      api
        .post("quota/test", {
          json: input,
          timeout: QUOTA_LIVE_REQUEST_TIMEOUT_MS,
          retry: 0,
        })
        .json<ApiResponse<QuotaProbe>>(),
    ),
};
