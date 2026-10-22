import type { components } from "@/types/api.gen";
import { api } from "./client";

/** 契约类型直接引用 api.gen.ts。 */
type Schemas = components["schemas"];

type CreateGitHubTokenRequest = Schemas["CreateGitHubTokenRequest"];

type UpdateGitHubTokenRequest = Schemas["UpdateGitHubTokenRequest"];

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
