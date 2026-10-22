import type { components } from "@/types/api.gen";
import { api } from "./client";

/**
 * 创建请求体保留手写：parent_id 的表单三态（不传=挂根级、null=同样挂根级、
 * 字符串=设为父文档）叠加创建端点的「可传 null」表单语义；契约的
 * CreateDocumentRequest.parent_id 不含 null，表达不了这个写法。
 */
export interface CreateDocumentPayload {
  title: string;
  body?: string;
  parent_id?: string | null;
}

export type UpdateDocumentPayload = components["schemas"]["UpdateDocumentRequest"];

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
