export interface IssuePromptInput {
  projectId: string;
  issueId: string;
}

export function buildIssuePrompt(input: IssuePromptInput): string {
  return `/fast-ship 通过探索、调研、写临时测试复现等方式，彻底搞懂要解决这个问题需要做哪些事情。向我追问决策树的分支以达成共识，并总结为方向性的设计决策保存到问题协作区的实施建议列表中。实施建议应保持简洁，不要细致到文件。

---
项目ID：${input.projectId}
问题ID：${input.issueId}`;
}
