import { api } from "./client";

interface CreateVersionRequest {
  version_number: string;
  release_notes?: string;
  target_commitish?: string;
}

interface UpdateVersionRequest {
  release_notes?: string;
  target_commitish?: string;
}

export const versionApi = {
  list: (projectId: string, status?: string) =>
    api
      .get(`projects/${projectId}/versions`, {
        searchParams: {
          page: 1,
          page_size: 100,
          ...(status ? { status } : {}),
        },
      })
      .json<ApiResponse<PaginatedData<Version>>>(),

  get: (vid: string) =>
    api.get(`versions/${vid}`).json<ApiResponse<Version>>(),

  create: (projectId: string, data: CreateVersionRequest) =>
    api
      .post(`projects/${projectId}/versions`, { json: data })
      .json<ApiResponse<Version>>(),

  update: (vid: string, data: UpdateVersionRequest) =>
    api
      .put(`versions/${vid}`, { json: data })
      .json<ApiResponse<Version>>(),

  delete: (vid: string) =>
    api.delete(`versions/${vid}`).json<ApiResponse<null>>(),

  ship: (vid: string) =>
    api.post(`versions/${vid}/ship`).json<ApiResponse<Version>>(),
};
