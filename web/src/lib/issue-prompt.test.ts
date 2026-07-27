import { describe, expect, it } from "vitest";

import {
  buildIssuePrompt,
  DEFAULT_ISSUE_PROMPTS,
  normalizeIssuePrompts,
} from "./issue-prompt";

describe("buildIssuePrompt", () => {
  it("starts with /fast-ship invocation and embeds default content and ids", () => {
    const prompt = buildIssuePrompt({
      projectId: "p1",
      issueId: "i1",
      content: "请处理此问题",
    });

    expect(prompt.startsWith("/fast-ship ")).toBe(true);
    expect(prompt).toContain("请处理此问题");
    expect(prompt).toContain("项目ID：p1");
    expect(prompt).toContain("问题ID：i1");
    expect(prompt).toContain("\n---\n项目ID：");
  });

  it("does not contain the legacy hardcoded long-form instructions", () => {
    const prompt = buildIssuePrompt({
      projectId: "p1",
      issueId: "i1",
      content: "请处理此问题",
    });

    expect(prompt).not.toContain("决策树的分支");
    expect(prompt).not.toContain("workflow_status 非 todo");
    expect(prompt).not.toContain("实施建议");
  });

  it("embeds custom content verbatim", () => {
    const prompt = buildIssuePrompt({
      projectId: "p1",
      issueId: "i1",
      content: "自定义指令",
    });

    expect(prompt.startsWith("/fast-ship 自定义指令")).toBe(true);
    expect(prompt).toContain("/fast-ship 自定义指令\n---\n项目ID：p1");
  });

  it("preserves surrounding whitespace in content without trimming", () => {
    const prompt = buildIssuePrompt({
      projectId: "p1",
      issueId: "i1",
      content: "  保留前后空格  ",
    });

    expect(prompt).toContain("/fast-ship   保留前后空格  \n---\n");
  });
});

describe("normalizeIssuePrompts", () => {
  it("returns a single default entry for null", () => {
    const result = normalizeIssuePrompts(null);
    expect(result).toHaveLength(1);
    expect(result[0].id).toBe("default");
    expect(result[0].name).toBe("默认");
    expect(result[0].content).toBe("请处理此问题");
  });

  it("returns a single default entry for undefined", () => {
    const result = normalizeIssuePrompts(undefined);
    expect(result).toHaveLength(1);
    expect(result[0].id).toBe("default");
  });

  it("returns a single default entry for an empty array", () => {
    const result = normalizeIssuePrompts([]);
    expect(result).toHaveLength(1);
    expect(result[0].id).toBe("default");
  });

  it("returns a copy that is independent from the DEFAULT_ISSUE_PROMPTS constant", () => {
    const result = normalizeIssuePrompts(null);
    result[0].id = "mutated";
    result[0].content = "changed";

    expect(DEFAULT_ISSUE_PROMPTS[0].id).toBe("default");
    expect(DEFAULT_ISSUE_PROMPTS[0].content).toBe("请处理此问题");
  });

  it("returns non-empty arrays preserving order and content", () => {
    const input: IssuePrompt[] = [
      { id: "a", name: "A", content: "内容A" },
      { id: "b", name: "B", content: "内容B" },
    ];
    const result = normalizeIssuePrompts(input);

    expect(result).toHaveLength(2);
    expect(result.map((p) => p.id)).toEqual(["a", "b"]);
    expect(result[0]).toEqual(input[0]);
    expect(result[1]).toEqual(input[1]);
  });

  it("returns copies whose mutation does not affect the source array", () => {
    const input: IssuePrompt[] = [
      { id: "a", name: "A", content: "内容A" },
    ];
    const result = normalizeIssuePrompts(input);
    result[0].id = "changed";

    expect(input[0].id).toBe("a");
  });
});
