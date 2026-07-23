import { api } from "./client";

export interface CreateDocumentPayload {
  title: string;
  body?: string;
  parent_id?: string | null;
}

export interface UpdateDocumentPayload {
  title?: string;
  body?: string;
  parent_id?: string | null;
}

export const documentApi = {
  list: (projectId: string) =>
    api
      .get(`projects/${projectId}/documents`)
      .json<ApiResponse<DocumentListData>>(),

  create: (projectId: string, payload: CreateDocumentPayload) =>
    api
      .post(`projects/${projectId}/documents`, { json: payload })
      .json<ApiResponse<DocumentDetail>>(),

  get: (docId: string) =>
    api.get(`documents/${docId}`).json<ApiResponse<DocumentDetail>>(),

  update: (docId: string, payload: UpdateDocumentPayload) =>
    api
      .put(`documents/${docId}`, { json: payload })
      .json<ApiResponse<DocumentDetail>>(),

  delete: (docId: string) =>
    api.delete(`documents/${docId}`).json<ApiResponse<null>>(),
};
