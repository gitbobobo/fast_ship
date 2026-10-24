import { beforeEach, describe, expect, it, vi } from "vitest";
import { FORGE_LIVE_REQUEST_TIMEOUT_MS, forgeIntegrationsApi } from "./forge-integrations";

const { get, post, put, delete: del } = vi.hoisted(() => {
  const json = () => ({
    json: vi.fn().mockResolvedValue({ data: null }),
  });
  return {
    get: vi.fn(json),
    post: vi.fn(json),
    put: vi.fn(json),
    delete: vi.fn(json),
  };
});

vi.mock("./client", () => ({
  api: { get, post, put, delete: del },
  decodeApiError: (promise: Promise<unknown>) => promise,
}));

type Call = [string, { json?: Record<string, unknown>; timeout?: number; retry?: number } | undefined];
const calls = (mock: { mock: { calls: unknown[][] } }) =>
  mock.mock.calls as unknown as Call[];

describe("forge integrations api requests", () => {
  beforeEach(() => vi.clearAllMocks());

  it("lists integrations", async () => {
    await forgeIntegrationsApi.list();
    expect(calls(get)[0][0]).toBe("deployment/integrations");
  });

  it("creates a forgejo integration with the given payload", async () => {
    await forgeIntegrationsApi.create({
      name: "自建 Forgejo",
      base_url: "https://forge.example.com",
      token: "pat-1",
      allow_private_network: true,
    });

    const [path, options] = calls(post)[0];
    expect(path).toBe("deployment/integrations");
    expect(options?.json).toEqual({
      name: "自建 Forgejo",
      base_url: "https://forge.example.com",
      token: "pat-1",
      allow_private_network: true,
    });
  });

  it("updates and removes by id", async () => {
    await forgeIntegrationsApi.update("row-1", {
      name: "新名字",
      api_base_url: "",
      ca_cert_pem: null,
    });

    const [putPath, putOptions] = calls(put)[0];
    expect(putPath).toBe("deployment/integrations/row-1");
    expect(putOptions?.json).toEqual({
      name: "新名字",
      api_base_url: "",
      ca_cert_pem: null,
    });

    await forgeIntegrationsApi.remove("row-1");
    expect(calls(del)[0][0]).toBe("deployment/integrations/row-1");
  });

  it("only the test endpoint gets the live-request timeout and no retry", async () => {
    await forgeIntegrationsApi.list();
    await forgeIntegrationsApi.create({
      name: "n",
      base_url: "https://forge.example.com",
      token: "t",
    });
    await forgeIntegrationsApi.update("row-1", { name: "n" });
    await forgeIntegrationsApi.remove("row-1");
    await forgeIntegrationsApi.test("row-1");

    expect(FORGE_LIVE_REQUEST_TIMEOUT_MS).toBe(30_000);
    expect(calls(post)).toHaveLength(2);
    // test 是唯一真实打 Forgejo 实例的端点：加长超时、禁止重试。
    expect(calls(post)[1][0]).toBe("deployment/integrations/row-1/test");
    expect(calls(post)[1][1]).toMatchObject({
      timeout: 30_000,
      retry: 0,
    });
    expect(calls(post)[0][1]?.timeout).toBeUndefined();
  });
});
