import type { ReactNode } from "react";
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  useGitHubAppIntegration,
  useTestGitHubAppIntegration,
  useUpdateGitHubAppIntegration,
  watchIntegrationsIdentity,
} from "@/lib/hooks/use-integrations";
import { signInAs, signOut, teardownAuth } from "@/test/auth";

const { getGitHubApp, updateGitHubApp, testGitHubApp } = vi.hoisted(() => ({
  getGitHubApp: vi.fn(),
  updateGitHubApp: vi.fn(),
  testGitHubApp: vi.fn(),
}));

vi.mock("@/lib/api/integrations", () => ({
  integrationApi: { getGitHubApp, updateGitHubApp, testGitHubApp },
}));

function integration(appId: string): { data: GitHubAppIntegration } {
  return {
    data: {
      provider: "github_app",
      configured: true,
      source: "database",
      app_id: appId,
      private_key_set: true,
      webhook_secret_set: false,
      webhook_url: "",
      detection_status: "ok",
      detection_detail: "",
      detected_at: "2026-10-01T00:00:00Z",
      updated_at: "2026-10-01T00:00:00Z",
    },
  };
}

function setup() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  return { client, wrapper };
}

beforeEach(() => signInAs("A"));
afterEach(() => {
  teardownAuth();
  getGitHubApp.mockReset();
  updateGitHubApp.mockReset();
  testGitHubApp.mockReset();
});

describe("useGitHubAppIntegration identity", () => {
  it("never shows user A's integration to user B, even when B's request fails", async () => {
    const { client, wrapper } = setup();
    const stop = watchIntegrationsIdentity(client);
    getGitHubApp.mockResolvedValueOnce(integration("A-APP-ID"));
    const seen: Array<string | undefined> = [];
    const { result } = renderHook(
      () => {
        const query = useGitHubAppIntegration();
        seen.push(query.data?.app_id);
        return query;
      },
      { wrapper },
    );
    await waitFor(() => expect(result.current.data?.app_id).toBe("A-APP-ID"));

    getGitHubApp.mockRejectedValueOnce(new Error("404"));
    seen.length = 0;
    act(() => signOut());
    act(() => signInAs("B"));
    await waitFor(() => expect(result.current.isError).toBe(true));

    expect(result.current.data).toBeUndefined();
    expect(seen.every((id) => id === undefined)).toBe(true);
    stop();
  });

  it("clears the cache on logout", async () => {
    const { client, wrapper } = setup();
    const stop = watchIntegrationsIdentity(client);
    getGitHubApp.mockResolvedValue(integration("A-APP-ID"));
    const { result } = renderHook(() => useGitHubAppIntegration(), { wrapper });
    await waitFor(() => expect(result.current.data?.app_id).toBe("A-APP-ID"));

    act(() => signOut());

    expect(
      client
        .getQueryCache()
        .findAll()
        .filter((q) => q.queryKey[1] === "A"),
    ).toHaveLength(0);
    stop();
  });
});

describe("integration mutations stay with the requester", () => {
  it("writes the save response into the requesting user's cache", async () => {
    const { client, wrapper } = setup();
    updateGitHubApp.mockResolvedValue(integration("654321"));
    const { result } = renderHook(() => useUpdateGitHubAppIntegration(), {
      wrapper,
    });

    await act(async () => {
      await result.current.mutateAsync({ app_id: "654321" });
    });

    expect(
      client.getQueryData<GitHubAppIntegration>(["integrations", "A"])?.app_id,
    ).toBe("654321");
  });

  // 挂起的 GET：B 的缓存只能由 mutation 回写产生，断言确定。
  function hangingFetch() {
    return new Promise<{ data: GitHubAppIntegration }>(() => {});
  }

  it("drops user A's slow save response after switching to user B", async () => {
    const { client, wrapper } = setup();
    const stop = watchIntegrationsIdentity(client);
    let resolveUpdate!: (value: { data: GitHubAppIntegration }) => void;
    updateGitHubApp.mockImplementation(
      () => new Promise((resolve) => (resolveUpdate = resolve)),
    );
    getGitHubApp.mockImplementation(hangingFetch);
    // 同组件挂载 query hook（与集成页一致）：换号重渲染会把在飞
    // mutation 的回调换成新 userId 闭包，context 必须绑定的是发起时的人。
    const { result } = renderHook(
      () => ({
        query: useGitHubAppIntegration(),
        mutation: useUpdateGitHubAppIntegration(),
      }),
      { wrapper },
    );

    let pending!: Promise<ApiResponse<GitHubAppIntegration>>;
    act(() => {
      pending = result.current.mutation.mutateAsync({ app_id: "654321" });
    });
    // mutationFn 要等 onMutate 结算后才发出，先确认请求真的在飞。
    await waitFor(() => expect(updateGitHubApp).toHaveBeenCalledTimes(1));
    act(() => signOut());
    act(() => signInAs("B"));
    await act(async () => {
      resolveUpdate(integration("A-APP-ID"));
      await pending;
    });

    await waitFor(() => expect(result.current.mutation.isSuccess).toBe(true));
    expect(client.getQueryData(["integrations", "A"])).toBeUndefined();
    expect(client.getQueryData(["integrations", "B"])).toBeUndefined();
    stop();
  });

  it("drops user A's slow test response after switching to user B", async () => {
    const { client, wrapper } = setup();
    const stop = watchIntegrationsIdentity(client);
    let resolveTest!: (value: { data: GitHubAppIntegration }) => void;
    testGitHubApp.mockImplementation(
      () => new Promise((resolve) => (resolveTest = resolve)),
    );
    getGitHubApp.mockImplementation(hangingFetch);
    const { result } = renderHook(
      () => ({
        query: useGitHubAppIntegration(),
        mutation: useTestGitHubAppIntegration(),
      }),
      { wrapper },
    );

    let pending!: Promise<ApiResponse<GitHubAppIntegration>>;
    act(() => {
      pending = result.current.mutation.mutateAsync();
    });
    await waitFor(() => expect(testGitHubApp).toHaveBeenCalledTimes(1));
    act(() => signOut());
    act(() => signInAs("B"));
    await act(async () => {
      resolveTest(integration("A-APP-ID"));
      await pending;
    });

    await waitFor(() => expect(result.current.mutation.isSuccess).toBe(true));
    expect(client.getQueryData(["integrations", "A"])).toBeUndefined();
    expect(client.getQueryData(["integrations", "B"])).toBeUndefined();
    stop();
  });
});
