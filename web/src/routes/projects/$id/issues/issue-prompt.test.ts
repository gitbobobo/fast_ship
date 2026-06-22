import { describe, expect, it } from "vitest";

import { buildIssuePrompt } from "./issue-prompt";

describe("buildIssuePrompt", () => {
  const baseInput = {
    projectId: "proj-1",
    issueId: "issue-1",
  };

  it("starts with the /fast-ship invocation and the new instructions", () => {
    const prompt = buildIssuePrompt(baseInput);

    expect(prompt.startsWith("/fast-ship ")).toBe(true);
    expect(prompt).toContain("通过探索、调研、写临时测试复现等方式");
    expect(prompt).toContain("彻底搞懂要解决这个问题需要做哪些事情");
    expect(prompt).toContain("向我追问决策树的分支以达成共识");
    expect(prompt).toContain("并总结为方向性的设计决策保存到问题协作区的实施建议列表中。");
    expect(prompt).toContain("实施建议应保持简洁，不要细致到文件。");
  });

  it("embeds the project id and issue id", () => {
    const prompt = buildIssuePrompt(baseInput);

    expect(prompt).toContain("项目ID：proj-1");
    expect(prompt).toContain("问题ID：issue-1");
  });

  it("separates the ids from the instructions with a divider", () => {
    const prompt = buildIssuePrompt(baseInput);

    expect(prompt).toContain("\n---\n项目ID：");
  });
});
