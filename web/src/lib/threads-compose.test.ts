import { describe, expect, it } from "vitest";
import type { ThreadDetail, ThreadItem, ThreadSummary, WorkspaceOptions } from "@/lib/api/threads";
import { makeActivity, makeQueue } from "@/test/threads";
import {
  approvalOptions,
  buildModelSelection,
  buildWorkspace,
  canOperate,
  chooseRef,
  createIdTracker,
  isRequestAnswerable,
  parseProviders,
  reorderTarget,
  resolveModel,
  resolvePreviousWorktree,
  shouldPollThread,
} from "./threads-compose";

const item = (over: Partial<ThreadItem>): ThreadItem => ({
  id: "i",
  type: "approval_request",
  text: "",
  summary: "",
  ...over,
});

describe("access", () => {
  it("only operate may write; unknown means read-only", () => {
    expect(canOperate({ access: "operate" })).toBe(true);
    expect(canOperate({ access: "read-only" })).toBe(false);
    expect(canOperate(undefined)).toBe(false);
  });
});

describe("shouldPollThread", () => {
  const detail = (over: Partial<ThreadDetail>) =>
    ({
      thread: { status: "idle" },
      items: [],
      queue: makeQueue(),
      activity: makeActivity(),
      ...over,
    }) as ThreadDetail;

  it("polls for running, stopping, pending requests, and an unheld queue", () => {
    expect(shouldPollThread(detail({ thread: { status: "running" } as ThreadDetail["thread"] }))).toBe(true);
    expect(shouldPollThread(detail({ activity: makeActivity({ stopping: true }) }))).toBe(true);
    expect(shouldPollThread(detail({ activity: makeActivity({ has_pending_requests: true }) }))).toBe(true);
    const items = [{ run_id: "r", message_id: "m", text: "t" }];
    expect(shouldPollThread(detail({ queue: makeQueue({ items }) }))).toBe(true);
  });

  it("stops when idle, or only a held queue remains", () => {
    expect(shouldPollThread(detail({}))).toBe(false);
    const items = [{ run_id: "r", message_id: "m", text: "t" }];
    expect(shouldPollThread(detail({ queue: makeQueue({ held: true, items }) }))).toBe(false);
    expect(shouldPollThread(undefined)).toBe(false);
  });
});

describe("approval options and answerability", () => {
  it("uses request options, and the four defaults only when absent", () => {
    expect(approvalOptions(item({})).map((o) => o.decision)).toEqual([
      "accept",
      "decline",
      "acceptForSession",
      "cancel",
    ]);
    const custom = [{ decision: "accept", label: "Go", warning: "careful" }];
    expect(approvalOptions(item({ options: custom }))).toBe(custom);
  });

  it("answers only pending, non-inherited requests with a live/message channel", () => {
    const ok = { request_status: "pending", response_capability: "live" };
    expect(isRequestAnswerable(item(ok))).toBe(true);
    expect(isRequestAnswerable(item({ ...ok, response_capability: "message" }))).toBe(true);
    expect(isRequestAnswerable(item({ ...ok, inherited: true }))).toBe(false);
    expect(isRequestAnswerable(item({ ...ok, request_status: "resolved" }))).toBe(false);
    expect(isRequestAnswerable(item({ ...ok, response_capability: "not_resumable" }))).toBe(false);
  });
});

describe("reorderTarget", () => {
  const items = ["a", "b", "c"].map((id) => ({ run_id: id, message_id: id, text: id }));
  it("maps moves to before_run_id, null meaning the end", () => {
    expect(reorderTarget(items, 0, "up")).toBeUndefined();
    expect(reorderTarget(items, 1, "up")).toBe("a");
    expect(reorderTarget(items, 0, "down")).toBe("c");
    expect(reorderTarget(items, 1, "down")).toBeNull();
    expect(reorderTarget(items, 2, "down")).toBeUndefined();
  });
});

describe("createIdTracker", () => {
  it("reuses ids for the same content and renews after reset or a change", () => {
    const tracker = createIdTracker();
    const a = tracker.idsFor("x");
    expect(tracker.idsFor("x")).toBe(a);
    expect(tracker.idsFor("y")).not.toBe(a);
    const b = tracker.idsFor("y");
    tracker.reset();
    expect(tracker.idsFor("y")).not.toBe(b);
  });
});

describe("workspace", () => {
  it("never sends branch for root, and omits an empty new branch", () => {
    expect(buildWorkspace({ kind: "root" })).toEqual({ type: "root" });
    expect(buildWorkspace({ kind: "worktree", baseRef: "main", newBranch: "  " })).toEqual({
      type: "worktree",
      base_ref: "main",
    });
    expect(buildWorkspace({ kind: "worktree", baseRef: "main", newBranch: "f/x" })).toEqual({
      type: "worktree",
      base_ref: "main",
      branch: "f/x",
    });
    expect(buildWorkspace({ kind: "worktree", baseRef: "", newBranch: "" })).toBeNull();
  });

  it("a ref with a worktree switches to it, unless it is the main checkout", () => {
    const current = { kind: "worktree", baseRef: "main", newBranch: "keep" } as const;
    expect(chooseRef(current, { name: "f", kind: "local", worktree_path: "/w/f" }, "/w")).toEqual({
      kind: "existing",
      path: "/w/f",
      branch: "f",
    });
    expect(chooseRef(current, { name: "main", kind: "local", worktree_path: "/w" }, "/w")).toEqual({
      kind: "root",
    });
    expect(chooseRef(current, { name: "dev", kind: "local" }, "/w")).toEqual({
      kind: "worktree",
      baseRef: "dev",
      newBranch: "keep",
    });
  });
});

