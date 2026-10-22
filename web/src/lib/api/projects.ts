import type { components } from "@/types/api.gen";
import { api } from "./client";

/** 契约类型直接引用 api.gen.ts；请求体字段语义见 openapi.yaml 的 @description。 */
type Schemas = components["schemas"];

type CreateProjectRequest = Schemas["CreateProjectRequest"];

// 更新请求的仓库与 Token 引用字段是三态：缺省保留、null 或空串清空、非空替换。
type UpdateProjectRequest = Schemas["UpdateProjectRequest"];

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
