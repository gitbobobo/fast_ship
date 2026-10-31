import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { BotMarkdown } from "./bot-markdown";
import { splitIncompleteFence, splitStreamingMarkdown } from "@/lib/bots/markdown-stream";

// 等价性样本：闭合/完整文本，流式分块渲染与整段解析必须产出相同 DOM。
// 不含原始 HTML 行（行首 `<`）与引用/脚注定义（行首 `[`）：那两类文本走
// 「整段退回」路径，单独在 BAIL_SAMPLES 里断言。
const EQUIV_SAMPLES: Array<[string, string]> = [
  ["多段段落", "第一段。\n\n第二段。\n\n第三段。"],
  ["标题", "# 大标题\n\n## 小标题\n\n正文。"],
  ["紧凑列表", "- a\n- b\n- c"],
  ["松散列表", "- a\n\n- b\n\n- c"],
  ["有序列表", "1. 第一\n2. 第二\n3. 第三"],
  ["嵌套列表", "- 外层\n  - 内层\n  - 内层二\n- 外层二"],
  ["表格", "| x | y |\n| --- | --- |\n| 1 | 2 |\n\n表格后正文。"],
  ["闭合围栏含空行", "前文。\n\n```ts\nconst a = 1;\n\nconst b = 2;\n```\n\n后文。"],
  ["blockquote", "> 引用一\n>\n> 引用二\n\n正文。"],
  ["分隔线", "上文\n\n---\n\n下文"],
  ["行内代码", "使用 `npm run dev` 启动，然后 `pnpm build`。"],
  ["链接", "参见 [文档](https://example.com/a) 与 [仓库](https://example.com/b)。"],
  ["末尾空行", "第一段。\n\n第二段。\n\n"],
  ["连续空行", "第一段。\n\n\n\n第二段。"],
  ["缩进代码块", "前文。\n\n    const a = 1;\n\n    const b = 2;"],
  ["两个相邻表格", "| a |\n| --- |\n| 1 |\n\n| b |\n| --- |\n| 2 |"],
  ["列表后续段落", "段落。\n\n- a\n- b\n\n后续段落。"],
  ["引用后续段落", "> 引用\n\n正文。"],
  ["松散列表后续段落", "- a\n\n  b\n\n收尾。"],
  // 反例样本：NBSP 行、开头空行——分块切错就会与整段解析不一致。
  ["NBSP 行", "A\n\u00a0\nB\n"],
  // tail 只剩一行 NBSP：若把这篇 tail 当空白而不渲染，内容直接丢。
  ["NBSP 尾段", "A\n\n\u00a0\n"],
  ["开头空行", "\n\nHello\n"],
  // 围栏属于引用块：引用结束时（裸空行或掉出容器）围栏随之结束，后面的正文
  // 不能再被当成代码块吞掉。降级扫描漏掉这条规则时这里会全线变红。
  ["引用里的围栏被裸空行结束", "> ```\n> code\n\nZQMARKER\n"],
  ["引用里惰性续行结束围栏", "> ```\n> code\nZQMARKER\n"],
  ["围栏行后立刻掉出容器", "> ```\ncode\n\nZQMARKER\n"],
  ["下一个引用不算进代码块", "> ```\n> code\n\n> stillfence\n"],
  ["列表项内围栏（既有边界）", "- ```\n  code\n\nZQMARKER\n"],
];

