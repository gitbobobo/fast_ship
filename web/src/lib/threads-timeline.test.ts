import { describe, expect, it } from "vitest";
import type { ThreadItem } from "@/lib/api/threads";
import {
  commandProgram,
  groupTimeline,
  summarizeGroup,
} from "./threads-timeline";

function item(over: Partial<ThreadItem> & Pick<ThreadItem, "id" | "type">): ThreadItem {
  return { text: "", summary: "", ...over };
}

describe("groupTimeline", () => {
  it("merges consecutive foldable items and keeps other items in place", () => {
    const entries = groupTimeline([
      item({ id: "u", type: "user_message" }),
      item({ id: "c1", type: "command_execution" }),
      item({ id: "r1", type: "reasoning" }),
      item({ id: "a", type: "assistant_message" }),
      item({ id: "d1", type: "dynamic_tool" }),
      item({ id: "e", type: "error" }),
      item({ id: "f1", type: "file_change" }),
      item({ id: "f2", type: "subagent" }),
    ]);
    expect(
      entries.map((e) =>
        e.kind === "item" ? e.item.id : `${e.id}:${e.items.map((i) => i.id).join(",")}`,
      ),
    ).toEqual(["u", "c1:c1,r1", "a", "d1:d1", "e", "f1:f1,f2"]);
  });

  it("groups even a single foldable item and returns nothing for no items", () => {
    expect(groupTimeline([])).toEqual([]);
    expect(groupTimeline([item({ id: "c", type: "command_execution" })])).toEqual([
      { kind: "group", id: "c", items: [expect.objectContaining({ id: "c" })] },
    ]);
  });

  it("does not merge across approval or input requests", () => {
    const entries = groupTimeline([
      item({ id: "c1", type: "command_execution" }),
      item({ id: "ap", type: "approval_request" }),
      item({ id: "c2", type: "command_execution" }),
    ]);
    expect(entries.map((e) => e.kind)).toEqual(["group", "item", "group"]);
  });

  it("keeps a group's id stable when items are appended", () => {
    const base = [item({ id: "c1", type: "command_execution" })];
    const next = [...base, item({ id: "c2", type: "dynamic_tool" })];
    expect(groupTimeline(base)[0]).toMatchObject({ id: "c1" });
    expect(groupTimeline(next)[0]).toMatchObject({ id: "c1" });
  });
});

describe("summarizeGroup", () => {
  const done = { status: "completed" };

  it("counts by type in first-seen order joined with 、", () => {
    const { text, active } = summarizeGroup([
      item({ id: "1", type: "dynamic_tool", ...done }),
      item({ id: "2", type: "command_execution", ...done }),
      item({ id: "3", type: "dynamic_tool", ...done }),
      item({ id: "4", type: "file_change", ...done }),
      item({ id: "5", type: "reasoning", ...done }),
      item({ id: "6", type: "subagent", ...done }),
    ]);
    expect(text).toBe("使用 2 个工具、运行 1 条命令、修改 1 个文件、思考 1 段、1 个子代理");
    expect(active).toBe(false);
  });

  it("uses the progressive form for the last running item", () => {
    const items = [
      item({ id: "1", type: "command_execution", summary: "ls", ...done }),
      item({ id: "2", type: "command_execution", summary: "FOO=1 git status", status: "running" }),
    ];
    expect(summarizeGroup(items)).toEqual({ text: "正在运行 git", active: true });
    expect(
      summarizeGroup([item({ id: "3", type: "dynamic_tool", summary: "search", status: "pending" })]),
    ).toEqual({ text: "正在使用 search", active: true });
    expect(
      summarizeGroup([item({ id: "4", type: "reasoning", status: "starting" })]).text,
    ).toBe("正在思考");
  });

  it("falls back to counts when the thread is not live", () => {
    expect(
      summarizeGroup([item({ id: "1", type: "command_execution", status: "running" })], false),
    ).toEqual({ text: "运行 1 条命令", active: false });
  });
});

describe("commandProgram", () => {
  it("returns the first word and skips env assignments", () => {
    expect(commandProgram("git status")).toBe("git");
    expect(commandProgram("A=1 B=2 pnpm test")).toBe("pnpm");
    expect(commandProgram("")).toBe("");
  });
});
