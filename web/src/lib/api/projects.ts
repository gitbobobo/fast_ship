import { api } from "./client";

interface CreateProjectRequest {
  name: string;
  description?: string;
  repository_url?: string;
  /** 公开仓库 Token（Token 库记录 id）；可空 */
  public_github_token_id?: string;
  /** 代码仓库 Token（Token 库记录 id）；可空 */
  code_github_token_id?: string;
}

// 更新请求的 Token 引用字段是三态：缺省保留、null 或空串清空、非空替换。
interface UpdateProjectRequest {
  name?: string;
  description?: string;
  repository_url?: string;
  public_github_token_id?: string | null;
  code_github_token_id?: string | null;
}

export const projectApi = {
  list: (page = 1, pageSize = 20) =>
    api
      .get("projects", { searchParams: { page, page_size: pageSize } })
      .json<ApiResponse<PaginatedData<Project>>>(),

  get: (id: string) =>
    api.get(`projects/${id}`).json<ApiResponse<Project>>(),

  create: (data: CreateProjectRequest) =>
    api.post("projects", { json: data }).json<ApiResponse<Project>>(),

  update: (id: string, data: UpdateProjectRequest) =>
    api.put(`projects/${id}`, { json: data }).json<ApiResponse<Project>>(),

  delete: (id: string) =>
    api.delete(`projects/${id}`).json<ApiResponse<null>>(),

  getBranches: (id: string) =>
    api.get(`projects/${id}/branches`).json<ApiResponse<ProjectBranchesResponse>>(),
};
