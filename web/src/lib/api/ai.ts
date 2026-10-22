import type { components } from "@/types/api.gen";
import { api } from "./client";

/** 契约类型直接引用 api.gen.ts。 */
type Schemas = components["schemas"];

type UpdateAISettingsRequest = Schemas["UpdateAISettingsRequest"];

export const aiApi = {
  getSettings: () => api.get("ai/settings").json<ApiResponse<AISettings>>(),

  updateSettings: (data: UpdateAISettingsRequest) =>
    api.put("ai/settings", { json: data }).json<ApiResponse<AISettings>>(),

  suggestIssueChecklist: (issueId: string) =>
    api
      .post(`issues/${issueId}/checklist-suggestions`)
      .json<ApiResponse<IssueChecklistSuggestions>>(),

  generateTitle: (body: string) =>
    api
      .post("ai/generate-title", { json: { body } })
      .json<ApiResponse<GenerateTitleResponse>>(),
};
