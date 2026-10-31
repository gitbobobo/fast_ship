import { render } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";
import { BotMarkdown } from "./bot-markdown";

// 统计 ReactMarkdown 的调用次数与入参：分块缓存的目的是让已稳定的块不再
// 重新解析，这条断言直接锁住「整段回复的 Markdown 解析总量随长度线性增长，
// 而不是每帧重解析全文」。注意它只锁解析量：每帧仍有一次对累计全文的逐行
// 扫描（splitStreamingMarkdown 内的 scanSource，不解析 Markdown）。
// mock 工厂里不用 JSX（工厂被提升到 import 之前），改用 createElement。
const parses = vi.hoisted(() => ({ texts: [] as string[] }));

vi.mock("react-markdown", async (importOriginal) => {
  const actual = await importOriginal<typeof import("react-markdown")>();
  const { createElement } = await import("react");
  // 转发给真实组件时不重声明它的 Options 类型：这里只关心 children（被解析
  // 的 Markdown 文本），其余 props 原样透传。
  const Real = actual.default as unknown as (props: {
    children?: string;
  }) => ReactNode;
  function Counting(props: { children?: ReactNode }) {
    const children = String(props.children ?? "");
    parses.texts.push(children);
    return createElement(Real, { ...props, children });
  }
  return { ...actual, default: Counting };
});

const BLOCKS = [
  "第一块正文。",
  "第二块正文。",
  "第三块正文。",
  "第四块正文。",
  "第五块正文。",
];

describe("BotMarkdown 增量解析", () => {
  it("稳定块不再随全文增长被重复解析，单帧的 Markdown 解析长度与全文长度无关", () => {
    const { rerender, unmount } = render(<BotMarkdown streaming text="" />);
    const parsedFirstBlockAfterEachBlock: number[] = [];
    let text = "";
    let frames = 0;
    for (const block of BLOCKS) {
      // 逐字追加，模拟流式增量。
      for (const char of block + "\n\n") {
        text += char;
        frames += 1;
        rerender(<BotMarkdown streaming text={text} />);
      }
      parsedFirstBlockAfterEachBlock.push(
        parses.texts.filter((value) => value.includes("第一块正文")).length,
      );
    }
    unmount();

    // 第一块只在「它自己 + 紧随其后的那一块」还在尾部时被解析；第三块出现
    // 之后再加多少内容都不会让它再进解析器——解析总次数不随全文变长增长。
    expect(parsedFirstBlockAfterEachBlock[2]).toBe(
      parsedFirstBlockAfterEachBlock[4],
    );
    // 任何一帧的解析内容都不会同时含「第一块」与「第三块」。
    expect(
      parses.texts.filter(
        (value) =>
          value.includes("第一块正文") && value.includes("第三块正文"),
      ),
    ).toHaveLength(0);
    // 单帧的 Markdown 解析文本长度上界 ≈ 两个块，与全文长度（五个块）无关；
    // 全文长度只影响每帧那一次逐行扫描。
    const longest = Math.max(...parses.texts.map((value) => value.length));
    expect(longest).toBeLessThanOrEqual("第二块正文。\n\n第三块正文。\n".length);
    // 每帧至多一次尾部解析，加上每块转为稳定时的一次。
    expect(parses.texts.length).toBeLessThanOrEqual(frames + BLOCKS.length);
  });
});
