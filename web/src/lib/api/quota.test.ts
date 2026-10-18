import { beforeEach, describe, expect, it, vi } from "vitest";
import { quotaApi, QUOTA_LIVE_REQUEST_TIMEOUT_MS } from "./quota";

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
  ApiRequestError: class ApiRequestError extends Error {},
}));

type Call = [string, { json?: Record<string, unknown> } | undefined];
const calls = (mock: { mock: { calls: unknown[][] } }) =>
  mock.mock.calls as unknown as Call[];

describe("quota api requests", () => {
  beforeEach(() => vi.clearAllMocks());

  it("lists providers and accounts with GET", async () => {
    await quotaApi.listProviders();
    await quotaApi.listAccounts();

    expect(calls(get).map(([path]) => path)).toEqual([
      "quota/providers",
      "quota/accounts",
    ]);
  });

  it("creates, updates, deletes accounts and posts refresh/probe", async () => {
    await quotaApi.createAccount({
      provider: "factory",
      label: "我的 Pro",
      credentials: { api_key: "fk-123" },
    });
    await quotaApi.updateAccount("acc 1", { label: "改名" });
    await quotaApi.updateAccount("acc-2", {
      credentials: { api_key: "fk-new" },
    });
    await quotaApi.deleteAccount("acc 1");
    await quotaApi.refreshAccount("acc 1");
    await quotaApi.probe({
      provider: "grok",
      credentials: { token: "xai-1" },
    });

    const posts = calls(post);
    expect(posts[0][0]).toBe("quota/accounts");
    expect(posts[0][1]?.json).toEqual({
      provider: "factory",
      label: "我的 Pro",
      credentials: { api_key: "fk-123" },
    });
    expect(posts[1][0]).toBe("quota/accounts/acc%201/refresh");
    expect(posts[2][0]).toBe("quota/test");
    expect(posts[2][1]?.json).toEqual({
      provider: "grok",
      credentials: { token: "xai-1" },
    });

    expect(calls(put)[0][0]).toBe("quota/accounts/acc%201");
    expect(calls(put)[0][1]?.json).toEqual({ label: "改名" });
    // 整组替换凭据：不带 label
    expect(calls(put)[1][1]?.json).toEqual({
      credentials: { api_key: "fk-new" },
    });

    expect(calls(del)[0][0]).toBe("quota/accounts/acc%201");
  });

  it("gives live-fetch endpoints an extended timeout and no retry", async () => {
    await quotaApi.refreshAccount("a1");
    await quotaApi.probe({ provider: "zai", credentials: { api_key: "k" } });

    expect(QUOTA_LIVE_REQUEST_TIMEOUT_MS).toBe(30_000);
    for (const [, options] of calls(post)) {
      expect(options).toMatchObject({ timeout: 30_000, retry: 0 });
    }
  });

  it("device authorization endpoints: POST 起授权、GET 轮询、DELETE 取消", async () => {
    await quotaApi.startDeviceAuthorization({
      provider: "opendesign",
      label: "我的 Design",
    });
    // 编辑模式换凭据：account_id 透传进请求体（A5/B4）
    await quotaApi.startDeviceAuthorization({
      provider: "opendesign",
      label: "我的 Design",
      account_id: "acc-1",
    });
    await quotaApi.getDeviceAuthorization("sess 1");
    await quotaApi.cancelDeviceAuthorization("sess 1");

    const posts = calls(post);
    expect(posts[0][0]).toBe("quota/device-authorizations");
    expect(posts[0][1]?.json).toEqual({
      provider: "opendesign",
      label: "我的 Design",
    });
    expect(posts[1][1]?.json).toEqual({
      provider: "opendesign",
      label: "我的 Design",
      account_id: "acc-1",
    });
    // 起授权与轮询都是真实上游请求：延长超时、禁重试
    expect(posts[0][1]).toMatchObject({ timeout: 30_000, retry: 0 });

    const gets = calls(get);
    expect(gets[0][0]).toBe("quota/device-authorizations/sess%201");
    expect(gets[0][1]).toMatchObject({ timeout: 30_000, retry: 0 });

    expect(calls(del)[0][0]).toBe("quota/device-authorizations/sess%201");
  });
});
