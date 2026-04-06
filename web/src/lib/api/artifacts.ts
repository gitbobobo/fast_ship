import { api } from "./client";

export const artifactApi = {
  upload: (vid: string, formData: FormData) =>
    api
      .post(`versions/${vid}/artifacts`, { body: formData })
      .json<ApiResponse<Artifact>>(),

  delete: (aid: string) =>
    api.delete(`artifacts/${aid}`).json<ApiResponse<null>>(),

  downloadUrl: (aid: string) => `/api/artifacts/${aid}/download`,
};
