import { describe, expect, it } from "vitest";
import {
  TOOL_EFFECT_BADGE,
  TOOL_EFFECT_LABELS,
  toolActionEffect,
  toolActionLabel,
  toolParamChips,
} from "./tool-labels";

describe("toolActionLabel", () => {
  it("返回已知工具/action 的中文标签", () => {
    expect(toolActionLabel("fs_issue", "create")).toBe("创建问题");
    expect(toolActionLabel("fs_issue", "comment")).toBe("添加评论");
    expect(toolActionLabel("fs_issue", "replace_checklist")).toBe(
      "更新清单（整组替换）",
    );
    expect(toolActionLabel("fs_thread", "get")).toBe("查看线程");
    expect(toolActionLabel("fs_thread", "launch")).toBe("启动线程");
    expect(toolActionLabel("fs_thread", "send")).toBe("向线程发送消息");
    expect(toolActionLabel("fs_schedule", "create")).toBe("创建定时任务");
    expect(toolActionLabel("fs_memory", "append")).toBe("追加记忆内容");
  });

  it("未知工具或 action 回退为原始标识，不抛错", () => {
    expect(toolActionLabel("fs_xxx", "do")).toBe("fs_xxx.do");
    expect(toolActionLabel("fs_issue", "explode")).toBe("fs_issue.explode");
    expect(toolActionLabel("", "")).toBe(".");
  });
});

describe("toolActionEffect", () => {
  it("按后端只读清单返回 read/write", () => {
    expect(toolActionEffect("fs_issue", "get")).toBe("read");
    expect(toolActionEffect("fs_issue", "timeline")).toBe("read");
    // fs_schedule 的写是同步执行、不需确认，但仍是写入。
    expect(toolActionEffect("fs_schedule", "delete")).toBe("write");
    expect(toolActionEffect("fs_schedule", "runs")).toBe("read");
    expect(toolActionEffect("fs_memory", "restore")).toBe("write");
    expect(toolActionEffect("fs_result", "read")).toBe("read");
    expect(toolActionEffect("fs_cancel", "cancel")).toBe("write");
  });

  it("未知值保守默认 write", () => {
    expect(toolActionEffect("fs_xxx", "peek")).toBe("write");
    expect(toolActionEffect("fs_issue", "peek")).toBe("write");
  });

  it("effect 徽标与标签覆盖 read/write 两种取值", () => {
    expect(TOOL_EFFECT_LABELS.read).toBe("只读");
    expect(TOOL_EFFECT_LABELS.write).toBe("写入");
    expect(TOOL_EFFECT_BADGE.read).not.toBe(TOOL_EFFECT_BADGE.write);
  });
});

describe("toolParamChips", () => {
  it("issue_id + project_id 拼出问题详情路由，ID 显示为短标识", () => {
    const chips = toolParamChips("fs_issue", {
      issue_id: "abcdef1234567890",
      project_id: "proj-0001",
    });
    const issue = chips.find((c) => c.key === "issue_id");
    expect(issue).toBeDefined();
    expect(issue!.value).toBe("abcdef12…");
    expect(issue!.title).toBe("abcdef1234567890");
    expect(issue!.href).toBe("/projects/proj-0001/issues/abcdef1234567890");
    const project = chips.find((c) => c.key === "project_id");
    expect(project).toBeDefined();
    expect(project!.href).toBeUndefined();
  });

  it("thread_id + env_id 拼出线程路由；text 截断进 chip", () => {
    const long =
      "这是一条很长很长的消息正文内容，需要被截断处理，否则会撑爆卡片的显示区域，排布会乱掉";
    const chips = toolParamChips("fs_thread", {
      env_id: "env-1",
      thread_id: "thread-xyz-987654321",
      text: long,
    });
    const thread = chips.find((c) => c.key === "thread_id");
    expect(thread!.href).toBe("/threads/env-1/thread-xyz-987654321");
    const text = chips.find((c) => c.key === "text");
    expect(text!.value.endsWith("…")).toBe(true);
    expect(text!.title).toBe(long);
  });

  it("次要参数（分页/乐观锁/action 自身）不进 chips", () => {
    const chips = toolParamChips("fs_issue", {
      project_id: "p1",
      page: 2,
      page_size: 50,
      expected_updated_at: "2026-10-17T00:00:00Z",
      action: "list",
    });
    expect(chips.map((c) => c.key)).toEqual(["project_id"]);
  });

  it("清单 items 摘要为数量，不展开结构化内容", () => {
    const chips = toolParamChips("fs_issue", {
      issue_id: "i1",
      items: [{ text: "a" }, { text: "b" }, { text: "c" }],
    });
    const items = chips.find((c) => c.key === "items");
    expect(items!.value).toBe("3 项");
  });

  it("未知工具走通用字段集，不抛错", () => {
    const chips = toolParamChips("fs_xxx", {
      call_id: "call-123456789",
      whatever: { nested: true },
    });
    expect(chips.map((c) => c.key)).toEqual(["call_id"]);
    expect(chips[0]!.value).toBe("call-123…");
    expect(chips[0]!.href).toBeUndefined();
  });

  it("空参数与缺省值返回空数组/跳过", () => {
    expect(toolParamChips("fs_issue", {})).toEqual([]);
    expect(toolParamChips("fs_issue", null)).toEqual([]);
    expect(toolParamChips("fs_issue", undefined)).toEqual([]);
    const chips = toolParamChips("fs_memory", { path: "" });
    expect(chips).toEqual([]);
  });

  it("fs_thread 的 query 不进 chips（仅 workspace_options 引用名过滤用）；fs_issue 仍显示关键词", () => {
    const threadChips = toolParamChips("fs_thread", {
      query: "my-repo",
      env_id: "env-1",
    });
    expect(threadChips.map((c) => c.key)).toEqual(["env_id"]);
    const issueChips = toolParamChips("fs_issue", {
      project_id: "p1",
      query: "登录",
    });
    expect(issueChips.map((c) => c.key)).toEqual(["project_id", "query"]);
    expect(issueChips.find((c) => c.key === "query")!.label).toBe("关键词");
  });
});
