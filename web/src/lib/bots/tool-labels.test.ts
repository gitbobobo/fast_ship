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
    expect(toolActionLabel("fs_thread", "settle")).toBe("结算线程（收进历史区）");
    expect(toolActionLabel("fs_thread", "unsettle")).toBe("撤销结算");
    expect(toolActionLabel("fs_thread", "resume_queue")).toBe("恢复队列");
    expect(toolActionLabel("fs_schedule", "create")).toBe("创建定时任务");
    expect(toolActionLabel("fs_schedule", "run")).toBe("立即运行定时任务");
    expect(toolActionLabel("fs_schedule", "state_get")).toBe("读取任务状态");
    expect(toolActionLabel("fs_schedule", "state_set")).toBe("写入任务状态");
    expect(toolActionLabel("fs_memory", "append")).toBe("追加记忆内容");
  });

  it("fs_schedule 十个动作全部有中文标签", () => {
    const expected: Record<string, string> = {
      list: "列出定时任务",
      create: "创建定时任务",
      update: "更新定时任务",
      delete: "删除定时任务",
      runs: "查看任务运行记录",
      run: "立即运行定时任务",
      enable: "启用定时任务",
      disable: "停用定时任务",
      state_get: "读取任务状态",
      state_set: "写入任务状态",
    };
    for (const [action, label] of Object.entries(expected)) {
      expect(toolActionLabel("fs_schedule", action)).toBe(label);
    }
  });

  it("未知工具或 action 回退为原始标识，不抛错", () => {
    expect(toolActionLabel("fs_xxx", "do")).toBe("fs_xxx.do");
    expect(toolActionLabel("fs_issue", "explode")).toBe("fs_issue.explode");
    // 受理即拒的调用可能没解析出 action：空 action 回退为裸工具名。
    expect(toolActionLabel("fs_thread", "")).toBe("fs_thread");
    expect(toolActionLabel("", "")).toBe("");
  });
});

