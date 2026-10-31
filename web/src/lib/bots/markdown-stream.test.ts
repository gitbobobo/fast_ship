import { describe, expect, it } from "vitest";
import { splitStreamingMarkdown } from "./markdown-stream";

/** 切分扫描得到的「已完成的块前缀」：segments 拼接即稳定前缀。 */
function stableOf(text: string): string {
  return splitStreamingMarkdown(text).segments.join("");
}

describe("围栏降级：tailPlain 直接断言", () => {
  // 围栏降级只由 splitStreamingMarkdown 的 tailPlain 承担：最后一个未闭合围栏
  // 起始行起的原文按纯文本展示，闭合后自动升级为代码块。这里逐条钉住围栏状态
  // 与降级边界（这些样本原先断言的是已删除的围栏扫描器导出）。
  it("顶格未闭合围栏：围栏起始行起降级为纯文本", () => {
    expect(splitStreamingMarkdown("前文\n```ts\nconst a = 1;")).toEqual({
      segments: [],
      tailMarkdown: "前文\n",
      tailPlain: "```ts\nconst a = 1;",
    });
    expect(splitStreamingMarkdown("前文\n~~~\ncode")).toEqual({
      segments: [],
      tailMarkdown: "前文\n",
      tailPlain: "~~~\ncode",
    });
  });

  it("闭围栏规则：同字符、长度 ≥ 开围栏、标记后只允许空白", () => {
    // ``` 不能闭合 ~~~ 开的围栏，反之亦然。
    expect(
      splitStreamingMarkdown("~~~\ncode\n```\nstill").tailPlain,
    ).toBe("~~~\ncode\n```\nstill");
    // 闭围栏长度必须 ≥ 开围栏：```` 内 ``` 不闭合。
    expect(
      splitStreamingMarkdown("````\ncode\n```\nmore").tailPlain,
    ).toBe("````\ncode\n```\nmore");
    // 更长的闭围栏可以闭合短围栏。
    expect(splitStreamingMarkdown("```\ncode\n````\n后文").tailPlain).toBeNull();
    // 闭围栏标记后只允许空白：带信息串不闭合，带空白闭合。
    expect(
      splitStreamingMarkdown("```js\ncode\n``` js\nmore").tailPlain,
    ).toBe("```js\ncode\n``` js\nmore");
    expect(
      splitStreamingMarkdown("```js\ncode\n```   \n后文").tailPlain,
    ).toBeNull();
  });

  it("闭合后再开新围栏：降级从最后一个未闭合围栏起", () => {
    expect(splitStreamingMarkdown("```\na\n```\n中间\n```\nb")).toEqual({
      segments: [],
      tailMarkdown: "```\na\n```\n中间\n",
      tailPlain: "```\nb",
    });
  });

  it("围栏信息串含反引号不算围栏：不降级，整段留在尾部", () => {
    const text = "前文\n``` `a` ```\n后文";
    expect(splitStreamingMarkdown(text)).toEqual({
      segments: [],
      tailMarkdown: text,
      tailPlain: null,
    });
  });

  it("缩进 4 空格不是围栏（是缩进代码块）：不降级", () => {
    const text = "前文\n    ```js\ncode";
    expect(splitStreamingMarkdown(text)).toEqual({
      segments: [],
      tailMarkdown: text,
      tailPlain: null,
    });
  });

  it("blockquote 内围栏按容器深度配对：深度不同不闭合", () => {
    const text = "> ```ts\n> > ```\n> x";
    expect(splitStreamingMarkdown(text).tailPlain).toBe(text);
    expect(splitStreamingMarkdown("> ```ts\n> a\n> ```").tailPlain).toBeNull();
    // 顶层围栏不会被内容里的 `> ``` ` 行误闭合。
    const nested = "```\n> ```ts\n> x\n> ```\n后文";
    expect(splitStreamingMarkdown(nested).tailPlain).toBe(nested);
  });

  it("裸空行结束引用块里的围栏：后面的正文不再降级", () => {
    expect(
      splitStreamingMarkdown("> ```\n> code\n\nZQMARKER").tailPlain,
    ).toBeNull();
  });

  it("末尾换行不产生额外空行：引用块里的围栏保持降级", () => {
    for (const text of ["> ```\n> code\n", "> ```\n> code\n> more\n"]) {
      expect(splitStreamingMarkdown(text).tailPlain, text).toBe(text);
    }
  });
});

