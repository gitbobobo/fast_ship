import { describe, expect, it } from "vitest";
import { splitIncompleteFence, splitStableBlocks } from "./markdown-stream";

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
    const text = ">> ```ts\ncode";
    expect(splitIncompleteFence(text)).toEqual({
      complete: "",
      openTail: ">> ```ts\ncode",
    });
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
});

describe("splitStableBlocks", () => {
  it("普通段落在空白行处切分，tail 含该空白行", () => {
    const r = splitStableBlocks("第一段\n\n第二段\n");
    expect(r.stable).toBe("第一段\n");
    expect(r.tail).toBe("\n第二段\n");
  });

  it("顶层围栏内的空白行不切分", () => {
    const r = splitStableBlocks("前文\n\n```ts\nconst a = 1;\n\nconst b = 2;\n```\n后文\n");
    expect(r.stable).toBe("前文\n");
    expect(r.tail).toBe("\n```ts\nconst a = 1;\n\nconst b = 2;\n```\n后文\n");
  });

  it("闭合围栏之后的空白行可以切分", () => {
    const r = splitStableBlocks("```ts\na\n\nb\n```\n\n后文\n");
    expect(r.stable).toBe("```ts\na\n\nb\n```\n");
    expect(r.tail).toBe("\n后文\n");
  });

  it("无序/有序/引用续行不切分：同一个列表或引用块", () => {
    expect(splitStableBlocks("- a\n\n- b\n").stable).toBe("");
    expect(splitStableBlocks("1. a\n\n2. b\n").stable).toBe("");
    expect(splitStableBlocks("> a\n\n> b\n").stable).toBe("");
  });

  it("blockquote 嵌套围栏内的空白续行同样不产生切分点", () => {
    const r = splitStableBlocks("> ```ts\n> const a = 1;\n>\n> const b = 2;\n> ```\n");
    expect(r.stable).toBe("");
  });

  it("blockquote 内围栏闭合后，容器外的空白行可以切分", () => {
    const r = splitStableBlocks("> ```ts\n> a\n> ```\n\n后文\n");
    expect(r.stable).toBe("> ```ts\n> a\n> ```\n");
    expect(r.tail).toBe("\n后文\n");
  });

  it("尾部未闭合围栏时 stable 停在围栏前", () => {
    const r = splitStableBlocks("前文\n\n```ts\ncode");
    expect(r.stable).toBe("前文\n");
    expect(r.tail).toBe("\n```ts\ncode");
  });

  it("空文本、单行、末尾空白行（下一行还没到）stable 为空", () => {
    expect(splitStableBlocks("")).toEqual({ stable: "", tail: "", segments: [] });
    expect(splitStableBlocks("单行").stable).toBe("");
    expect(splitStableBlocks("单行\n").stable).toBe("");
    expect(splitStableBlocks("段落\n\n").stable).toBe("");
  });

  it("segments 拼接等于 stable，且前缀块文本随文本增长不变", () => {
    const full = "段落一\n\n段落二\n\n```ts\ncode\n```\n\n尾段\n";
    const { stable, segments } = splitStableBlocks(full);
    expect(segments.join("")).toBe(stable);
    expect(segments).toEqual([
      "段落一\n",
      "\n段落二\n",
      "\n```ts\ncode\n```\n",
    ]);
    // 逐字增长时，已出现的块文本不得被后续文本改写（memo 跳过的前提）。
    for (let end = 1; end <= full.length; end += 1) {
      const grown = splitStableBlocks(full.slice(0, end)).segments;
      expect(grown.slice(0, Math.max(0, grown.length - 1))).toEqual(
        segments.slice(0, Math.max(0, grown.length - 1)),
      );
    }
  });

  it("下一行还在增长时不消费前面的空白行（`1` 可能长成 `1.`）", () => {
    expect(splitStableBlocks("段落\n\n1").stable).toBe("");
    expect(splitStableBlocks("段落\n\n1.\n").stable).toBe("");
    // 顶格普通文本收完才落切分点。
    expect(splitStableBlocks("段落\n\n1 x\n").stable).toBe("段落\n");
  });

  it("缩进续行不切分：松散列表项的延续", () => {
    expect(splitStableBlocks("- a\n\n  continuation\n").stable).toBe("");
  });

  it("增长前缀下 stable 单调不减且始终是全文前缀", () => {
    const full = "段落一\n\n段落二\n\n```ts\ncode\n```\n\n尾段\n";
    let previous = "";
    for (let end = 1; end <= full.length; end += 1) {
      const prefix = full.slice(0, end);
      const { stable, tail } = splitStableBlocks(prefix);
      expect(stable.startsWith(previous)).toBe(true);
      expect(prefix.startsWith(stable)).toBe(true);
      expect(tail).toBe(prefix.slice(stable.length));
      previous = stable;
    }
    expect(previous).toBe("段落一\n\n段落二\n\n```ts\ncode\n```\n");
  });

  it("带 \\r\\n 的行：偏移按原始行计，空白行仍可切分", () => {
    const r = splitStableBlocks("para\r\n\r\nsecond\r\n");
    expect(r.stable).toBe("para\r\n");
    expect(r.tail).toBe("\r\nsecond\r\n");
  });

  it("链接/脚注引用定义行不切分：拆开会让前面的引用失去定义", () => {
    expect(splitStableBlocks("见 [标注]\n\n[标注]: https://x\n").stable).toBe("");
    expect(splitStableBlocks("正文\n\n[^1]: 注解\n").stable).toBe("");
  });

  it("全文出现引用定义行就整段退回，不分块", () => {
    // 定义可以出现在文档任意位置、对全文生效（文档级作用域）：定义在后、在
    // 前、脚注定义都一律退回，不能继续声称可以按块缓存。
    const definitions = [
      "See [x].\n\nMiddle paragraph.\n\n[x]: https://example.com",
      "[x]: https://example.com\n\nMiddle paragraph.\n\nSee [x].",
      "A[^1].\n\nMiddle paragraph.\n\n[^1]: Note",
      // ≤3 空格缩进的定义行同样生效。
      "见 [x]。\n\n中间段。\n\n   [x]: https://example.com",
    ];
    for (const text of definitions) {
      expect(splitStableBlocks(text)).toEqual({
        stable: "",
        segments: [],
        tail: text,
      });
    }
  });

  it("4 空格缩进的定义行是代码块，不触发整段退回", () => {
    const r = splitStableBlocks("段落。\n\n    [x]: https://example.com\n\n收尾。\n");
    expect(r.stable).toBe("段落。\n\n    [x]: https://example.com\n");
  });

  it("HTML 块（类型 1–5）内部的空白行不是切分点", () => {
    // 这些块不被空白行打断：切开会把块内文本重新解释成普通段落。
    expect(splitStableBlocks("<!--\n\nhello\n\n-->\n").stable).toBe("");
    expect(splitStableBlocks("<script>\n\nalert(1)\n\n</script>\n").stable).toBe("");
    expect(splitStableBlocks("<PRE>\n\ncode\n\n</pre>\n").stable).toBe("");
    expect(splitStableBlocks("<?php\n\necho 1;\n\n?>\n").stable).toBe("");
    expect(splitStableBlocks("<![CDATA[\n\nx\n\n]]>\n").stable).toBe("");
    expect(splitStableBlocks("<!DOCTYPE html\n\n(x)\n\n>\n").stable).toBe("");
    // 起始行自身就含结束序列：块当场结束，后面的空白行恢复为候选。
    expect(splitStableBlocks("<!-- 注释 -->\n\n正文\n").stable).toBe(
      "<!-- 注释 -->\n",
    );
    expect(splitStableBlocks("<script>x()</script>\n\n正文\n").stable).toBe(
      "<script>x()</script>\n",
    );
    // 块结束后空白行仍是切分点。
    expect(splitStableBlocks("<!--\n\nhello\n\n-->\n\n后文\n").stable).toBe(
      "<!--\n\nhello\n\n-->\n",
    );
    // 普通标签不是类型 1–5（以空白行结束），不影响切分。
    expect(splitStableBlocks("<div>\n\n正文\n").stable).toBe("<div>\n");
  });

  it("NBSP 行不是空白行：不切分", () => {
    // CommonMark 的空白行只含空格与制表符；U+00A0 是正文。
    const r = splitStableBlocks("A\n\u00a0\nB\n");
    expect(r.stable).toBe("");
    expect(r.tail).toBe("A\n\u00a0\nB\n");
    // 只有空格/制表符的行仍是空白行。
    expect(splitStableBlocks("A\n \t \nB\n").stable).toBe("A\n");
  });
});