describe("toolActionEffect", () => {
  it("按后端只读清单返回 read/write", () => {
    expect(toolActionEffect("fs_issue", "get")).toBe("read");
    expect(toolActionEffect("fs_issue", "timeline")).toBe("read");
    // fs_schedule 的写无论同步（delete）还是异步需确认（create/run）都是写入。
    expect(toolActionEffect("fs_schedule", "delete")).toBe("write");
    expect(toolActionEffect("fs_schedule", "runs")).toBe("read");
    expect(toolActionEffect("fs_schedule", "run")).toBe("write");
    expect(toolActionEffect("fs_schedule", "state_get")).toBe("read");
    expect(toolActionEffect("fs_schedule", "state_set")).toBe("write");
    expect(toolActionEffect("fs_memory", "restore")).toBe("write");
    expect(toolActionEffect("fs_result", "read")).toBe("read");
    expect(toolActionEffect("fs_cancel", "cancel")).toBe("write");
    // fs_thread 的三个新写动作与 send/stop 一致：异步、需确认、写入。
    expect(toolActionEffect("fs_thread", "settle")).toBe("write");
    expect(toolActionEffect("fs_thread", "unsettle")).toBe("write");
    expect(toolActionEffect("fs_thread", "resume_queue")).toBe("write");
  });

  it("fs_schedule：读类 read、写类 write（含需确认与同步写）", () => {
    for (const action of ["list", "runs", "state_get"]) {
      expect(toolActionEffect("fs_schedule", action)).toBe("read");
    }
    // create/敏感字段 update/run 异步需确认；delete/enable/disable/state_set
    // 同步执行——同步不等于只读，effect 一律 write。
    for (const action of [
      "create",
      "update",
      "delete",
      "run",
      "enable",
      "disable",
      "state_set",
    ]) {
      expect(toolActionEffect("fs_schedule", action)).toBe("write");
    }
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
    expect(issue!.value).toBe("abcdef1234567890");
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

  it("launch/send 的 text 不进 chip（确认卡整块平铺），无 action 时仍出", () => {
    const launch = toolParamChips("fs_thread", {
      action: "launch",
      env_id: "env-1",
      text: "调查这个仓库",
    });
    expect(launch.find((c) => c.key === "text")).toBeUndefined();
    const send = toolParamChips("fs_thread", {
      action: "send",
      env_id: "env-1",
      text: "回复内容",
    });
    expect(send.find((c) => c.key === "text")).toBeUndefined();
    const noAction = toolParamChips("fs_thread", { env_id: "env-1", text: "正文" });
    expect(noAction.find((c) => c.key === "text")).toBeDefined();
  });

  it("workspace 拆字段 chip：各自折行不互相截断（窄屏视觉审查）", () => {
    const chips = toolParamChips("fs_thread", {
      workspace: {
        type: "worktree",
        branch: "feat-x",
        base_ref: "main",
        start_from_origin: true,
      },
    });
    const byKey = new Map(chips.map((c) => [c.key, c.value]));
    expect(byKey.get("workspace")).toBe("新工作树");
    expect(byKey.get("workspace.branch")).toBe("feat-x");
    expect(byKey.get("workspace.base_ref")).toBe("main");
    expect(byKey.get("workspace.start")).toBe("origin");

    const existing = toolParamChips("fs_thread", {
      workspace: {
        type: "existing_worktree",
        worktree_path: "/a/b/c/d",
        branch: "fix",
      },
    });
    const eKeys = new Map(existing.map((c) => [c.key, c.value]));
    expect(eKeys.get("workspace")).toBe("已有工作树");
    expect(eKeys.get("workspace.path")).toBe("/a/b/c/d");
    expect(eKeys.get("workspace.branch")).toBe("fix");

    // 长路径中段省略保留首尾目录（尾段区分、首段给根上下文），完整
    // 路径仍在 title（视觉审查：截首只剩尾部不可核对）。
    const longPath =
      "/Users/dev/repos/monorepo/packages/service-a/worktrees/hotfix-login-flow-regression-20261015";
    const longChips = toolParamChips("fs_thread", {
      workspace: { type: "existing_worktree", worktree_path: longPath },
    });
    const pathChip = longChips.find((c) => c.key === "workspace.path")!;
    expect(pathChip.value).toBe("/Users/dev/repos/monorepo…/hotfix-login-flow-regression-20261015");
    expect(pathChip.title).toBe(longPath);

    const bare = toolParamChips("fs_thread", {
      workspace: { type: "worktree" },
    });
    expect(bare.map((c) => c.key)).toEqual(["workspace"]);
    expect(bare[0]!.value).toBe("新工作树");

    // start_from_origin:null 与省略同口径（服务端 null=缺省），不出
    // 「起点：本地」chip——显示了等于谎称显式传了 false（代码审查 P2）。
    const nullStart = toolParamChips("fs_thread", {
      workspace: { type: "worktree", start_from_origin: null },
    });
    expect(nullStart.map((c) => c.key)).toEqual(["workspace"]);
    const falseStart = toolParamChips("fs_thread", {
      workspace: { type: "worktree", start_from_origin: false },
    });
    expect(falseStart.find((c) => c.key === "workspace.start")?.value).toBe("本地");
  });

  it("launch 缺省 runtime_mode 也给「完全访问（缺省）」警示 chip", () => {
    const chips = toolParamChips(
      "fs_thread",
      { action: "launch", env_id: "e", project_id: "p", text: "x" },
      "launch",
    );
    const rt = chips.find((c) => c.key === "runtime_mode");
    expect(rt).toBeDefined();
    expect(rt!.value).toBe("完全访问（缺省）");
    expect(rt!.tone).toBe("warn");
    // send 不带 runtime_mode 字段语义，不出缺省 chip；非 launch 同。
    const send = toolParamChips(
      "fs_thread",
      { action: "send", env_id: "e", project_id: "p", text: "x" },
      "send",
    );
    expect(send.find((c) => c.key === "runtime_mode")).toBeUndefined();
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
    expect(chips[0]!.value).toBe("call-123456789");
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
