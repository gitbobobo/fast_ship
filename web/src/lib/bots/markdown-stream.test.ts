import { describe, expect, it } from "vitest";
import {
  splitIncompleteFence,
  splitStreamingMarkdown,
} from "./markdown-stream";

/** 切分扫描得到的「已完成的块前缀」：segments 拼接即稳定前缀。 */
function stableOf(text: string): string {
  return splitStreamingMarkdown(text).segments.join("");
}

describe("splitIncompleteFence", () => {
  it("无围栏时整体返回，openTail 为 null", () => {
    const text = "普通文本\n\n- 列表\n- 项\n";
    expect(splitIncompleteFence(text)).toEqual({
      complete: text,
      openTail: null,
    });
  });

  it("已闭合的 ``` 围栏不算未闭合", () => {
    const text = "前文\n```ts\nconst a = 1;\n```\n后文";
    expect(splitIncompleteFence(text).openTail).toBeNull();
  });

  it("未闭合的 ``` 从围栏起始行切出 openTail", () => {
    const text = "前文\n```ts\nconst a = 1;";
    expect(splitIncompleteFence(text)).toEqual({
      complete: "前文\n",
      openTail: "```ts\nconst a = 1;",
    });
  });

  it("未闭合的 ~~~ 同样切出", () => {
    const text = "前文\n~~~\ncode";
    expect(splitIncompleteFence(text)).toEqual({
      complete: "前文\n",
      openTail: "~~~\ncode",
    });
  });

  it("``` 不能闭合 ~~~ 开的围栏（反之亦然）", () => {
    const text = "~~~\ncode\n```\nstill open";
    const r = splitIncompleteFence(text);
    expect(r.openTail).toBe("~~~\ncode\n```\nstill open");
  });

  it("闭围栏长度必须 ≥ 开围栏：```` 内 ``` 不闭合", () => {
    const text = "````\ncode\n```\nmore";
    const r = splitIncompleteFence(text);
    expect(r.openTail).toBe("````\ncode\n```\nmore");
  });

  it("更长的闭围栏可以闭合短围栏", () => {
    const text = "```\ncode\n````\n后文";
    expect(splitIncompleteFence(text).openTail).toBeNull();
  });

  it("行首缩进 ≤3 空格算围栏，4 空格不算", () => {
    const open = splitIncompleteFence("前文\n   ```js\ncode");
    expect(open.openTail).toBe("   ```js\ncode");
    const indented = splitIncompleteFence("前文\n    ```js\ncode");
    expect(indented.openTail).toBeNull();
  });

  it("闭围栏标记后只允许空白，带信息串不闭合", () => {
    const text = "```js\ncode\n``` js\nmore";
    expect(splitIncompleteFence(text).openTail).toBe("```js\ncode\n``` js\nmore");
    const closed = "```js\ncode\n```   \n后文";
    expect(splitIncompleteFence(closed).openTail).toBeNull();
  });

  it("反引号开围栏的信息串含反引号时整行不是围栏", () => {
    const text = "前文\n``` `a` ```\n后文";
    expect(splitIncompleteFence(text).openTail).toBeNull();
  });

  it("闭合后再开新围栏，切最后一个未闭合的", () => {
    const text = "```\na\n```\n中间\n```\nb";
    expect(splitIncompleteFence(text)).toEqual({
      complete: "```\na\n```\n中间\n",
      openTail: "```\nb",
    });
  });

  it("行内出现的围栏标记不当作围栏行", () => {
    const text = "正文里有 ``` 但不是行首围栏";
    expect(splitIncompleteFence(text).openTail).toBeNull();
  });

  it("blockquote 内未闭合围栏从容器行切出 openTail", () => {
    const text = "前文\n> ```ts\n> const a = 1;";
    expect(splitIncompleteFence(text)).toEqual({
      complete: "前文\n",
      openTail: "> ```ts\n> const a = 1;",
    });
  });

  it("blockquote 内已闭合围栏不算未闭合", () => {
    const text = "> ```ts\n> const a = 1;\n> ```";
    expect(splitIncompleteFence(text).openTail).toBeNull();
  });

  it("嵌套 blockquote（> >）内未闭合围栏可识别", () => {
    const text = "前文\n> > ```ts\n> > const a = 1;";
    expect(splitIncompleteFence(text)).toEqual({
      complete: "前文\n",
      openTail: "> > ```ts\n> > const a = 1;",
    });
  });

  it("紧凑嵌套 blockquote（>>）内的围栏同样可识别", () => {
    const text = ">> ```ts\n>> code";
    expect(splitIncompleteFence(text)).toEqual({
      complete: "",
      openTail: text,
    });
    // 下一行掉出两层容器：围栏随容器一起结束（末行 `code` 是顶层段落）。
    expect(splitIncompleteFence(">> ```ts\ncode").openTail).toBeNull();
  });

  it("普通引用文本不误判为围栏", () => {
    const text = "> 普通引用文本\n> 第二行引用";
    expect(splitIncompleteFence(text)).toEqual({
      complete: text,
      openTail: null,
    });
  });

  it("顶层围栏不会被内容里的容器前缀行误闭合", () => {
    const text = "```\n> ```ts\n> x\n> ```\n后文";
    expect(splitIncompleteFence(text)).toEqual({
      complete: "",
      openTail: text,
    });
  });

  it("blockquote 内围栏只被同容器深度的行闭合", () => {
    const text = "> ```ts\n> > ```\n> x";
    expect(splitIncompleteFence(text)).toEqual({
      complete: "",
      openTail: text,
    });
  });

  it("容器深度下降时围栏随之结束（裸空行结束引用块里的围栏）", () => {
    const text = "> ```\n> code\n\nZQMARKER";
    expect(splitIncompleteFence(text).openTail).toBeNull();
    // 行首 `>` 的空引用行深度不变，引用还在继续，围栏不结束。
    expect(splitIncompleteFence("> ```\n> code\n>\n> still").openTail).toBe(
      "> ```\n> code\n>\n> still",
    );
  });

  it("末尾换行不产生额外的空行：引用块里的围栏不因它关闭", () => {
    // `"> ```\n> code\n"` 在 CommonMark 里只有两行，末尾那个空串不是一行；
    // 把它当真实空行（深度 0）会关闭引用块里仍在增大的围栏。
    expect(splitIncompleteFence("> ```\n> code\n").openTail).toBe(
      "> ```\n> code\n",
    );
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
      expect(splitIncompleteFence(text).openTail, text).toBe(text);
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

describe("tailPlain 与 splitIncompleteFence 一致", () => {
  // 未闭合围栏起点是同一份扫描结果的两个出口：分块用的降级段与既有
  // splitIncompleteFence 的 openTail 必须逐字节相同，否则两处围栏状态又会分叉。
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
    "- ```\n  code\n\nZQMARKER",
    "> ```\n> code\n\nZQMARKER",
    "> ```\n> code\n",
    "> ```\n> code\n> more",
  ];

  it("非退回样本上两处出口逐字节一致", () => {
    for (const text of samples) {
      const scan = splitStreamingMarkdown(text);
      const fence = splitIncompleteFence(text);
      expect(scan.tailPlain, text).toBe(fence.openTail);
      expect(scan.segments.join("") + scan.tailMarkdown, text).toBe(
        fence.complete,
      );
      expect(
        scan.segments.join("") + scan.tailMarkdown + (scan.tailPlain ?? ""),
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

  it("列表项缩进内的围栏不在扫描范围（既有边界）：不降级", () => {
    expect(splitStreamingMarkdown("- ```\n  code\n\nZQMARKER\n")).toEqual({
      segments: ["- ```\n  code\n"],
      tailMarkdown: "\nZQMARKER\n",
      tailPlain: null,
    });
  });
});
