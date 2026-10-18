import { api, decodeApiError } from "./client";

// 集成（INT-119）：GitHub App 是用户级配置，每个登录用户各有一份，
// 接口按当前 JWT 用户读写。PUT 省略或传 null 表示保留已存值；
// webhook_secret 空串（或全空白）表示清空。保存与测试都会在服务端真实访问 GitHub。

export interface UpdateGitHubAppIntegrationInput {
  app_id: string;
  private_key_pem?: string | null;
  webhook_secret?: string | null;
}

/** 保存/测试会同步打 GitHub 接口（服务端默认 20s 超时），ky 默认 10s 不够；retry:0 防重复打。 */
export const GITHUB_APP_LIVE_REQUEST_TIMEOUT_MS = 30_000;

const githubAppPath = "integrations/github-app";

export const integrationApi = {
  getGitHubApp: () =>
    decodeApiError(
      api.get(githubAppPath).json<ApiResponse<GitHubAppIntegration>>(),
    ),

  updateGitHubApp: (input: UpdateGitHubAppIntegrationInput) =>
    decodeApiError(
      api
        .put(githubAppPath, {
          json: input,
          timeout: GITHUB_APP_LIVE_REQUEST_TIMEOUT_MS,
          retry: 0,
        })
        .json<ApiResponse<GitHubAppIntegration>>(),
    ),

  testGitHubApp: () =>
    decodeApiError(
      api
        .post(`${githubAppPath}/test`, {
          timeout: GITHUB_APP_LIVE_REQUEST_TIMEOUT_MS,
          retry: 0,
        })
        .json<ApiResponse<GitHubAppIntegration>>(),
    ),
};
