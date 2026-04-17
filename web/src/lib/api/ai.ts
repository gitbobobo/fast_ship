import { api } from "./client";

interface UpdateAISettingsRequest {
  api_host: string;
  api_key?: string;
  model: string;
}

export const aiApi = {
  getSettings: () => api.get("ai/settings").json<ApiResponse<AISettings>>(),

  updateSettings: (data: UpdateAISettingsRequest) =>
    api.put("ai/settings", { json: data }).json<ApiResponse<AISettings>>(),

  suggestIssueChecklist: (issueId: string) =>
    api
      .post(`issues/${issueId}/checklist-suggestions`)
      .json<ApiResponse<IssueChecklistSuggestions>>(),
};
