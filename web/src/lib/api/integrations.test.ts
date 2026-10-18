import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  GITHUB_APP_LIVE_REQUEST_TIMEOUT_MS,
  integrationApi,
} from "./integrations";

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

describe("integrations api requests", () => {
  beforeEach(() => vi.clearAllMocks());

  it("reads and updates the github-app integration", async () => {
    await integrationApi.getGitHubApp();
    expect(calls(get)[0][0]).toBe("integrations/github-app");

    await integrationApi.updateGitHubApp({
      app_id: "123456",
      private_key_pem: "-----BEGIN RSA PRIVATE KEY-----",
      webhook_secret: "",
    });

    const [path, options] = calls(put)[0];
    expect(path).toBe("integrations/github-app");
    expect(options?.json).toEqual({
      app_id: "123456",
      private_key_pem: "-----BEGIN RSA PRIVATE KEY-----",
      webhook_secret: "",
    });
  });

  it("passes null through for keep-stored-value fields", async () => {
    await integrationApi.updateGitHubApp({
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
    await integrationApi.testGitHubApp();
    expect(calls(post)[0][0]).toBe("integrations/github-app/test");
  });

  it("gives live-detection endpoints an extended timeout and no retry", async () => {
    await integrationApi.updateGitHubApp({ app_id: "1" });
    await integrationApi.testGitHubApp();

    expect(GITHUB_APP_LIVE_REQUEST_TIMEOUT_MS).toBe(30_000);
    for (const [, options] of [...calls(put), ...calls(post)]) {
      expect(options).toMatchObject({ timeout: 30_000, retry: 0 });
    }
  });
});
