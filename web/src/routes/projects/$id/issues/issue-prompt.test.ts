import { describe, expect, it } from "vitest";

import { buildIssuePrompt } from "./issue-prompt";

describe("buildIssuePrompt", () => {
  const baseInput = {
    id: "issue-1",
    title: "优化提示词模版",
    body: "请添加处理流程",
    labels: [] as string[],
    comments: [] as { author: string; body: string }[],
  };

  it("renders the four-step handling flow", () => {
    const prompt = buildIssuePrompt(baseInput);

    expect(prompt).toContain("MULTIPLE UNRELATED");
    expect(prompt).toContain("ask the user which one to handle");
    expect(prompt).toContain("BUG: a defect");
    expect(prompt).toContain("NOT A BUG");
    expect(prompt).toContain("DO NOT write or modify any code");
    expect(prompt).toContain("High / Medium / Low");
    expect(prompt).toContain("failing test that reproduces the bug");
    expect(prompt).toContain("root cause");
  });

  it("uses a type-neutral lead and keeps the safety rules", () => {
    const prompt = buildIssuePrompt(baseInput);

    expect(prompt).toContain("needs to be handled");
    expect(prompt).not.toContain("needs to be resolved");
    expect(prompt).toContain("Do NOT modify the issue status");
    expect(prompt).toContain("Do NOT commit or push code");
  });

  it("embeds the issue id and title", () => {
    const prompt = buildIssuePrompt(baseInput);

    expect(prompt).toContain("<id>issue-1</id>");
    expect(prompt).toContain("<title>优化提示词模版</title>");
  });

  it("omits the labels block when there are no labels", () => {
    const prompt = buildIssuePrompt(baseInput);

    expect(prompt).not.toContain("<labels>");
  });

  it("joins labels with a comma when present", () => {
    const prompt = buildIssuePrompt({ ...baseInput, labels: ["bug", "priority-high"] });

    expect(prompt).toContain("<labels>bug, priority-high</labels>");
  });

  it("omits the comments block when there are no comments", () => {
    const prompt = buildIssuePrompt(baseInput);

    expect(prompt).not.toContain("<comments>");
  });

  it("renders each comment with its author", () => {
    const prompt = buildIssuePrompt({
      ...baseInput,
      comments: [
        { author: "alice", body: "第一条评论" },
        { author: "bob", body: "第二条评论" },
      ],
    });

    expect(prompt).toContain('<comment author="alice">第一条评论</comment>');
    expect(prompt).toContain('<comment author="bob">第二条评论</comment>');
  });

  it("escapes XML-special characters so payload cannot break the structure", () => {
    const prompt = buildIssuePrompt({
      id: "id-1",
      title: "标题 <inject>",
      body: '正文 <inject attr="x"> 结束',
      labels: ["bug&feat"],
      comments: [{ author: 'a"b', body: "<c>" }],
    });

    expect(prompt).not.toContain("<inject");
    expect(prompt).toContain("&lt;inject attr=&quot;x&quot;&gt;");
    expect(prompt).toContain('author="a&quot;b"');
    expect(prompt).toContain("bug&amp;feat");
    expect(prompt).toContain("&lt;c&gt;");
  });
});
