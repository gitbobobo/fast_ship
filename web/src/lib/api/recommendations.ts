import { api } from "./client";

export const recommendationApi = {
  list: (projectId?: string) =>
    api
      .get("recommendations", {
        searchParams: projectId ? { project_id: projectId } : {},
      })
      .json<ApiResponse<IssueRecommendationListData>>(),

  defer: (issueId: string, note?: string) =>
    api
      .put(`issues/${issueId}/recommendation/defer`, { json: { note } })
      .json<ApiResponse<IssueRecommendation>>(),

  restore: (issueId: string) =>
    api
      .delete(`issues/${issueId}/recommendation/defer`)
      .json<ApiResponse<IssueRecommendation>>(),

  remove: (issueId: string) =>
    api.delete(`issues/${issueId}/recommendation`).json<ApiResponse<null>>(),
};
