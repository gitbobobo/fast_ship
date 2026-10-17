import { beforeEach, describe, expect, it, vi } from "vitest";
import { botApi, BOT_REQUEST_TIMEOUT_MS } from "./bots";

const { get, post, put, del } = vi.hoisted(() => {
  const json = () => ({
    json: vi.fn().mockResolvedValue({ data: null }),
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

type Call = [string, { json?: Record<string, unknown> }];
const calls = (mock: { mock: { calls: unknown[][] } }) =>
  mock.mock.calls as unknown as Call[];

describe("bot api requests", () => {
  beforeEach(() => vi.clearAllMocks());

  it("uses the frozen 35s timeout and no retry everywhere", async () => {
    const { signal } = new AbortController();
    await botApi.list(signal);
    await botApi.get("b 1", signal);
    await botApi.messages("b1", { limit: 50 }, signal);
    await botApi.runs("b1", signal);
    await botApi.calls("b1", signal);
    await botApi.callResult("b 1", "c/1", signal);
    await botApi.authorizations("b1", signal);

    expect(BOT_REQUEST_TIMEOUT_MS).toBe(35_000);
    for (const [, options] of calls(get)) {
      expect(options).toMatchObject({ timeout: 35_000, retry: 0, signal });
    }
  });

  it("encodes ids into every path", async () => {
    await botApi.get("b 1");
    await botApi.update("b/1", { name: "x" });
    await botApi.delete("b 1");
    await botApi.stopRun("b 1", "r/1");
    await botApi.decide("b 1", "c 1", "approve");
    await botApi.cancelCall("b 1", "c 1");
    await botApi.callResult("b 1", "c 1");
    await botApi.revokeAuthorization("b 1", "a 1");

    expect(calls(get).map(([path]) => path)).toEqual([
      "bots/b%201",
      "bots/b%201/calls/c%201/result",
    ]);
    expect(calls(put).map(([path]) => path)).toEqual(["bots/b%2F1"]);
    expect(calls(del).map(([path]) => path)).toEqual([
      "bots/b%201",
      "bots/b%201/authorizations/a%201",
    ]);
    expect(calls(post).map(([path]) => path)).toEqual([
      "bots/b%201/runs/r%2F1/stop",
      "bots/b%201/calls/c%201/decision",
      "bots/b%201/calls/c%201/cancel",
    ]);
  });

  it("routes management requests with contract bodies", async () => {
    await botApi.create({
      name: "助手",
      persona_prompt: "帮我盯 Issue",
      base_url: "https://api.example.com/v1",
      model: "gpt-test",
      api_key: "sk-live",
    });
    await botApi.update("b1", { model: "gpt-next", api_key: "sk-new" });
    await botApi.testConnection({ bot_id: "b1", base_url: "https://x", model: "m" });

    expect(calls(post)[0][1].json).toEqual({
      name: "助手",
      persona_prompt: "帮我盯 Issue",
      base_url: "https://api.example.com/v1",
      model: "gpt-test",
      api_key: "sk-live",
    });
    expect(calls(post)[1][0]).toBe("bots/test-connection");
    expect(calls(post)[1][1].json).toEqual({
      bot_id: "b1",
      base_url: "https://x",
      model: "m",
    });
    // 更新是指针语义：未提供的字段不出现在请求体里。
    expect(calls(put)[0][1].json).toEqual({ model: "gpt-next", api_key: "sk-new" });
  });

  it("omits undefined query params instead of stringifying them", async () => {
    await botApi.messages("b1");
    await botApi.messages("b1", { limit: 50 });
    await botApi.messages("b1", { after_seq: 7 });
    await botApi.messages("b1", { before_seq: 20, limit: 50 });
    await botApi.messages("b1", { after_seq: 0, limit: 50 });

    expect(
      calls(get).map(([, o]) => (o as { searchParams?: unknown }).searchParams),
    ).toEqual([
      undefined,
      { limit: "50" },
      { after_seq: "7" },
      { before_seq: "20", limit: "50" },
      { after_seq: "0", limit: "50" },
    ]);
  });

  it("sends messages with the idempotency key and decision with its enum", async () => {
    await botApi.send("b1", { client_message_id: "cm-1", text: "你好" });
    await botApi.decide("b1", "c1", "always_allow");
    await botApi.decide("b1", "c1", "reject");

    expect(calls(post)[0][0]).toBe("bots/b1/messages");
    expect(calls(post)[0][1].json).toEqual({
      client_message_id: "cm-1",
      text: "你好",
    });
    expect(calls(post)[1][1].json).toEqual({ decision: "always_allow" });
    expect(calls(post)[2][1].json).toEqual({ decision: "reject" });
  });

  it("routes schedule-task requests with encoded paths", async () => {
    const { signal } = new AbortController();
    await botApi.scheduleTasks("b 1", signal);
    await botApi.scheduleTask("b 1", "t/1", signal);
    await botApi.scheduleTaskRuns("b 1", "t 1", { limit: 20, offset: 40 }, signal);
    await botApi.setScheduleTaskEnabled("b 1", "t 1", false);

    expect(calls(get).map(([path]) => path)).toEqual([
      "bots/b%201/schedule-tasks",
      "bots/b%201/schedule-tasks/t%2F1",
      "bots/b%201/schedule-tasks/t%201/runs",
    ]);
    expect(
      (calls(get)[2][1] as { searchParams?: unknown }).searchParams,
    ).toEqual({ limit: "20", offset: "40" });
    expect(calls(put).map(([path]) => path)).toEqual([
      "bots/b%201/schedule-tasks/t%201/enabled",
    ]);
  });

  it("sends enabled as the only toggle body and omits undefined run params", async () => {
    await botApi.setScheduleTaskEnabled("b1", "t1", true);
    await botApi.scheduleTaskRuns("b1", "t1");
    await botApi.scheduleTaskRuns("b1", "t1", { limit: 20 });

    expect(calls(put)[0][1].json).toEqual({ enabled: true });
    expect(
      calls(get).map(([, o]) => (o as { searchParams?: unknown }).searchParams),
    ).toEqual([undefined, { limit: "20" }]);
  });
});
