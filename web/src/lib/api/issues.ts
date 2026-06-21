import { api } from "./client";

interface IssueListParams {
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

interface CreateInternalIssueCommentRequest {
  body: string;
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
};
