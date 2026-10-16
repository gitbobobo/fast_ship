import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createSSEParser, subscribeBotStream, type BotStreamEvent } from "./bot-stream";

const { resolveSessionToken } = vi.hoisted(() => ({ resolveSessionToken: vi.fn() }));
const authState = { token: "token-a" as string | null };

vi.mock("./api/client", () => ({ resolveSessionToken }));
vi.mock("./store/auth-store", () => ({
  useAuthStore: { getState: () => ({ token: authState.token }) },
}));

const tick = (ms = 0) => new Promise((resolve) => setTimeout(resolve, ms));

describe("createSSEParser", () => {
  it("parses event/data/id fields from a plain frame", () => {
    const events: BotStreamEvent[] = [];
    const parser = createSSEParser((e) => events.push(e));
    parser.push('event: delta\ndata: {"text":"hi"}\nid: 5\n\n');
    expect(events).toEqual([
      { event: "delta", data: '{"text":"hi"}', id: 5 },
    ]);
  });

  it("accepts CRLF and bare CR line endings", () => {
    const events: BotStreamEvent[] = [];
    const parser = createSSEParser((e) => events.push(e));
    parser.push("event: done\r\ndata: {}\r\r");
    expect(events).toEqual([
      { event: "done", data: "{}", id: null },
    ]);
  });

  it("buffers half lines across chunks before dispatching", () => {
    const events: BotStreamEvent[] = [];
    const parser = createSSEParser((e) => events.push(e));
    parser.push('event: tool_call\ndata: {"call');
    parser.push('_id":"c1"}\n');
    parser.push("id: 9\n");
    expect(events).toEqual([]);
    parser.push("\n");
    expect(events).toEqual([
      { event: "tool_call", data: '{"call_id":"c1"}', id: 9 },
    ]);
  });

  it("joins multi-line data with newlines and ignores comment heartbeats", () => {
    const events: BotStreamEvent[] = [];
    const parser = createSSEParser((e) => events.push(e));
    parser.push(": ping\ndata: first\ndata: second\n\n: ping\n");
    expect(events).toEqual([
      { event: "message", data: "first\nsecond", id: null },
    ]);
  });

  it("maps missing or non-numeric ids to null", () => {
    const events: BotStreamEvent[] = [];
    const parser = createSSEParser((e) => events.push(e));
    parser.push("data: a\n\nid: not-a-seq\ndata: b\n\n");
    expect(events.map((e) => e.id)).toEqual([null, null]);
  });

  it("drops an incomplete trailing line when the stream ends", () => {
    const events: BotStreamEvent[] = [];
    const parser = createSSEParser((e) => events.push(e));
    parser.push("data: complete\n\ndata: half-line-no-newline");
    expect(events).toEqual([{ event: "message", data: "complete", id: null }]);
  });
});

// ---- 订阅循环：用可控的假流替代 fetch，只覆盖代码实际用到的接口 ----

interface FakeStream {
  push(chunk: string): void;
  pushBytes(chunk: Uint8Array): void;
  end(): void;
  fail(err: unknown): void;
  reader: { read(): Promise<IteratorResult<Uint8Array>> };
}

function fakeStream(signal?: AbortSignal): FakeStream {
  const encoder = new TextEncoder();
  const queue: Uint8Array[] = [];
  const waiters: Array<{
    resolve: (r: IteratorResult<Uint8Array>) => void;
    reject: (e: unknown) => void;
  }> = [];
  let ended = false;
  let failure: unknown = null;
  const flush = () => {
    while (waiters.length > 0) {
      if (failure !== null) {
        waiters.shift()!.reject(failure);
        continue;
      }
      const chunk = queue.shift();
      if (chunk) {
        waiters.shift()!.resolve({ done: false, value: chunk });
        continue;
      }
      if (ended) {
        waiters.shift()!.resolve({ done: true, value: undefined });
        continue;
      }
      break;
    }
  };
  signal?.addEventListener("abort", () => {
    failure = new Error("aborted");
    flush();
  });
  return {
    push(chunk) {
      queue.push(encoder.encode(chunk));
      flush();
    },
    pushBytes(chunk) {
      queue.push(chunk);
      flush();
    },
    end() {
      ended = true;
      flush();
    },
    fail(err) {
      failure = err;
      flush();
    },
    reader: {
      read: () =>
        new Promise<IteratorResult<Uint8Array>>((resolve, reject) => {
          waiters.push({ resolve, reject });
          flush();
        }),
    },
  };
}

