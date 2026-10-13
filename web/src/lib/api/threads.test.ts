import { beforeEach, describe, expect, it, vi } from "vitest";
import { environmentApi, threadApi, THREAD_REQUEST_TIMEOUT_MS } from "./threads";

const { get, post, put, del } = vi.hoisted(() => {
  const json = (..._args: unknown[]) => ({
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
});