describe("splitStreamingMarkdown 切分点", () => {
  it("普通段落在空白行处切分，tailMarkdown 含该空白行", () => {
    const r = splitStreamingMarkdown("第一段\n\n第二段\n");
    expect(stableOf("第一段\n\n第二段\n")).toBe("第一段\n");
    expect(r.tailMarkdown).toBe("\n第二段\n");
    expect(r.tailPlain).toBeNull();
  });

  it("顶层围栏内的空白行不切分", () => {
    const r = splitStreamingMarkdown(
      "前文\n\n```ts\nconst a = 1;\n\nconst b = 2;\n```\n后文\n",
    );
    expect(r.segments).toEqual(["前文\n"]);
    expect(r.tailMarkdown).toBe(
      "\n```ts\nconst a = 1;\n\nconst b = 2;\n```\n后文\n",
    );
    expect(r.tailPlain).toBeNull();
  });

  it("闭合围栏之后的空白行可以切分", () => {
    const r = splitStreamingMarkdown("```ts\na\n\nb\n```\n\n后文\n");
    expect(r.segments).toEqual(["```ts\na\n\nb\n```\n"]);
    expect(r.tailMarkdown).toBe("\n后文\n");
    expect(r.tailPlain).toBeNull();
  });

  it("无序/有序/引用续行不切分：同一个列表或引用块", () => {
    expect(stableOf("- a\n\n- b\n")).toBe("");
    expect(stableOf("1. a\n\n2. b\n")).toBe("");
    expect(stableOf("> a\n\n> b\n")).toBe("");
  });

  it("blockquote 嵌套围栏内的空白续行同样不产生切分点", () => {
    const r = splitStreamingMarkdown(
      "> ```ts\n> const a = 1;\n>\n> const b = 2;\n> ```\n",
    );
    expect(r.segments).toEqual([]);
    expect(r.tailPlain).toBeNull();
  });

  it("blockquote 内围栏闭合后，容器外的空白行可以切分", () => {
    const r = splitStreamingMarkdown("> ```ts\n> a\n> ```\n\n后文\n");
    expect(r.segments).toEqual(["> ```ts\n> a\n> ```\n"]);
    expect(r.tailMarkdown).toBe("\n后文\n");
    expect(r.tailPlain).toBeNull();
  });

  it("尾部未闭合围栏：稳定前缀停在围栏前，围栏段降级为纯文本", () => {
    const r = splitStreamingMarkdown("前文\n\n```ts\ncode");
    expect(stableOf("前文\n\n```ts\ncode")).toBe("前文\n");
    expect(r.tailMarkdown).toBe("\n");
    expect(r.tailPlain).toBe("```ts\ncode");
  });

  it("空文本、单行、末尾空白行（下一行还没到）稳定前缀为空", () => {
    expect(splitStreamingMarkdown("")).toEqual({
      segments: [],
      tailMarkdown: "",
      tailPlain: null,
    });
    expect(stableOf("单行")).toBe("");
    expect(stableOf("单行\n")).toBe("");
    expect(stableOf("段落\n\n")).toBe("");
  });

  it("segments 拼接等于稳定前缀，且前缀块文本随文本增长不变", () => {
    const full = "段落一\n\n段落二\n\n```ts\ncode\n```\n\n尾段\n";
    const { segments } = splitStreamingMarkdown(full);
    expect(segments).toEqual([
      "段落一\n",
      "\n段落二\n",
      "\n```ts\ncode\n```\n",
    ]);
    // 逐字增长时，已出现的块文本不得被后续文本改写（memo 跳过的前提）。
    for (let end = 1; end <= full.length; end += 1) {
      const grown = splitStreamingMarkdown(full.slice(0, end)).segments;
      expect(grown.slice(0, Math.max(0, grown.length - 1))).toEqual(
        segments.slice(0, Math.max(0, grown.length - 1)),
      );
    }
  });

  it("下一行还在增长时不消费前面的空白行（`1` 可能长成 `1.`）", () => {
    expect(stableOf("段落\n\n1")).toBe("");
    expect(stableOf("段落\n\n1.\n")).toBe("");
    // 顶格普通文本收完才落切分点。
    expect(stableOf("段落\n\n1 x\n")).toBe("段落\n");
  });

  it("缩进续行不切分：松散列表项的延续", () => {
    expect(stableOf("- a\n\n  continuation\n")).toBe("");
  });

  it("增长前缀下稳定前缀单调不减且始终是全文前缀", () => {
    const full = "段落一\n\n段落二\n\n```ts\ncode\n```\n\n尾段\n";
    let previous = "";
    for (let end = 1; end <= full.length; end += 1) {
      const prefix = full.slice(0, end);
      const { segments, tailMarkdown, tailPlain } =
        splitStreamingMarkdown(prefix);
      const stable = segments.join("");
      expect(stable.startsWith(previous)).toBe(true);
      expect(prefix.startsWith(stable)).toBe(true);
      expect(stable + tailMarkdown + (tailPlain ?? "")).toBe(prefix);
      previous = stable;
    }
    expect(previous).toBe("段落一\n\n段落二\n\n```ts\ncode\n```\n");
  });

  it("带 \\r\\n 的行：偏移按原始行计，空白行仍可切分", () => {
    const r = splitStreamingMarkdown("para\r\n\r\nsecond\r\n");
    expect(r.segments).toEqual(["para\r\n"]);
    expect(r.tailMarkdown).toBe("\r\nsecond\r\n");
  });

  it("NBSP 行不是空白行：不切分", () => {
    // CommonMark 的空白行只含空格与制表符；U+00A0 是正文。
    const r = splitStreamingMarkdown("A\n\u00a0\nB\n");
    expect(r.segments).toEqual([]);
    // 只有空格/制表符的行仍是空白行。
    expect(stableOf("A\n \t \nB\n")).toBe("A\n");
  });
});

