import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { BotMarkdown } from "./bot-markdown";

describe("BotMarkdown", () => {
  it("渲染 GFM 各元素：标题/列表/表格/行内代码/代码块/引用/分隔线/链接", () => {
    const { container } = render(
      <BotMarkdown
        text={
          "# 标题\n\n- a\n- b\n\n1. x\n2. y\n\n| x | y |\n|---|---|\n| 1 | 2 |\n\n`inline`\n\n```ts\nconst a = 1;\n```\n\n> 引用\n\n---\n\n[link](https://example.com)"
        }
      />,
    );
    expect(screen.getByRole("heading", { name: "标题" })).toBeInTheDocument();
    expect(container.querySelectorAll("ul li")).toHaveLength(2);
    expect(container.querySelectorAll("ol li")).toHaveLength(2);
    expect(container.querySelector("table")).not.toBeNull();
    expect(container.querySelector("blockquote")).not.toBeNull();
    expect(container.querySelector("hr")).not.toBeNull();
    expect(container.querySelector("pre code")?.textContent).toContain(
      "const a = 1;",
    );
    const link = screen.getByRole("link", { name: "link" });
    expect(link).toHaveAttribute("target", "_blank");
    expect(link.getAttribute("rel")).toContain("noopener");
  });

  it("原始 HTML 与 <script> 不执行，图片（Markdown 与 HTML）都不渲染", () => {
    const { container } = render(
      <BotMarkdown
        text={
          '![shot](https://example.com/a.png)\n\n<img src="https://example.com/b.png" onerror="alert(1)">\n\n<script>alert(1)</script>'
        }
      />,
    );
    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector("script")).toBeNull();
  });

  it("丢弃 javascript: 链接目标", () => {
    render(<BotMarkdown text="[x](javascript:alert(1))" />);
    const link = screen.queryByRole("link");
    expect(link?.getAttribute("href") ?? "").not.toContain("javascript:");
  });

  it("围栏代码块带语言标签与复制按钮", () => {
    render(<BotMarkdown text={"```ts\nconst a = 1;\n```"} />);
    expect(screen.getByText("ts")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /复制/ })).toBeInTheDocument();
  });

  it("无语言围栏显示「代码」", () => {
    render(<BotMarkdown text={"```\nplain\n```"} />);
    expect(screen.getByText("代码")).toBeInTheDocument();
  });

  it("非流式：文末未闭合围栏按 CommonMark 渲染成代码块", () => {
    const { container } = render(
      <BotMarkdown text={"前文\n```ts\nconst a = 1;"} />,
    );
    expect(container.querySelector("pre")).not.toBeNull();
  });

  it("流式：未闭合围栏降级为纯文本，不渲染成代码块", () => {
    const { container } = render(
      <BotMarkdown streaming text={"前文\n```ts\nconst a = 1;"} />,
    );
    expect(container.querySelector("pre")).toBeNull();
    // 围栏标记原样可见，未被吞进代码块
    expect(container.textContent).toContain("```ts");
    expect(container.textContent).toContain("const a = 1;");
  });

  it("流式：围栏闭合后升级回代码块渲染", () => {
    const { container, rerender } = render(
      <BotMarkdown streaming text={"前文\n```ts\nconst a = 1;"} />,
    );
    expect(container.querySelector("pre")).toBeNull();
    rerender(
      <BotMarkdown streaming text={"前文\n```ts\nconst a = 1;\n```\n后文"} />,
    );
    expect(container.querySelector("pre code")?.textContent).toContain(
      "const a = 1;",
    );
  });
});
