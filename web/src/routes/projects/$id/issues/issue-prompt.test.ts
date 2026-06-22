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
    expect(prompt).toContain("并总结为方向性的设计决策作为实施建议。");
    expect(prompt).toContain("实施建议应保持简洁，不要细致到文件。");
    expect(prompt).toContain("在保存到问题协作区之前，先把准备添加的实施建议展示给我");
    expect(prompt).toContain("在我没有明确同意之前，仅按我的要求修改，并始终展示最新的实施建议列表。");
    expect(prompt).toContain("检查该问题的工作流状态");
    expect(prompt).toContain("若不处于「待处理」（workflow_status 非 todo）");
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
