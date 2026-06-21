export interface IssuePromptInput {
  projectId: string;
  issueId: string;
}

export function buildIssuePrompt(input: IssuePromptInput): string {
  return `/fast-ship 请对此问题的每个方面进行 relentless 的追问，直到我们达成共识。沿着设计树的每个分支逐步推进，逐一解决决策之间的依赖关系。每个问题都附上你推荐的答案。

原则：
- 每次只问一个问题。
- 优先使用内置的问题工具来提问。
- 同时探索和提问——凡是通过探索代码库自己能搞清楚的事，绝不去问用户。
- 我不懂技术，凡是技术方面的问题，提出问题后，尝试自己解答，选择长期最佳决策。
- 如果是 bug，先添加临时的测试来复现问题，如果成功复现，记录问题根因，未能复现则跳过处理。最后记得删除添加的临时测试。

最后将所有共识决策写入协作区的实施建议列表中，无需创建计划。实施建议应为方向性的设计决策，而非细致的修改建议。

---
项目ID：${input.projectId}
问题ID：${input.issueId}`;
}
