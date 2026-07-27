import { api } from "./client";

export const issuePromptApi = {
  get: () => api.get("issue-prompts").json<ApiResponse<IssuePromptSettings>>(),

  update: (prompts: IssuePrompt[]) =>
    api
      .put("issue-prompts", { json: { prompts } })
      .json<ApiResponse<IssuePromptSettings>>(),
};
