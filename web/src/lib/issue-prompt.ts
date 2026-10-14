export interface IssuePromptInput {
  projectId: string;
  issueId: string;
  content: string;
  /** 推荐理由；仅推荐入口传入，追加为引用块最后一行。 */
  reason?: string;
}

export const DEFAULT_ISSUE_PROMPT_CONTENT = "请处理此问题";

export const DEFAULT_ISSUE_PROMPTS: IssuePrompt[] = [
  {
    id: "default",
    name: "默认",
    content: DEFAULT_ISSUE_PROMPT_CONTENT,
  },
];

export function normalizeIssuePrompts(
  prompts: IssuePrompt[] | null | undefined,
): IssuePrompt[] {
  if (!prompts || prompts.length === 0) {
    return DEFAULT_ISSUE_PROMPTS.map((p) => ({ ...p }));
  }
  return prompts.map((p) => ({ ...p }));
}

/**
 * 提示词 = 模板正文 + 结尾的 Markdown 引用块。引用块完全由系统生成：
 * 指引 Agent 用 fast-ship 技能（未安装时附安装命令）、附项目/问题 ID；
 * 推荐理由仅推荐场景出现。不再有 /fast-ship 前缀。
 */
export function buildIssuePrompt(input: IssuePromptInput): string {
  // 尾部换行不进正文（与服务端 BuildIssuePrompt 的 TrimRight 同口径），
  // 否则 web 生成的提示词比服务端多出空行，两边格式不一致。
  const content = input.content.replace(/\n+$/, "");
  const lines = [
    "以下信息由 Fast Ship 附加。请使用 fast-ship 技能查询并处理该问题。",
    "若当前环境未安装 fast-ship 技能，先征得用户同意，再执行：",
    "`npx skills add https://github.com/gitbobobo/fast_ship --skill fast-ship`",
    `项目ID：${input.projectId}`,
    `问题ID：${input.issueId}`,
  ];
  const reason = input.reason?.trim();
  if (reason) {
    const reasonLines = reason.split("\n");
    lines.push(`推荐理由：${reasonLines[0]}`, ...reasonLines.slice(1));
  }
  const block = lines.map((line) => `> ${line}`).join("\n");
  return content ? `${content}\n\n${block}` : block;
}