function fakeResponse(
  reader: { read(): Promise<IteratorResult<Uint8Array>> },
  status = 200,
): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    body: { getReader: () => reader, cancel: async () => undefined },
  } as unknown as Response;
}

describe("subscribeBotStream", () => {
  const fetchMock = vi.fn();

  beforeEach(() => {
    vi.clearAllMocks();
    authState.token = "token-a";
    resolveSessionToken.mockResolvedValue("token-b");
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("subscribes with bearer auth and the after_seq cursor", async () => {
    const stream = fakeStream();
    fetchMock.mockResolvedValue(fakeResponse(stream.reader));
    const events: BotStreamEvent[] = [];
    const sub = subscribeBotStream({
      botId: "b 1",
      afterSeq: 7,
      onEvent: (e) => events.push(e),
    });

    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/api/bots/b%201/events?after_seq=7");
    expect(init.headers.Authorization).toBe("Bearer token-a");
    expect(init.headers.Accept).toBe("text/event-stream");

    stream.push('id: 8\nevent: delta\ndata: {"text":"hi"}\n\n');
    await vi.waitFor(() => expect(events).toHaveLength(1));
    expect(events[0]).toMatchObject({ event: "delta", data: '{"text":"hi"}', id: 8 });
    expect(sub.lastSeq()).toBe(8);
    sub.close();
  });

  it("omits the Authorization header when signed out", async () => {
    authState.token = null;
    const stream = fakeStream();
    fetchMock.mockResolvedValue(fakeResponse(stream.reader));
    const sub = subscribeBotStream({ botId: "b1", afterSeq: 0, onEvent: vi.fn() });
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    expect(fetchMock.mock.calls[0][1].headers.Authorization).toBeUndefined();
    sub.close();
  });

  it("decodes multibyte characters split across chunks", async () => {
    const stream = fakeStream();
    fetchMock.mockResolvedValue(fakeResponse(stream.reader));
    const events: BotStreamEvent[] = [];
    const sub = subscribeBotStream({ botId: "b1", afterSeq: 0, onEvent: (e) => events.push(e) });
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));

    const encoder = new TextEncoder();
    const head = encoder.encode('id: 1\ndata: {"text":"');
    const tail = encoder.encode('"}\n\n');
    const payload = encoder.encode("你好");
    const whole = new Uint8Array(head.length + payload.length + tail.length);
    whole.set(head, 0);
    whole.set(payload, head.length);
    whole.set(tail, head.length + payload.length);
    // 把「你」的三个字节劈开：TextDecoder({stream:true}) 必须缓冲前两个字节。
    const split = head.length + 1;
    stream.pushBytes(whole.slice(0, split));
    stream.pushBytes(whole.slice(split));

    await vi.waitFor(() => expect(events).toHaveLength(1));
    expect(JSON.parse(events[0].data).text).toBe("你好");
    sub.close();
  });

  it("refreshes the session on 401 and reconnects with the new token", async () => {
    // 真实 resolveSessionToken 刷新后会把新 token 写回 store，重连时读到的就是它。
    resolveSessionToken.mockImplementation(async () => {
      authState.token = "token-b";
      return "token-b";
    });
    fetchMock.mockResolvedValueOnce(fakeResponse(fakeStream().reader, 401));
    const stream = fakeStream();
    fetchMock.mockResolvedValueOnce(fakeResponse(stream.reader));
    const statuses: string[] = [];
    const sub = subscribeBotStream({
      botId: "b1",
      afterSeq: 0,
      onEvent: vi.fn(),
      onStatus: (s) => statuses.push(s),
    });

    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(resolveSessionToken).toHaveBeenCalledWith("token-a");
    expect(fetchMock.mock.calls[1][1].headers.Authorization).toBe("Bearer token-b");
    // 401 刷新重连发生在成功建连之前，两次都是 connecting（connected 未置位）。
    expect(statuses).toEqual(["connecting", "connecting", "open"]);
    sub.close();
  });

  it("reconnects after stream end from lastSeq and drops replayed duplicates", async () => {
    const first = fakeStream();
    fetchMock.mockResolvedValueOnce(fakeResponse(first.reader));
    const second = fakeStream();
    fetchMock.mockResolvedValueOnce(fakeResponse(second.reader));
    const statuses: string[] = [];
    const events: BotStreamEvent[] = [];
    const sub = subscribeBotStream({
      botId: "b1",
      afterSeq: 0,
      onEvent: (e) => events.push(e),
      onStatus: (s) => statuses.push(s),
      retryBaseMs: 1,
    });

    first.push("id: 10\ndata: one\n\n");
    await vi.waitFor(() => expect(events).toHaveLength(1));
    first.end();
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(fetchMock.mock.calls[1][0]).toContain("after_seq=10");
    expect(statuses).toContain("reconnecting");

    // 补读重叠：seq 10 已投递过，去重后只有 11 进来。
    second.push("id: 10\ndata: one-again\n\nid: 11\ndata: two\n\n");
    await vi.waitFor(() => expect(events).toHaveLength(2));
    expect(events[1].data).toBe("two");
    sub.close();
  });

  it("gives up after the retry budget and can resume manually", async () => {
    fetchMock.mockRejectedValue(new Error("network down"));
    const statuses: string[] = [];
    const sub = subscribeBotStream({
      botId: "b1",
      afterSeq: 0,
      onEvent: vi.fn(),
      onStatus: (s) => statuses.push(s),
      retryBaseMs: 1,
      maxRetries: 2,
    });

    await vi.waitFor(() => expect(statuses[statuses.length - 1]).toBe("error"));
    expect(fetchMock).toHaveBeenCalledTimes(3);

    const stream = fakeStream();
    fetchMock.mockResolvedValue(fakeResponse(stream.reader));
    sub.resume();
    await vi.waitFor(() => expect(statuses[statuses.length - 1]).toBe("open"));
    sub.close();
  });

  it("close is idempotent, cancels in-flight reads, and stops pending reconnects", async () => {
    fetchMock.mockRejectedValue(new Error("down"));
    const statuses: string[] = [];
    const sub = subscribeBotStream({
      botId: "b1",
      afterSeq: 0,
      onEvent: vi.fn(),
      onStatus: (s) => statuses.push(s),
      // 退避调大：断言窗口内重连定时器绝不触发，验证 close 能取消挂起重试。
      retryBaseMs: 60_000,
      maxRetries: 5,
    });
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));

    sub.close();
    sub.close(); // StrictMode 双清理必须安全
    await tick(30);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(statuses[statuses.length - 1]).toBe("closed");

    // close 之后 resume 是空操作，不再发起连接。
    sub.resume();
    await tick(10);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("aborts the fetch on close without delivering further events", async () => {
    const streams: FakeStream[] = [];
    fetchMock.mockImplementation(
      (_url: string, init: { signal: AbortSignal }) => {
        const stream = fakeStream(init.signal);
        streams.push(stream);
        return Promise.resolve(fakeResponse(stream.reader));
      },
    );
    const events: BotStreamEvent[] = [];
    const sub = subscribeBotStream({ botId: "b1", afterSeq: 0, onEvent: (e) => events.push(e) });
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    streams[0].push("id: 1\ndata: before-close\n\n");
    await vi.waitFor(() => expect(events).toHaveLength(1));

    sub.close();
    const count = events.length;
    // abort 后假流使 read() reject；循环退出且不再投递、不再重连。
    streams[0].push("id: 2\ndata: after-close\n\n");
    await tick(20);
    expect(events).toHaveLength(count);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});
