export interface IssuePromptInput {
  projectId: string;
  issueId: string;
  content: string;
}

export const DEFAULT_ISSUE_PROMPT_CONTENT = "请处理此问题";

export const DEFAULT_ISSUE_PROMPTS: IssuePrompt[] = [
  { id: "default", name: "默认", content: DEFAULT_ISSUE_PROMPT_CONTENT },
];

export function normalizeIssuePrompts(
  prompts: IssuePrompt[] | null | undefined,
): IssuePrompt[] {
  if (!prompts || prompts.length === 0) {
    return DEFAULT_ISSUE_PROMPTS.map((p) => ({ ...p }));
  }
  return prompts.map((p) => ({ ...p }));
}

export function buildIssuePrompt(input: IssuePromptInput): string {
  return `/fast-ship ${input.content}
---
项目ID：${input.projectId}
问题ID：${input.issueId}`;
}
