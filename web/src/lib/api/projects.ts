import { api } from "./client";

interface CreateProjectRequest {
  name: string;
  description?: string;
  github_owner: string;
  github_repo: string;
  github_token: string;
}

interface UpdateProjectRequest {
  name?: string;
  description?: string;
  github_owner?: string;
  github_repo?: string;
  github_token?: string;
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
};
