import { describe, expect, it } from "vitest";

import { buildIssuePrompt } from "./issue-prompt";

describe("buildIssuePrompt", () => {
  const baseInput = {
    projectId: "proj-1",
    issueId: "issue-1",
  };

  it("starts with the /fast-ship invocation", () => {
    const prompt = buildIssuePrompt(baseInput);

    expect(prompt.startsWith("/fast-ship ")).toBe(true);
  });

  it("includes the relentless questioning instructions and principles", () => {
    const prompt = buildIssuePrompt(baseInput);

    expect(prompt).toContain("relentless");
    expect(prompt).toContain("沿着设计树的每个分支逐步推进");
    expect(prompt).toContain("每个问题都附上你推荐的答案");
    expect(prompt).toContain("每次只问一个问题");
    expect(prompt).toContain("优先使用内置的问题工具来提问");
    expect(prompt).toContain("凡是通过探索代码库自己能搞清楚的事，绝不去问用户");
    expect(prompt).toContain("我不懂技术");
    expect(prompt).toContain("先添加临时的测试来复现问题");
    expect(prompt).toContain("记录问题根因");
    expect(prompt).toContain("删除添加的临时测试");
    expect(prompt).toContain("最后将所有共识决策写入协作区的实施建议列表中，无需创建计划。");
    expect(prompt).toContain("实施建议应为方向性的设计决策，而非细致的修改建议。");
  });

  it("embeds the project id and issue id", () => {
    const prompt = buildIssuePrompt(baseInput);

    expect(prompt).toContain("项目ID：proj-1");
    expect(prompt).toContain("问题ID：issue-1");
  });

  it("separates the ids from the principles with a divider", () => {
    const prompt = buildIssuePrompt(baseInput);

    expect(prompt).toContain("\n---\n项目ID：");
  });
});
