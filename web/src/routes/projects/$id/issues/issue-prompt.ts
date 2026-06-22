export interface IssuePromptInput {
  projectId: string;
  issueId: string;
}

export function buildIssuePrompt(input: IssuePromptInput): string {
  return `/fast-ship 通过探索、调研、写临时测试复现等方式，彻底搞懂要解决这个问题需要做哪些事情。向我追问决策树的分支以达成共识，并总结为方向性的设计决策作为实施建议。实施建议应保持简洁，不要细致到文件。

在保存到问题协作区之前，先把准备添加的实施建议展示给我，征得我同意后再发送请求写入。在我没有明确同意之前，仅按我的要求修改，并始终展示最新的实施建议列表。

我同意后，先检查该问题的工作流状态：若不处于「待处理」（workflow_status 非 todo），则通过 PUT /api/issues/:issue_id/internal-meta 更新为 todo，再保存实施建议。

---
项目ID：${input.projectId}
问题ID：${input.issueId}`;
}