describe("缩进代码块里的空白行不切分", () => {
  it("空白行自己缩进 ≥4 时不断开缩进代码块", () => {
    // `"     code\n     \npara\n"` 整段解析是 `<pre><code>code\n \n</code></pre>`
    // 加 `<p>para</p>`：那个空格行去掉 4 空格缩进后仍是空格，是代码内容。
    // 在这里切分会让代码块丢掉这一行。
    const r = splitStreamingMarkdown("     code\n     \npara\n");
    expect(r.segments).toEqual([]);
    expect(r.tailMarkdown).toBe("     code\n     \npara\n");
    expect(r.tailPlain).toBeNull();
  });

  it("空白行不结束缩进代码块：0 空格的空行也在代码块里", () => {
    // `"    code\n\n    \npara\n"` 整段解析是一个代码块（`code` 后跟两个空行）
    // 加 `<p>para</p>`：缩进代码块里的无缩进空白行同样是代码内容（只是空行），
    // 从这里切开会把中间那行 4 空格剥出代码块。
    const spaced = splitStreamingMarkdown("    code\n\n    \npara\n");
    expect(spaced.segments).toEqual([]);
    expect(spaced.tailMarkdown).toBe("    code\n\n    \npara\n");
    expect(spaced.tailPlain).toBeNull();
  });

  it("tab 缩进算 4 列：tab 代码行也能识别缩进代码块", () => {
    // CommonMark 的 tab stop 是 4 列，`\t` 与 4 空格同义。按 `/^ {4}/` 判定时
    // tab 缩进的代码行认不出来，后面那个空行就会被切出去。
    const tabbed = splitStreamingMarkdown("\tcode\n     \npara\n");
    expect(tabbed.segments).toEqual([]);
    expect(tabbed.tailMarkdown).toBe("\tcode\n     \npara\n");
    expect(tabbed.tailPlain).toBeNull();
  });

  it("缩进代码块之后紧邻的空行也少切一刀（刻意保守）", () => {
    // 空白行不结束缩进代码块，所以缩进块后面的空行一律不作候选，直到出现
    // 缩进 <4 的非空行。这两组以前会切一刀，现在整段留在尾部：代价只是少了
    // 一点增量优化，换来的是缩进代码块里的空行不会被切出去。
    for (const text of ["     code\n\npara\n", "     code\n   \npara\n"]) {
      expect(splitStreamingMarkdown(text), text).toEqual({
        segments: [],
        tailMarkdown: text,
        tailPlain: null,
      });
    }
  });

  it("普通段落之间的空白行仍可切分", () => {
    // 防过度保守：缩进代码块规则不向外扩散，顶格段落与 3 空格缩进的空白行
    // 照旧切分。
    expect(stableOf("甲段落\n\n乙段落\n")).toBe("甲段落\n");
    expect(stableOf("A\n   \nB\n")).toBe("A\n");
    // 空白行缩进 ≥4 但前面没有缩进代码块：不是代码内容，仍然切分。
    expect(stableOf("A\n     \nB\n")).toBe("A\n");
  });
});

