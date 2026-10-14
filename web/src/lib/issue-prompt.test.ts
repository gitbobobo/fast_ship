import { describe, expect, it } from "vitest";

import {
  buildIssuePrompt,
  DEFAULT_ISSUE_PROMPTS,
  normalizeIssuePrompts,
} from "./issue-prompt";

const APPENDED_BLOCK = `> 以下信息由 Fast Ship 附加。请使用 fast-ship 技能查询并处理该问题。
> 若当前环境未安装 fast-ship 技能，先征得用户同意，再执行：
> \`npx skills add https://github.com/gitbobobo/fast_ship --skill fast-ship\`
> 项目ID：p1
> 问题ID：i1`;

describe("buildIssuePrompt", () => {
  it("puts template content first, then a system-appended quote block", () => {
    const prompt = buildIssuePrompt({
      projectId: "p1",
      issueId: "i1",
      content: "请处理此问题",
    });

    expect(prompt).toBe(`请处理此问题\n\n${APPENDED_BLOCK}`);
  });

  it("has no /fast-ship prefix and ends the reference block with the issue id", () => {
    const prompt = buildIssuePrompt({
      projectId: "p1",
      issueId: "i1",
      content: "自定义指令",
    });

    expect(prompt.startsWith("自定义指令")).toBe(true);
    expect(prompt).not.toContain("/fast-ship");
    // --skill fast-ship 出现在安装命令里，而非斜杠命令前缀
    expect(prompt).toContain("--skill fast-ship");
    expect(prompt.endsWith("> 问题ID：i1")).toBe(true);
    // 附加块每行都在引用块内
    for (const line of prompt.split("\n").slice(3)) {
      expect(line.startsWith("> ")).toBe(true);
    }
  });

  it("embeds custom content verbatim including surrounding whitespace", () => {
    const prompt = buildIssuePrompt({
      projectId: "p1",
      issueId: "i1",
      content: "  保留前后空格  ",
    });

    expect(prompt.startsWith("  保留前后空格  \n\n> 以下信息")).toBe(true);
  });

  it("appends the recommendation reason as the last quoted lines, only when given", () => {
    const prompt = buildIssuePrompt({
      projectId: "p1",
      issueId: "i1",
      content: "请处理此问题",
      reason: "先做能解锁后续工作\n第二行",
    });

    expect(prompt).toBe(
      `请处理此问题\n\n${APPENDED_BLOCK}\n> 推荐理由：先做能解锁后续工作\n> 第二行`,
    );
  });

  it("omits the reason line when the reason is blank", () => {
    const prompt = buildIssuePrompt({
      projectId: "p1",
      issueId: "i1",
      content: "请处理此问题",
      reason: "   ",
    });

    expect(prompt).not.toContain("推荐理由");
    expect(prompt.endsWith("> 问题ID：i1")).toBe(true);
  });

  it("renders only the appended block when content is empty", () => {
    const prompt = buildIssuePrompt({
      projectId: "p1",
      issueId: "i1",
      content: "",
    });

    expect(prompt).toBe(APPENDED_BLOCK);
  });
});

describe("normalizeIssuePrompts", () => {
  it("returns a single default entry for null/undefined/empty", () => {
    for (const input of [null, undefined, []]) {
      const result = normalizeIssuePrompts(input);
      expect(result).toHaveLength(1);
      expect(result[0].id).toBe("default");
      expect(result[0].name).toBe("默认");
      expect(result[0].content).toBe("请处理此问题");
    }
  });

  it("returns a copy that is independent from the DEFAULT_ISSUE_PROMPTS constant", () => {
    const result = normalizeIssuePrompts(null);
    result[0].id = "mutated";
    result[0].content = "changed";

    expect(DEFAULT_ISSUE_PROMPTS[0].id).toBe("default");
    expect(DEFAULT_ISSUE_PROMPTS[0].content).toBe("请处理此问题");
  });

  it("returns non-empty arrays preserving order, content and preferences", () => {
    const input: IssuePrompt[] = [
      { id: "a", name: "A", content: "内容A" },
      {
        id: "b",
        name: "B",
        content: "内容B",
        preferences: { environment: "e1", workspace: "root" },
      },
    ];
    const result = normalizeIssuePrompts(input);

    expect(result).toHaveLength(2);
    expect(result.map((p) => p.id)).toEqual(["a", "b"]);
    expect(result[0]).toEqual(input[0]);
    expect(result[1]).toEqual(input[1]);
    expect(result[1].preferences).toEqual({
      environment: "e1",
      workspace: "root",
    });
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
