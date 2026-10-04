export interface IssuePromptInput {
  projectId: string;
  issueId: string;
  content: string;
  /** 推荐理由；非空时追加在问题ID之后，供 Agent 了解为什么推荐先做。 */
  reason?: string;
}

export const DEFAULT_ISSUE_PROMPT_CONTENT = "请处理此问题";

export const DEFAULT_ISSUE_PROMPTS: IssuePrompt[] = [
  {
    id: "default",
    name: "默认",
    content: DEFAULT_ISSUE_PROMPT_CONTENT,
    supports_batch: true,
  },
];

export function normalizeIssuePrompts(
  prompts: IssuePrompt[] | null | undefined,
): IssuePrompt[] {
  if (!prompts || prompts.length === 0) {
    return DEFAULT_ISSUE_PROMPTS.map((p) => ({ ...p }));
  }
  return prompts.map((p) => ({ ...p, supports_batch: p.supports_batch ?? false }));
}

export function buildIssuePromptBatch(input: {
  projectId: string;
  content: string;
  issueIds: string[];
}): string {
  const issueIdLines = input.issueIds.map(
    (issueId) => `问题ID：${issueId}`,
  );
  return `/fast-ship ${input.content}
---
项目ID：${input.projectId}
${issueIdLines.join("\n")}`;
}

export function buildIssuePrompt(input: IssuePromptInput): string {
  const prompt = buildIssuePromptBatch({
    projectId: input.projectId,
    content: input.content,
    issueIds: [input.issueId],
  });
  const reason = input.reason?.trim();
  return reason ? `${prompt}\n推荐理由：${reason}` : prompt;
}