describe("splitStreamingMarkdown 退回（不分块）", () => {
  /** 退回的完整口径：不分块、整段交给 Markdown、也不做围栏降级。 */
  function expectBail(text: string) {
    expect(splitStreamingMarkdown(text), text).toEqual({
      segments: [],
      tailMarkdown: text,
      tailPlain: null,
    });
  }

  it("行首 `<`（任意 HTML 块）整段退回", () => {
    // HTML 块类型 1–7 都以行首 `<` 开始：块不被空白行打断、块内是原样文本，
    // 切成两半会让块内文本被重新解释成普通段落，所以不去识别结束序列。
    expectBail("<!--\n\nhello\n\n-->\n");
    expectBail("<script>\n\nhello\n\n</script>\n");
    expectBail("<pre>\n\ncode\n\n</pre>\n");
    expectBail("<div>\n\n正文\n");
    expectBail("<!DOCTYPE html\n\nx\n\n>\n");
    expectBail("<style>\n```\nstill");
    expectBail("说明。\n\n<pre>\n```\ncode");
    expectBail("前文\n\n<!--\n```\nhello");
    expectBail("> <pre>\n> ```\n> code");
    // 容器里的 HTML 同样退回（剥掉容器前缀再判定）。
    expectBail("段落\n\n- <div>\n\n正文\n");
  });

  it("行首 `[`（链接/脚注引用定义）整段退回", () => {
    // 定义是文档级作用域：切到后面的块里，前面的引用就退化成裸文本、脚注区
    // 也会错位。定义标签可跨行、可含转义右括号，语法形状追不完，行首是 `[`
    // 就保守退回。
    expectBail("See [x].\n\nMiddle paragraph.\n\n[x]: https://example.com");
    expectBail("[x]: https://example.com\n\nMiddle paragraph.\n\nSee [x].");
    expectBail("A[^1].\n\nMiddle paragraph.\n\n[^1]: Note");
    expectBail("见 [x]。\n\n中间段。\n\n   [x]: https://example.com");
    // 块容器里的定义同样是文档级作用域。
    expectBail("See [x].\n\nMiddle.\n\n> [x]: https://e.com\n");
    expectBail("A[^1].\n\nMiddle.\n\n- [^1]: note\n");
    expectBail("A[^1].\n\n> [^1]: note\n\nB\n");
    expectBail("见 [x]。\n\n> > [x]: https://e.com\n");
    expectBail("见 [x]。\n\n1. [x]: https://e.com\n");
    expectBail("见 [x]。\n\n- > [x]: https://e.com\n");
    // 顶层 4 空格缩进（代码块）里的同名行也命中：刻意保守，只损失增量。
    expectBail("见 [x]。\n\n    [x]: https://e.com\n");
    // 定义标签可跨行 / 含转义右括号：正则追不完的形状一样退回。
    expectBail("见 [foo\nbar]。\n\n[foo\nbar]: https://e.com\n");
    expectBail("见 [a]b]。\n\n[a\\]b]: https://e.com\n");
  });

  it("正文里的 `[x]: y` 片段不触发退回", () => {
    // 前缀不是空白/容器标记的行剥不动，`see [x]: y` 仍只是普通段落。
    expect(stableOf("see [x]: y\n\n正文\n")).toBe("see [x]: y\n");
    expect(stableOf("- see [x]: y\n\n正文\n")).toBe("- see [x]: y\n");
  });

  it("任务列表项不触发退回：`[ ]`/`[x]` 是正文不是定义", () => {
    // 任务列表剥掉列表标记后以 `[` 开头，但 `]` 后是空白/行尾——这是本产品很
    // 常见的回复形态，整段退回会白白丢掉增量优化。
    const tasks = "## 待办\n\n- [ ] 修正标题\n- [x] 更新描述\n\n收尾段落。\n";
    const r = splitStreamingMarkdown(tasks);
    expect(r.segments.length).toBeGreaterThan(0);
    expect(r.segments).toEqual(["## 待办\n\n- [ ] 修正标题\n- [x] 更新描述\n"]);
    expect(r.tailPlain).toBeNull();
    // 有序列表的任务项、引用块里的任务项、大写 `[X]` 同样豁免。
    expect(stableOf("待办\n\n1. [x] 改标题\n\n收尾。\n")).toBe(
      "待办\n\n1. [x] 改标题\n",
    );
    expect(stableOf("待办\n\n> - [X] 改标题\n\n收尾。\n")).toBe(
      "待办\n\n> - [X] 改标题\n",
    );
  });

  it("`- [x]: url` 是真定义，仍整段退回", () => {
    // 任务项要求 `]` 后是空白或行尾；定义后面紧跟 `:`。两者只差一个字符，
    // 豁免不能宽到把定义放过去（定义是文档级作用域，切错会让引用退化成裸文本）。
    expectBail("- [x]: url\n\nSee [x].\n");
    expectBail("> - [x]: url\n\nSee [x].\n");
    expectBail("1. [x]: url\n\nSee [x].\n");
  });

  it("围栏开在列表项里时整段退回", () => {
    // 列表项里围栏的内容列取决于列表标记宽度，扫描器不建模它：`1. ``` ` 会被
    // 当成一个已完成的空代码块切出去，与整段解析（代码块里有一个 tab 缩进行）
    // 的 DOM 不一致。
    expectBail("1. ```\n\t\n正文\n");
    expectBail("- ```\n  code\n\nZQMARKER\n");
    expectBail("前提\n\n1. ~~~\ncode\n");
    expectBail("> 1. ```\n> code\n");
  });

  it("缩进 1–3 的围栏行整段退回：可能属于列表项内容", () => {
    // 围栏只认剥掉 blockquote 标记后的顶格：缩进 1–3 的围栏行可能开在列表项
    // 内容里（`- 运行：\n  ```bash`），内容列取决于列表标记宽度，列表容器不
    // 建模。代价是「顶层但缩进 2/3」的围栏也失去增量优化。
    expectBail("  ```\n  code\n  ```\n\n后文\n");
    expectBail("- 运行：\n  ```bash\n  pnpm test\n```\n\n后文一段。\n");
    expectBail(">    ```\n>    code\n>    ```\n\n后文\n");
  });

  it("裸 CR（不跟 `\\n`）整段退回", () => {
    // CommonMark 把裸 `\r` 也当换行，而扫描器按 `\n` 切行：HTML 块起点、引用
    // 定义、围栏都能藏在一行中间，绕过行首退回判定。
    expectBail("prefix\r<!--\n\ninside\n\n-->\n\nEnd\n");
    expectBail("See [x].\n\nMiddle\r\r[x]: https://e.com\n\nEnd\n");
    expectBail("```\rcode\n\npara\n```\n\nend\n");
  });

  it("顶格围栏照常切分（缩进退回的对照）", () => {
    const fence = splitStreamingMarkdown("```ts\ncode\n```\n\n后文\n");
    expect(fence.segments).toEqual(["```ts\ncode\n```\n"]);
    expect(fence.tailMarkdown).toBe("\n后文\n");
    expect(fence.tailPlain).toBeNull();
    // 列表之后的顶格围栏同样是语义独立的新块。
    const afterList = splitStreamingMarkdown(
      "- 运行：\n\n```bash\ncode\n```\n\n后文\n",
    );
    expect(afterList.segments).toEqual([
      "- 运行：\n",
      "\n```bash\ncode\n```\n",
    ]);
    expect(afterList.tailPlain).toBeNull();
  });

  it("围栏内的 `<`/`[` 行不触发退回：代码类回复不该失去增量优化", () => {
    const text = "```tsx\n<div>\n[x]: url\n```\n\n后文\n";
    const r = splitStreamingMarkdown(text);
    expect(r.segments).toEqual(["```tsx\n<div>\n[x]: url\n```\n"]);
    expect(r.tailPlain).toBeNull();
    const blockquote = splitStreamingMarkdown(
      "> ```\n> <div>\n> [x]: url\n> ```\n\n后文\n",
    );
    expect(blockquote.segments).toEqual([
      "> ```\n> <div>\n> [x]: url\n> ```\n",
    ]);
  });
});

