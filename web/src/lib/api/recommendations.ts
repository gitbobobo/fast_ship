import { api } from "./client";

export const recommendationApi = {
  list: (projectId?: string) =>
    api
      .get("recommendations", {
        searchParams: projectId ? { project_id: projectId } : {},
      })
      .json<ApiResponse<IssueRecommendationListData>>(),

  remove: (issueId: string) =>
    api.delete(`issues/${issueId}/recommendation`).json<ApiResponse<null>>(),
};
