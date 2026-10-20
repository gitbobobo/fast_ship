import { beforeEach, describe, expect, it, vi } from "vitest";
import { aiProviderApi, AI_PROVIDER_REQUEST_TIMEOUT_MS } from "./ai-providers";

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

describe("ai provider api requests", () => {
  beforeEach(() => vi.clearAllMocks());

  it("uses the frozen 35s timeout and no retry everywhere", async () => {
    const { signal } = new AbortController();
    await aiProviderApi.list(signal);
    await aiProviderApi.catalog(signal);

    expect(AI_PROVIDER_REQUEST_TIMEOUT_MS).toBe(35_000);
    for (const [, options] of calls(get)) {
      expect(options).toMatchObject({ timeout: 35_000, retry: 0, signal });
    }
  });

  it("routes list/catalog paths per contract", async () => {
    await aiProviderApi.list();
    await aiProviderApi.catalog();

    expect(calls(get).map(([path]) => path)).toEqual([
      "ai-providers",
      "ai-providers/catalog",
    ]);
  });

  it("sends create/update/delete with contract bodies", async () => {
    await aiProviderApi.create({
      catalog_id: "deepseek",
      name: "主力 DeepSeek",
      api_key: "sk-live",
    });
    await aiProviderApi.create({
      catalog_id: "custom",
      name: "自建网关",
      base_url: "https://gw.internal/v1",
      api_key: "sk-live",
    });
    await aiProviderApi.update("p/1", { name: "改名", api_key: "sk-new" });
    await aiProviderApi.delete("p 1");

    expect(calls(post).map(([path]) => path)).toEqual([
      "ai-providers",
      "ai-providers",
    ]);
    expect(calls(post)[0][1].json).toEqual({
      catalog_id: "deepseek",
      name: "主力 DeepSeek",
      api_key: "sk-live",
    });
    expect(calls(post)[1][1].json).toEqual({
      catalog_id: "custom",
      name: "自建网关",
      base_url: "https://gw.internal/v1",
      api_key: "sk-live",
    });
    // 更新是指针语义：未提供的字段不出现在请求体里；catalog_id 不可改。
    expect(calls(put)[0][0]).toBe("ai-providers/p%2F1");
    expect(calls(put)[0][1].json).toEqual({ name: "改名", api_key: "sk-new" });
    expect(calls(del)[0][0]).toBe("ai-providers/p%201");
  });

  it("posts test-connection payload as-is", async () => {
    await aiProviderApi.test({ catalog_id: "deepseek", api_key: "sk-x" });
    await aiProviderApi.test({
      catalog_id: "custom",
      base_url: "https://gw.internal/v1",
      api_key: "sk-x",
      model: "my-model",
    });

    expect(calls(post)[0][0]).toBe("ai-providers/test");
    expect(calls(post)[0][1].json).toEqual({
      catalog_id: "deepseek",
      api_key: "sk-x",
    });
    expect(calls(post)[1][1].json).toEqual({
      catalog_id: "custom",
      base_url: "https://gw.internal/v1",
      api_key: "sk-x",
      model: "my-model",
    });
  });
});
