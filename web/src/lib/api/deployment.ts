import { api, decodeApiError } from "./client";

// 部署设置与部署级 GitHub App（INT-129）：整部署一份。
// GET 所有登录用户可读；PUT/POST 写操作仅管理员。
// GitHub App 的保存/测试/manifest 完成都会在服务端真实访问 GitHub，
// ky 默认 10s 超时不够；retry:0 防重复打。

/** 保存/测试/manifest 完成会同步打 GitHub 接口（服务端默认 20s 超时）。 */
export const GITHUB_APP_LIVE_REQUEST_TIMEOUT_MS = 30_000;

/**
 * manifest complete 的前端预算要盖过后端总账：conversions 换凭证最长 30s，
 * 随后的同步连接检测再加最长 15s。沿用 30s 的话凭证可能已落库而浏览器先超时，
 * code/state 已被消费、无法重试，用户会误以为创建失败。
 */
const COMPLETE_MANIFEST_TIMEOUT_MS = 60_000;

const settingsPath = "deployment/settings";
const githubAppPath = "deployment/github-app";

export const deploymentApi = {
  getSettings: () =>
    decodeApiError(
      api.get(settingsPath).json<ApiResponse<DeploymentSettings>>(),
    ),

  updateSettings: (input: UpdateDeploymentSettingsRequest) =>
    decodeApiError(
      api
        .put(settingsPath, { json: input })
        .json<ApiResponse<DeploymentSettings>>(),
    ),

  getGitHubApp: () =>
    decodeApiError(
      api.get(githubAppPath).json<ApiResponse<GitHubAppDeployment>>(),
    ),

  updateGitHubApp: (input: UpdateGitHubAppDeploymentRequest) =>
    decodeApiError(
      api
        .put(githubAppPath, {
          json: input,
          timeout: GITHUB_APP_LIVE_REQUEST_TIMEOUT_MS,
          retry: 0,
        })
        .json<ApiResponse<GitHubAppDeployment>>(),
    ),

  testGitHubApp: () =>
    decodeApiError(
      api
        .post(`${githubAppPath}/test`, {
          timeout: GITHUB_APP_LIVE_REQUEST_TIMEOUT_MS,
          retry: 0,
        })
        .json<ApiResponse<GitHubAppDeployment>>(),
    ),

  // 只生成 manifest 与 state，不打 GitHub；随后由浏览器表单 POST 到 action_url。
  createManifest: (input: GitHubAppManifestRequest = {}) =>
    decodeApiError(
      api
        .post(`${githubAppPath}/manifest`, { json: input })
        .json<ApiResponse<GitHubAppManifestResponse>>(),
    ),

  // GitHub 回调带回的 code + state 换部署级 App 凭证（服务端打 GitHub 转换）。
  completeManifest: (input: CompleteGitHubAppManifestRequest) =>
    decodeApiError(
      api
        .post(`${githubAppPath}/manifest/complete`, {
          json: input,
          timeout: COMPLETE_MANIFEST_TIMEOUT_MS,
          retry: 0,
        })
        .json<ApiResponse<GitHubAppDeployment>>(),
    ),
};
