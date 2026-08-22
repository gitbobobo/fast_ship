import type { IssueWorkflowStatus } from "@/lib/issue-workflow-status";
import { api } from "./client";

export interface IssueListParams {
  state?: string;
  q?: string;
  label?: string;
  source?: string;
  workflow_status?: string;
  sort?: string;
  page?: number;
  page_size?: number;
}

interface UpdateIssueInternalMetaRequest {
  workflow_status: "" | "todo" | "in_progress" | "done";
}

interface ReplaceIssueChecklistRequest {
  items: Array<{
    id?: string;
    title: string;
    is_completed: boolean;
  }>;
}

interface CreateIssueRequest {
  title: string;
  body: string;
  workflow_status?: "" | "todo" | "in_progress" | "done";
  source?: "internal" | "github";
}

interface UpdateInternalIssueRequest {
  title?: string;
  body?: string;
  state?: "open" | "closed";
  state_reason?: "completed" | "not_planned" | "reopened";
  labels?: string[];
}

interface BatchCloseDoneRequest {
  source?: "internal" | "github";
}

interface BatchCloseDoneResponse {
  total: number;
  succeeded: number;
  failed: number;
  failures: Array<{
    id: string;
    reference?: string;
    error: string;
  }>;
  elapsed_ms: number;
}

interface CreateInternalIssueCommentRequest {
  body: string;
}

interface UpsertShipHookRequest {
  comment_body?: string;
  close?: boolean;
  workflow_status?: IssueWorkflowStatus;
}

export const issueApi = {
  create: (projectId: string, data: CreateIssueRequest) =>
    api.post(`projects/${projectId}/issues`, { json: data }).json<ApiResponse<Issue>>(),

  list: (projectId: string, params: IssueListParams = {}) =>
    api
      .get(`projects/${projectId}/issues`, {
        searchParams: {
          page: params.page ?? 1,
          page_size: params.page_size ?? 20,
          ...(params.state ? { state: params.state } : {}),
          ...(params.q ? { q: params.q } : {}),
          ...(params.label ? { label: params.label } : {}),
          ...(params.source ? { source: params.source } : {}),
          ...(params.workflow_status ? { workflow_status: params.workflow_status } : {}),
          ...(params.sort ? { sort: params.sort } : {}),
        },
      })
      .json<ApiResponse<PaginatedData<Issue>>>(),

  count: (projectId: string, params: IssueListParams = {}) =>
    api
      .get(`projects/${projectId}/issues/count`, {
        searchParams: {
          ...(params.state ? { state: params.state } : {}),
          ...(params.q ? { q: params.q } : {}),
          ...(params.label ? { label: params.label } : {}),
          ...(params.source ? { source: params.source } : {}),
          ...(params.workflow_status ? { workflow_status: params.workflow_status } : {}),
        },
      })
      .json<ApiResponse<{ count: number }>>(),

  batchCloseDone: (projectId: string, data: BatchCloseDoneRequest = {}) =>
    api
      .post(`projects/${projectId}/issues/batch-close`, { json: data })
      .json<ApiResponse<BatchCloseDoneResponse>>(),

  filterOptions: (projectId: string) =>
    api
      .get(`projects/${projectId}/issues/filter-options`)
      .json<ApiResponse<IssueFilterOptions>>(),

  repoLabels: (projectId: string) =>
    api
      .get(`projects/${projectId}/issues/repo-labels`)
      .json<ApiResponse<IssueLabel[]>>(),

  get: (issueId: string) =>
    api.get(`issues/${issueId}`).json<ApiResponse<Issue>>(),

  update: (issueId: string, data: UpdateInternalIssueRequest) =>
    api.put(`issues/${issueId}`, { json: data }).json<ApiResponse<Issue>>(),

  uploadAsset: (issueId: string, formData: FormData) =>
    api.post(`issues/${issueId}/assets`, { body: formData }).json<ApiResponse<IssueAsset>>(),

  uploadDraftAsset: (projectId: string, formData: FormData) =>
    api.post(`projects/${projectId}/issues/assets`, { body: formData }).json<ApiResponse<IssueAsset>>(),

  comments: (issueId: string, page = 1, pageSize = 20) =>
    api
      .get(`issues/${issueId}/comments`, {
        searchParams: { page, page_size: pageSize },
      })
      .json<ApiResponse<PaginatedData<IssueComment>>>(),

  createComment: (issueId: string, data: CreateInternalIssueCommentRequest) =>
    api.post(`issues/${issueId}/comments`, { json: data }).json<ApiResponse<IssueComment>>(),

  timeline: (issueId: string, page = 1, pageSize = 20) =>
    api
      .get(`issues/${issueId}/timeline`, {
        searchParams: { page, page_size: pageSize },
      })
      .json<ApiResponse<PaginatedData<IssueTimelineEvent>>>(),

  sync: (projectId: string) =>
    api
      .post(`projects/${projectId}/issues/sync`)
      .json<ApiResponse<IssueSyncResult>>(),

  updateInternalMeta: (issueId: string, data: UpdateIssueInternalMetaRequest) =>
    api
      .put(`issues/${issueId}/internal-meta`, { json: data })
      .json<ApiResponse<IssueInternalMeta | null>>(),

  replaceChecklist: (issueId: string, data: ReplaceIssueChecklistRequest) =>
    api
      .put(`issues/${issueId}/checklist`, { json: data })
      .json<ApiResponse<IssueInternalMeta | null>>(),

  getCollab: (issueId: string) =>
    api.get(`issues/${issueId}/collab`).json<ApiResponse<IssueCollabArea>>(),

  deleteCollabSection: (
    issueId: string,
    section: "all" | "suggestions" | "plan" | "review" | "summary",
  ) => {
    const path =
      section === "all"
        ? `issues/${issueId}/collab`
        : `issues/${issueId}/collab/${section}`;
    return api.delete(path).json<ApiResponse<null>>();
  },

  upsertShipHook: (issueId: string, data: UpsertShipHookRequest) =>
    api
      .put(`issues/${issueId}/ship-hook`, { json: data })
      .json<ApiResponse<IssueShipHook>>(),

  deleteShipHook: (issueId: string) =>
    api.delete(`issues/${issueId}/ship-hook`).json<ApiResponse<null>>(),
};