describe("末尾换行与引围栏降级", () => {
  // 同一段引用块里的代码块：末尾换行不应让降级在「代码块」与「纯文本」之间
  // 来回跳。围栏没收到同容器的闭围栏时，三个前缀都必须降级。
  it("末尾换行不开关降级：三个前缀都停在围栏起始行", () => {
    const texts = [
      "> ```\n> code\n",
      "> ```\n> code\n> more",
      "> ```\n> code\n> more\n",
    ];
    for (const text of texts) {
      const r = splitStreamingMarkdown(text);
      // 围栏在首行：稳定前缀与尾部 Markdown 都是空，降级段是全文。
      expect(r.segments, text).toEqual([]);
      expect(r.tailMarkdown, text).toBe("");
      expect(r.tailPlain, text).toBe(text);
    }
  });

  it("按行增长时降级不来回跳：闭围栏出现前一直是纯文本", () => {
    // 闭围栏带 `>` 标记才属于引用块（同容器深度）；不带标记的行是掉出容器，
    // 引用与它的围栏一起结束，那一行按顶层开围栏算。
    const text = "> ```\n> code\n> 第一行\n> ```\n\n后文\n";
    const lines = text.split("\n");
    for (let i = 1; i <= lines.length; i += 1) {
      const prefix = lines.slice(0, i).join("\n");
      // 第 4 行是闭围栏；它一出现（即使还没收完换行）围栏就结束。
      const closed = i >= 4;
      const { tailPlain } = splitStreamingMarkdown(prefix);
      expect(tailPlain === null, prefix).toBe(closed);
      if (!closed) expect(tailPlain, prefix).toBe(prefix);
    }
  });
});

