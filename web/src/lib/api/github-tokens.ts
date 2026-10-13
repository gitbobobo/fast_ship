import { api } from "./client";

interface CreateGitHubTokenRequest {
  title: string;
  token: string;
}

interface UpdateGitHubTokenRequest {
  title?: string;
  token?: string;
}

export const githubTokenApi = {
  list: () => api.get("github-tokens").json<ApiResponse<GitHubToken[]>>(),

  create: (data: CreateGitHubTokenRequest) =>
    api.post("github-tokens", { json: data }).json<ApiResponse<GitHubToken>>(),

  update: (id: string, data: UpdateGitHubTokenRequest) =>
    api.put(`github-tokens/${id}`, { json: data }).json<ApiResponse<GitHubToken>>(),

  delete: (id: string) =>
    api.delete(`github-tokens/${id}`).json<ApiResponse<null>>(),

  detect: (id: string) =>
    api.post(`github-tokens/${id}/detect`).json<ApiResponse<GitHubToken>>(),
};
