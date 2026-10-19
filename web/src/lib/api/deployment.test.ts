import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  GITHUB_APP_LIVE_REQUEST_TIMEOUT_MS,
  deploymentApi,
} from "./deployment";

const { get, post, put } = vi.hoisted(() => {
  const json = () => ({
    json: vi.fn().mockResolvedValue({ data: null }),
  });
  return {
    get: vi.fn(json),
    post: vi.fn(json),
    put: vi.fn(json),
  };
});

vi.mock("./client", () => ({
  api: { get, post, put },
  decodeApiError: (promise: Promise<unknown>) => promise,
}));

type Call = [string, { json?: Record<string, unknown> } | undefined];
const calls = (mock: { mock: { calls: unknown[][] } }) =>
  mock.mock.calls as unknown as Call[];

describe("deployment api requests", () => {
  beforeEach(() => vi.clearAllMocks());

  it("reads and updates deployment settings", async () => {
    await deploymentApi.getSettings();
    expect(calls(get)[0][0]).toBe("deployment/settings");

    await deploymentApi.updateSettings({ public_url: "https://fs.example.com" });

    const [path, options] = calls(put)[0];
    expect(path).toBe("deployment/settings");
    expect(options?.json).toEqual({ public_url: "https://fs.example.com" });
  });

  it("reads and updates the github-app deployment", async () => {
    await deploymentApi.getGitHubApp();
    expect(calls(get)[0][0]).toBe("deployment/github-app");

    await deploymentApi.updateGitHubApp({
      app_id: "123456",
      private_key_pem: "-----BEGIN RSA PRIVATE KEY-----",
      webhook_secret: "",
    });

    const [path, options] = calls(put)[0];
    expect(path).toBe("deployment/github-app");
    expect(options?.json).toEqual({
      app_id: "123456",
      private_key_pem: "-----BEGIN RSA PRIVATE KEY-----",
      webhook_secret: "",
    });
  });

  it("passes null through for keep-stored-value fields", async () => {
    await deploymentApi.updateGitHubApp({
      app_id: "1",
      private_key_pem: null,
      webhook_secret: null,
    });

    expect(calls(put)[0][1]?.json).toEqual({
      app_id: "1",
      private_key_pem: null,
      webhook_secret: null,
    });
  });

  it("posts to the github-app test endpoint", async () => {
    await deploymentApi.testGitHubApp();
    expect(calls(post)[0][0]).toBe("deployment/github-app/test");
  });

  it("creates and completes the github-app manifest", async () => {
    await deploymentApi.createManifest({ org: "fast-ship" });
    expect(calls(post)[0]).toEqual([
      "deployment/github-app/manifest",
      expect.objectContaining({ json: { org: "fast-ship" } }),
    ]);

    await deploymentApi.createManifest();
    expect(calls(post)[1]).toEqual([
      "deployment/github-app/manifest",
      expect.objectContaining({ json: {} }),
    ]);

    await deploymentApi.completeManifest({ code: "abc", state: "s1" });
    expect(calls(post)[2]).toEqual([
      "deployment/github-app/manifest/complete",
      expect.objectContaining({ json: { code: "abc", state: "s1" } }),
    ]);
  });

  it("gives live-detection endpoints an extended timeout and no retry", async () => {
    await deploymentApi.updateGitHubApp({ app_id: "1" });
    await deploymentApi.testGitHubApp();

    expect(GITHUB_APP_LIVE_REQUEST_TIMEOUT_MS).toBe(30_000);
    const liveCalls = [...calls(put), ...calls(post)].filter(
      ([path]) =>
        path !== "deployment/github-app/manifest" &&
        path !== "deployment/github-app/manifest/complete",
    );
    for (const [, options] of liveCalls) {
      expect(options).toMatchObject({ timeout: 30_000, retry: 0 });
    }
  });

  it("gives manifest complete a longer timeout than other GitHub calls", async () => {
    // 后端 complete = conversions（最长 30s）+ 同步检测（最长 15s）：前端 30s
    // 会先超时，而 code/state 已消费无法重试，所以必须是 60s。
    await deploymentApi.completeManifest({ code: "c", state: "s" });

    const [, options] = calls(post)[0];
    expect(options).toMatchObject({ timeout: 60_000, retry: 0 });
  });
});