describe("resolvePreviousWorktree", () => {
  const workspace: WorkspaceOptions = {
    project_id: "p",
    cwd: "/w",
    refs: [
      { name: "main", kind: "local", worktree_path: "/w" },
      { name: "a", kind: "local", worktree_path: "/w/a" },
      { name: "b", kind: "local", worktree_path: "/w/b" },
    ],
  };
  const t = (over: Partial<ThreadSummary>) =>
    ({
      id: "t",
      environment_id: "e",
      project_id: "p",
      status: "idle",
      branch: "a",
      updated_at: "2026-10-01T00:00:00Z",
      ...over,
    }) as ThreadSummary;
  const run = (threads: ThreadSummary[], currentWorktreePath?: string) =>
    resolvePreviousWorktree({ threads, environmentId: "e", projectId: "p", workspace, currentWorktreePath });

  it("picks the latest valid, unarchived, same-project thread outside the current checkout", () => {
    expect(
      run([
        t({ branch: "a", updated_at: "2026-10-01T00:00:00Z" }),
        t({ branch: "b", updated_at: "2026-10-03T00:00:00Z" }),
        t({ branch: "main", updated_at: "2026-10-09T00:00:00Z" }),
        t({ branch: "a", updated_at: "2026-10-08T00:00:00Z", status: "archived" }),
        t({ branch: "a", updated_at: "2026-10-08T00:00:00Z", project_id: "other" }),
        t({ branch: "a", updated_at: "2026-10-08T00:00:00Z", environment_id: "other" }),
        t({ branch: "a", updated_at: "2026-10-08T00:00:00Z", parent_thread_id: "p0" }),
        t({ branch: "a", updated_at: "not-a-date" }),
      ]),
    ).toEqual({ branch: "b", path: "/w/b" });
  });

  it("skips the worktree already selected and returns null when nothing qualifies", () => {
    const threads = [t({ branch: "b", updated_at: "2026-10-03T00:00:00Z" }), t({ branch: "a" })];
    expect(run(threads, "/w/b")).toEqual({ branch: "a", path: "/w/a" });
    expect(run([t({ branch: "unknown" })])).toBeNull();
  });
});

describe("model selection", () => {
  const config = {
    providers: [
      { instanceId: "off", enabled: false, installed: true, models: [{ slug: "x" }] },
      {
        instanceId: "codex",
        displayName: "Codex",
        enabled: true,
        installed: true,
        models: [
          {
            slug: "gpt-5",
            name: "GPT-5",
            capabilities: {
              optionDescriptors: [
                {
                  id: "effort",
                  label: "Effort",
                  type: "select",
                  options: [
                    { id: "low", label: "Low" },
                    { id: "high", label: "High", isDefault: true },
                  ],
                },
                { id: "fast", label: "Fast", type: "boolean" },
              ],
            },
          },
        ],
      },
    ],
  };

  it("parses only available providers and select descriptors", () => {
    const providers = parseProviders(config);
    expect(providers.map((p) => p.instanceId)).toEqual(["codex"]);
    expect(providers[0].models[0].descriptors.map((d) => d.id)).toEqual(["effort"]);
  });

  it("keeps subProvider only when the upstream model carries one", () => {
    const providers = parseProviders({
      providers: [
        {
          instanceId: "pi",
          displayName: "Pi",
          enabled: true,
          installed: true,
          models: [
            { slug: "default", name: "Pi default" },
            { slug: "commandcode/deepseek", name: "DeepSeek", subProvider: "commandcode" },
            { slug: "blank", name: "Blank", subProvider: "" },
            { slug: "null", name: "Null", subProvider: null },
            { slug: "number", name: "Number", subProvider: 7 },
            { slug: "object", name: "Object", subProvider: { nested: true } },
          ],
        },
      ],
    });
    expect(providers[0].models.map((m) => m.subProvider)).toEqual([
      undefined,
      "commandcode",
      undefined,
      undefined,
      undefined,
      undefined,
    ]);
  });

  it("emits options as an [{id,value}] array, not an object", () => {
    const providers = parseProviders(config);
    const resolved = resolveModel(providers, undefined, {
      instanceId: "codex",
      model: "gpt-5",
      efforts: { effort: "low" },
    });
    const selection = buildModelSelection(resolved);
    expect(selection).toEqual({
      instance_id: "codex",
      model: "gpt-5",
      options: [{ id: "effort", value: "low" }],
    });
    expect(Array.isArray(selection?.options)).toBe(true);
  });

  it("falls back to the project default, then the first model, when a pick no longer exists", () => {
    const providers = parseProviders(config);
    const stale = { instanceId: "gone", model: "m", efforts: {} };
    const fromDefault = resolveModel(
      providers,
      { id: "p", title: "P", default_model_selection: { instance_id: "codex", model: "gpt-5", options: [{ id: "effort", value: "low" }] } },
      stale,
    );
    expect(buildModelSelection(fromDefault)?.options).toEqual([{ id: "effort", value: "low" }]);
    expect(resolveModel(providers, undefined, stale).model?.slug).toBe("gpt-5");
    expect(buildModelSelection(resolveModel([], undefined, null))).toBeUndefined();
  });
});
