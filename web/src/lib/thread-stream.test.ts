import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { subscribeThreadStream, type ThreadStreamEvent } from "./thread-stream";

const { resolveSessionToken } = vi.hoisted(() => ({ resolveSessionToken: vi.fn() }));
const authState = { token: "token-a" as string | null };

vi.mock("./api/client", () => ({ resolveSessionToken }));
vi.mock("./store/auth-store", () => ({
  useAuthStore: { getState: () => ({ token: authState.token }) },
}));

const tick = (ms = 0) => new Promise((resolve) => setTimeout(resolve, ms));

// ---- 订阅循环：用可控的假流替代 fetch，只覆盖代码实际用到的接口 ----

interface FakeStream {
  push(chunk: string): void;
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

describe("subscribeThreadStream", () => {
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

  it("connects with bearer auth and delivers parsed shell events", async () => {
    const stream = fakeStream();
    fetchMock.mockResolvedValue(fakeResponse(stream.reader));
    const events: ThreadStreamEvent[] = [];
    const sub = subscribeThreadStream({ onEvent: (e) => events.push(e) });

    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/api/threads/stream");
    expect(init.headers.Authorization).toBe("Bearer token-a");
    expect(init.headers.Accept).toBe("text/event-stream");

    stream.push(
      'event: shell\ndata: {"environment_id":"e1","thread_ids":["t1"],"state":"ready"}\n\n',
    );
    await vi.waitFor(() => expect(events).toHaveLength(1));
    expect(events[0]).toEqual({
      environment_id: "e1",
      thread_ids: ["t1"],
      state: "ready",
    });
    sub.close();
  });

  it("ignores comment heartbeats and non-shell frames", async () => {
    const stream = fakeStream();
    fetchMock.mockResolvedValue(fakeResponse(stream.reader));
    const events: ThreadStreamEvent[] = [];
    const sub = subscribeThreadStream({ onEvent: (e) => events.push(e) });
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));

    // 心跳注释、其他事件名、多行 data：只有 shell 帧会进入回调。
    stream.push(": ping\n\n");
    stream.push('event: other\ndata: {"x":1}\n\n');
    stream.push('event: shell\ndata: {"full":true}\n\n');
    await vi.waitFor(() => expect(events).toHaveLength(1));
    expect(events[0]).toEqual({ full: true });
    sub.close();
  });

  it("reconnects with backoff after the stream ends and delivers again", async () => {
    const first = fakeStream();
    fetchMock.mockResolvedValueOnce(fakeResponse(first.reader));
    const second = fakeStream();
    fetchMock.mockResolvedValueOnce(fakeResponse(second.reader));
    const statuses: string[] = [];
    const events: ThreadStreamEvent[] = [];
    const sub = subscribeThreadStream({
      onEvent: (e) => events.push(e),
      onStatus: (s) => statuses.push(s),
      retryBaseMs: 1,
    });

    first.push('event: shell\ndata: {"full":true}\n\n');
    await vi.waitFor(() => expect(events).toHaveLength(1));
    first.end();
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));

    second.push('event: shell\ndata: {"environment_id":"e1","full":true}\n\n');
    await vi.waitFor(() => expect(events).toHaveLength(2));
    expect(events[1].environment_id).toBe("e1");
    expect(statuses).toEqual(["connecting", "open", "reconnecting", "reconnecting", "open"]);
    sub.close();
  });

  it("retries forever: no error give-up beyond the old fixed budget", async () => {
    fetchMock.mockRejectedValue(new Error("network down"));
    const statuses: string[] = [];
    const sub = subscribeThreadStream({
      onEvent: vi.fn(),
      onStatus: (s) => statuses.push(s),
      retryBaseMs: 1,
    });

    // bot-stream 的固定预算会在第 6 次连接时放弃；线程壳流必须继续重连。
    await vi.waitFor(() => expect(fetchMock.mock.calls.length).toBeGreaterThan(6));
    expect(statuses).not.toContain("error");
    expect(statuses).toContain("reconnecting");

    const stream = fakeStream();
    fetchMock.mockResolvedValue(fakeResponse(stream.reader));
    await vi.waitFor(() => expect(statuses[statuses.length - 1]).toBe("open"));
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
    const sub = subscribeThreadStream({
      onEvent: vi.fn(),
      onStatus: (s) => statuses.push(s),
    });

    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(resolveSessionToken).toHaveBeenCalledWith("token-a");
    expect(fetchMock.mock.calls[1][1].headers.Authorization).toBe("Bearer token-b");
    expect(statuses).toEqual(["connecting", "connecting", "open"]);
    sub.close();
  });

  it("backs off after three consecutive 401s", async () => {
    fetchMock.mockResolvedValue(fakeResponse(fakeStream().reader, 401));
    const statuses: string[] = [];
    const sub = subscribeThreadStream({
      onEvent: vi.fn(),
      onStatus: (s) => statuses.push(s),
      retryBaseMs: 1,
    });

    // 前两次 401 刷新后立刻重连；第三次起转入退避（reconnecting）。
    await vi.waitFor(() => expect(statuses).toContain("reconnecting"));
    expect(resolveSessionToken.mock.calls.length).toBeGreaterThanOrEqual(3);
    sub.close();
  });

  it("close is idempotent, cancels in-flight reads, and stops pending reconnects", async () => {
    fetchMock.mockRejectedValue(new Error("down"));
    const statuses: string[] = [];
    const sub = subscribeThreadStream({
      onEvent: vi.fn(),
      onStatus: (s) => statuses.push(s),
      // 退避调大：断言窗口内重连定时器绝不触发，验证 close 能取消挂起重试。
      retryBaseMs: 60_000,
    });
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));

    sub.close();
    sub.close(); // StrictMode 双清理必须安全
    await tick(30);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(statuses[statuses.length - 1]).toBe("closed");
  });

  it("aborts the fetch on close without delivering further events", async () => {
    const streams: FakeStream[] = [];
    fetchMock.mockImplementation((_url: string, init: { signal: AbortSignal }) => {
      const stream = fakeStream(init.signal);
      streams.push(stream);
      return Promise.resolve(fakeResponse(stream.reader));
    });
    const events: ThreadStreamEvent[] = [];
    const sub = subscribeThreadStream({ onEvent: (e) => events.push(e) });
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    streams[0].push('event: shell\ndata: {"full":true}\n\n');
    await vi.waitFor(() => expect(events).toHaveLength(1));

    sub.close();
    const count = events.length;
    // abort 后假流使 read() reject；循环退出且不再投递、不再重连。
    streams[0].push('event: shell\ndata: {"full":true}\n\n');
    await tick(20);
    expect(events).toHaveLength(count);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});
