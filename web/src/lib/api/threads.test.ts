import { beforeEach, describe, expect, it, vi } from "vitest";
import { environmentApi, threadApi, THREAD_REQUEST_TIMEOUT_MS } from "./threads";

const { get, post, put, del } = vi.hoisted(() => {
  const json = (..._args: unknown[]) => ({
    json: vi.fn().mockResolvedValue({ data: null }),
    blob: vi.fn().mockResolvedValue(new Blob()),
  });
  return {
    get: vi.fn(json),
    post: vi.fn(json),
    put: vi.fn(json),
    del: vi.fn(json),
  };
});

vi.mock("./client", () => ({
  api: { get, post, put, delete: del },
  decodeApiError: (promise: Promise<unknown>) => promise,
}));

type Call = [string, { json?: Record<string, unknown>; body?: unknown; headers?: unknown; searchParams?: unknown }];
const calls = (mock: { mock: { calls: unknown[][] } }) => mock.mock.calls as unknown as Call[];

const expected = { timeout: THREAD_REQUEST_TIMEOUT_MS, retry: 0 };

describe("T3 thread api requests", () => {
  beforeEach(() => vi.clearAllMocks());

  it("gives every request an independent 35s timeout and forwards the abort signal", async () => {
    const { signal } = new AbortController();

    await environmentApi.list(signal);
    await threadApi.list(signal);
    await threadApi.detail("e 1", "t/1", signal);
    await threadApi.itemDetail("e 1", "t/1", "src:c 1", signal);
    await environmentApi.create({ name: "n", pairing_url: "u" });
    await environmentApi.update("e1", { name: "n" });
    await environmentApi.delete("e1");

    expect(THREAD_REQUEST_TIMEOUT_MS).toBe(35_000);
    expect(get.mock.calls.map(([path]) => path)).toEqual([
      "environments",
      "threads",
      "environments/e%201/threads/t%2F1",
      "environments/e%201/threads/t%2F1/items/src%3Ac%201",
    ]);
    for (const [, options] of get.mock.calls) {
      expect(options).toMatchObject({ ...expected, signal });
    }
    expect(post.mock.calls[0][1]).toMatchObject(expected);
    expect(put.mock.calls[0][1]).toMatchObject(expected);
    expect(del.mock.calls[0][1]).toMatchObject(expected);
  });

  it("routes write requests with the contract bodies", async () => {
    const att = { type: "image" as const, id: "a1", name: "n.png", mime_type: "image/png", size_bytes: 1 };
    await threadApi.send("e 1", "t1", { command_id: "c", message_id: "m", text: "hi", attachments: [] });
    await threadApi.stop("e1", "t1");
    await threadApi.resumeQueue("e1", "t1");
    await threadApi.steer("e1", "t1", "r 1");
    await threadApi.cancelQueued("e1", "t1", "r1");
    await threadApi.reorderQueued("e1", "t1", "r1", null);
    await threadApi.respond("e1", "t1", "q1", { type: "approval", decision: "accept" });
    await threadApi.answerSecret("e1", "t1", "src::s1", { type: "decline" });
    await threadApi.editQueued("e1", "t1", "r1", { text: "x" });
    await threadApi.editQueued("e1", "t1", "r1", { text: "x", attachments: [att] });

    expect(post.mock.calls.map(([path]) => path)).toEqual([
      "environments/e%201/threads/t1/messages",
      "environments/e1/threads/t1/stop",
      "environments/e1/threads/t1/queue/resume",
      "environments/e1/threads/t1/queue/r%201/steer",
      "environments/e1/threads/t1/queue/r1/cancel",
      "environments/e1/threads/t1/queue/r1/reorder",
      "environments/e1/threads/t1/requests/q1/respond",
      "environments/e1/threads/t1/items/src%3A%3As1/secret-answer",
    ]);
    expect(calls(post)[5][1].json).toEqual({ before_run_id: null });
    expect(calls(post)[7][1].json).toEqual({ type: "decline" });
    for (const [, options] of calls(post)) expect(options).toMatchObject(expected);

    // 编辑：缺省不传 attachments（保留），带上才整组替换。
    expect(calls(put)[0][1].json).toEqual({ text: "x", attachments: undefined });
    expect(JSON.parse(JSON.stringify(calls(put)[0][1].json))).not.toHaveProperty("attachments");
    expect(calls(put)[1][1].json?.attachments).toEqual([att]);
  });

  it("creates threads, reads launcher options, and uploads raw image bytes", async () => {
    await threadApi.create("e1", {
      command_id: "c",
      thread_id: "t",
      project_id: "p",
      message: { message_id: "m", text: "hi" },
      runtime_mode: "full-access",
      workspace: { type: "root" },
    });
    await threadApi.composerOptions("e1");
    get.mockReturnValueOnce({
      json: vi.fn().mockResolvedValue({
        data: {
          project_id: "p 1",
          cwd: "/r",
          refs: [{ name: "feat-a", kind: "local" }],
          next_cursor: 3,
        },
      }),
    } as never);
    get.mockReturnValueOnce({
      json: vi.fn().mockResolvedValue({
        data: {
          project_id: "p 1",
          cwd: "/r",
          refs: [{ name: "feat-b", kind: "local" }],
          next_cursor: null,
        },
      }),
    } as never);
    const paged = await threadApi.workspaceOptions("e1", "p 1", "feat");
    expect(paged.data.refs.map((r) => r.name)).toEqual(["feat-a", "feat-b"]);
    expect(paged.data.next_cursor).toBeNull();
    await threadApi.workspaceOptions("e1", "p1", "");
    const file = new File([new Uint8Array([1])], "a.png", { type: "image/png" });
    await threadApi.uploadAttachment("e1", file);
    await threadApi.uploadAttachment("e1", new File([new Uint8Array([1])], "截图.png", { type: "image/png" }));
    await threadApi.deleteAttachment("e1", "a/1");
    await threadApi.attachmentContent("e1", "a/1");

    expect(post.mock.calls[0][0]).toBe("environments/e1/threads");
    expect(calls(post)[0][1].json?.workspace).toEqual({ type: "root" });
    expect(get.mock.calls.map(([path]) => path)).toEqual([
      "environments/e1/composer-options",
      "environments/e1/projects/p%201/workspace-options",
      "environments/e1/projects/p%201/workspace-options",
      "environments/e1/projects/p1/workspace-options",
      "environments/e1/attachments/a%2F1/content",
    ]);
    expect(calls(get)[1][1].searchParams).toEqual({ query: "feat" });
    expect(calls(get)[2][1].searchParams).toEqual({ query: "feat", cursor: 3 });
    expect(calls(get)[3][1].searchParams).toBeUndefined();

    const [upload, utf8Upload] = calls(post).slice(1);
    expect(upload[0]).toBe("environments/e1/attachments");
    expect(upload[1].body).toBe(file);
    expect(upload[1].json).toBeUndefined();
    expect(upload[1].headers).toEqual({ "Content-Type": "image/png", "X-File-Name": "a.png" });
    expect(utf8Upload[1].headers).toEqual({ "Content-Type": "image/png" });
    expect(del.mock.calls[0][0]).toBe("environments/e1/attachments/a%2F1");
  });

  it("keeps next_cursor when the client page cap is hit", async () => {
    let page = 0;
    get.mockImplementation(
      () =>
        ({
          json: vi.fn().mockImplementation(async () => {
            page += 1;
            return {
              data: {
                project_id: "p1",
                cwd: "/r",
                refs: [{ name: `r${page}`, kind: "local" }],
                next_cursor: page,
              },
            };
          }),
        }) as never,
    );
    const paged = await threadApi.workspaceOptions("e1", "p1", "");
    expect(paged.data.refs).toHaveLength(32);
    expect(paged.data.next_cursor).toBe(32);
    expect(get).toHaveBeenCalledTimes(32);
  });
});
