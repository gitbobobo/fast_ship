import { describe, expect, it } from "vitest";
import { splitIncompleteFence } from "./markdown-stream";

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
});
