import { api } from "./client";

interface CreateProjectRequest {
  name: string;
  description?: string;
  repository_url: string;
  github_token?: string;
  source_project_id?: string;
}

interface UpdateProjectRequest {
  name?: string;
  description?: string;
  repository_url?: string;
  github_token?: string;
  source_project_id?: string;
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