describe("tailPlain 与切分点覆盖全文", () => {
  // 三段拼回必须逐字节等于全文：漏掉一段就是渲染丢内容。围栏状态只有
  // scanSource 一份，切分点与降级段不会得出相反结论。
  const samples = [
    "普通文本\n\n- 列表\n- 项\n",
    "前文\n```ts\nconst a = 1;",
    "前文\n~~~\ncode",
    "```\na\n```\n中间\n```\nb",
    "前文\n> ```ts\n> const a = 1;",
    "> ```ts\n> a\n> ```\n\n后文\n",
    "前文\n\n```",
    "前文\r\n```\r\ncode",
    "> > ```ts\n> > const a = 1;",
    "> ```\n> code\n\nZQMARKER",
    "> ```\n> code\n",
    "> ```\n> code\n> more",
  ];

  it("非退回样本上三段拼回逐字节等于全文", () => {
    for (const text of samples) {
      const { segments, tailMarkdown, tailPlain } = splitStreamingMarkdown(text);
      expect(
        segments.join("") + tailMarkdown + (tailPlain ?? ""),
        text,
      ).toBe(text);
    }
  });
});

describe("围栏在容器深度下降时结束", () => {
  // 围栏属于引用块：引用结束（行容器深度下降）时围栏随之结束，后续行不再算进
  // 代码块。把这一列逐条钉住，降级扫描才不会把后面的正文吞成纯文本。
  it("裸空行结束引用块里的围栏：后面的正文不再降级", () => {
    // 末行还没收完，切分点要等它定型；但围栏已经结束，不再降级。
    expect(splitStreamingMarkdown("> ```\n> code\n\nZQMARKER")).toEqual({
      segments: [],
      tailMarkdown: "> ```\n> code\n\nZQMARKER",
      tailPlain: null,
    });
    // 末行收完后落切分点：引用（含代码块）进稳定前缀，ZQMARKER 走尾部。
    expect(splitStreamingMarkdown("> ```\n> code\n\nZQMARKER\n")).toEqual({
      segments: ["> ```\n> code\n"],
      tailMarkdown: "\nZQMARKER\n",
      tailPlain: null,
    });
  });

  it("无空行的惰性续行同样结束围栏", () => {
    expect(splitStreamingMarkdown("> ```\n> code\nZQMARKER")).toEqual({
      segments: [],
      tailMarkdown: "> ```\n> code\nZQMARKER",
      tailPlain: null,
    });
  });

  it("围栏行后立刻掉出容器：代码块为空，后续是两段段落", () => {
    const r = splitStreamingMarkdown("> ```\ncode\n\nZQMARKER\n");
    expect(r).toEqual({
      segments: ["> ```\ncode\n"],
      tailMarkdown: "\nZQMARKER\n",
      tailPlain: null,
    });
    // 引用里的代码块为空，`code` 是顶层段落（与整段解析一致）。
    expect(r.tailPlain).toBeNull();
  });

  it("下一个引用不是同一个容器：里面的内容不算进代码块", () => {
    const text = "> ```\n> code\n\n> stillfence";
    expect(splitStreamingMarkdown(text)).toEqual({
      segments: [],
      tailMarkdown: text,
      tailPlain: null,
    });
    expect(splitStreamingMarkdown(text + "\n")).toEqual({
      segments: [],
      tailMarkdown: text + "\n",
      tailPlain: null,
    });
  });

  it("行首 `>` 的空引用行不结束围栏：仍是未闭合降级", () => {
    const text = "> ```\n> code\n>\n> stillfence";
    expect(splitStreamingMarkdown(text)).toEqual({
      segments: [],
      tailMarkdown: "",
      tailPlain: text,
    });
  });

  it("引用里的围栏结束后新开顶层围栏：后一段进降级", () => {
    expect(splitStreamingMarkdown("> ```\n> code\n\n```\nZQMARKER")).toEqual({
      segments: ["> ```\n> code\n"],
      tailMarkdown: "\n",
      tailPlain: "```\nZQMARKER",
    });
    expect(splitStreamingMarkdown("> ```\n> code\n\n```\nZQMARKER\n")).toEqual({
      segments: ["> ```\n> code\n"],
      tailMarkdown: "\n",
      tailPlain: "```\nZQMARKER\n",
    });
  });
});