// 退回样本：行首 `<`（原始 HTML 块）或行首 `[`（链接/脚注引用定义）。这些文本
// 不分块，流式渲染必须与非流式逐字节一致，且不得保留围栏降级。
const BAIL_SAMPLES: Array<[string, string]> = [
  ["引用式链接", "见 [标注] 与 [另一处][标]。\n\n[标注]: https://example.com/a\n[标]: https://example.com/b\n"],
  ["引用定义在后", "See [x].\n\nMiddle paragraph.\n\n[x]: https://example.com"],
  ["引用定义在前", "[x]: https://example.com\n\nMiddle paragraph.\n\nSee [x]."],
  ["脚注", "A[^1].\n\nMiddle paragraph.\n\n[^1]: Note"],
  ["多行定义标签", "见 [foo\nbar]。\n\n[foo\nbar]: https://e.com\n"],
  ["含转义右括号的定义", "见 [a]b]。\n\n[a\\]b]: https://e.com\n"],
  ["blockquote 内引用定义", "See [x].\n\nMiddle.\n\n> [x]: https://e.com\n"],
  ["列表项内脚注定义", "A[^1].\n\nMiddle.\n\n- [^1]: note\n"],
  ["脚注定义在中间", "A[^1].\n\n> [^1]: note\n\nB\n"],
  ["缩进定义行", "见 [x]。\n\n    [x]: https://e.com\n\n收尾。\n"],
  ["HTML 注释块", "<!--\n\nhello\n\n-->\n"],
  ["脚本块", "<script>\n\nhello\n\n</script>\n"],
  ["DOCTYPE 块", "<!DOCTYPE html\n\nx\n\n>\n\n<pre>\n\ncode\n\n</pre>\n"],
  ["blockquote 内 HTML 块与围栏", "> <pre>\n> ```\n> code"],
  // 退回路径若仍做围栏降级，下面这几条会与非流式不一致（审计实测）。
  ["HTML 块前的正文", "说明。\n\n<pre>\n```\ncode"],
  ["未闭合 HTML 块", "<style>\n```\nstill"],
  ["注释块内的围栏", "前文\n\n<!--\n```\nhello"],
];

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

  it("流式分块渲染与整段解析的 DOM 完全一致", () => {
    for (const [name, text] of EQUIV_SAMPLES) {
      const streamed = render(<BotMarkdown streaming text={text} />);
      const streamedHtml = streamed.container.innerHTML;
      streamed.unmount();
      const whole = render(<BotMarkdown text={text} />);
      expect(streamedHtml, name).toBe(whole.container.innerHTML);
      whole.unmount();
    }
  });

  it("含原始 HTML 或引用定义行 → 不分块，且流式与非流式逐字节一致", () => {
    for (const [name, text] of BAIL_SAMPLES) {
      // 单一扫描器判定为退回：没有稳定块，也没有围栏降级段。
      expect(splitStreamingMarkdown(text), name).toEqual({
        segments: [],
        tailMarkdown: text,
        tailPlain: null,
      });
      const streamed = render(<BotMarkdown streaming text={text} />);
      // 退回路径不做围栏降级：没有纯文本段，才可能与非流式完全一致。
      expect(
        streamed.container.querySelector(".whitespace-pre-wrap"),
        name,
      ).toBeNull();
      const streamedHtml = streamed.container.innerHTML;
      streamed.unmount();
      const whole = render(<BotMarkdown text={text} />);
      expect(streamedHtml, name).toBe(whole.container.innerHTML);
      whole.unmount();
    }
  });

  it("未闭合围栏仍按既有行为降级为纯文本", () => {
    // 这两条是刻意的已知边界：围栏在引用块里还没被容器结束判定到（行首 `>`
    // 的空引用行不关闭围栏），或围栏重新开在顶层。此时降级是既有行为，
    // 只保证「分块渲染 == 未分块的流式渲染」，不再声称与整段解析等价。
    const cases: Array<[string, string, string]> = [
      ["引用空行续围栏", "> ```\n> code\n>\n> stillfence", "stillfence"],
      ["引用后新开顶层围栏", "> ```\n> code\n\n```\nZQMARKER\n", "ZQMARKER"],
    ];
    for (const [name, text, marker] of cases) {
      const { container } = render(<BotMarkdown streaming text={text} />);
      const plain = container.querySelector(".whitespace-pre-wrap");
      expect(plain, name).not.toBeNull();
      expect(plain?.textContent, name).toContain(marker);
    }
  });

  it("引用块里未闭合的围栏：流式降级为纯文本，降级段就是 openTail", () => {
    // 设计行为：引用块里的围栏还没收到同容器的闭围栏时，流式从围栏起始行起
    // 降级为纯文本（增量优化优先），与非流式（CommonMark 里延续到文末的代码
    // 块）不同，因此这几条不进上面的严格等价样本。末尾换行不应改写这个判定
    // （末尾空串不是一行）。
    const texts = [
      "> ```\n> code\n",
      "> ```\n> code\n> more",
      "> ```\n> code\n> more\n",
    ];
    for (const text of texts) {
      const { container, unmount } = render(
        <BotMarkdown streaming text={text} />,
      );
      const plain = container.querySelector(".whitespace-pre-wrap");
      expect(plain, text).not.toBeNull();
      expect(plain?.textContent, text).toBe(splitIncompleteFence(text).openTail);
      // 降级段未被吞进代码块：围栏标记原样可见。
      expect(container.querySelector("pre"), text).toBeNull();
      expect(container.textContent, text).toContain("```");
      unmount();
    }
  });

  it("按行边界增量喂养：每一步中间态都与全新挂载的同一文本一致", () => {
    for (const [name, text] of [...EQUIV_SAMPLES, ...BAIL_SAMPLES]) {
      const lines = text.split("\n");
      const { container, rerender, unmount } = render(
        <BotMarkdown streaming text={""} />,
      );
      for (let i = 1; i <= lines.length; i += 1) {
        const prefix = lines.slice(0, i).join("\n");
        rerender(<BotMarkdown streaming text={prefix} />);
        // 把缓存累积出的 DOM 和「同一文本、全新挂载」逐字节比：新挂载没有
        // 块缓存，等价于从零渲染同一文本，一旦某块被错切、错缓存或重复入
        // 缓存，中间态就会当场暴露（而不是等到终态才对比）。
        const fresh = render(<BotMarkdown streaming text={prefix} />);
        const freshHtml = fresh.container.innerHTML;
        fresh.unmount();
        expect(container.innerHTML, `${name}@${i}: ${prefix}`).toBe(freshHtml);
      }
      // 终态另与整段解析（非流式）对比：分块路径就是整段路径的等价实现。
      const whole = render(<BotMarkdown text={text} />);
      const wholeHtml = whole.container.innerHTML;
      whole.unmount();
      expect(container.innerHTML, name).toBe(wholeHtml);
      unmount();
    }
  });

  it("流式增长期间不产生 React 告警（key / 重复内容）", () => {
    const spy = vi.spyOn(console, "error").mockImplementation(() => {});
    const lines = [
      "第一段",
      "",
      "```ts",
      "const a = 1;",
      "```",
      "",
      "- a",
      "- b",
      "",
      "| x |",
      "| --- |",
      "| 1 |",
      "",
      "收尾。",
    ];
    const { rerender, unmount } = render(<BotMarkdown streaming text={""} />);
    for (let i = 1; i <= lines.length; i += 1) {
      rerender(<BotMarkdown streaming text={lines.slice(0, i).join("\n")} />);
    }
    unmount();
    const warnings = spy.mock.calls.map((call) => String(call[0]));
    spy.mockRestore();
    expect(warnings).toEqual([]);
  });
});
