import type { ReactNode } from "react";
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  useCreateForgeIntegration,
  useForgeIntegrations,
  watchForgeIntegrationsIdentity,
} from "@/lib/hooks/use-forge-integrations";
import { signInAs, signOut, teardownAuth } from "@/test/auth";

const { list, create } = vi.hoisted(() => ({
  list: vi.fn(),
  create: vi.fn(),
}));

vi.mock("@/lib/api/forge-integrations", () => ({
  forgeIntegrationsApi: { list, create },
  FORGE_LIVE_REQUEST_TIMEOUT_MS: 30_000,
}));

const builtinRow: ForgeIntegration = {
  id: "github-default",
  kind: "github",
  name: "github-default",
  base_url: "https://github.com",
  api_base_url: "https://api.github.com",
  api_base_url_derived: false,
  token_set: true,
  webhook_secret_set: false,
  has_ca_cert: false,
  allow_private_network: false,
  builtin: true,
  created_at: null,
  updated_at: null,
};

function forgejoRow(id: string): ForgeIntegration {
  return {
    id,
    kind: "forgejo",
    name: `forgejo-${id}`,
    base_url: "https://forge.example.com",
    api_base_url: "https://forge.example.com/api/v1",
    api_base_url_derived: false,
    token_set: true,
    webhook_secret_set: false,
    has_ca_cert: false,
    allow_private_network: false,
    builtin: false,
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
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
  list.mockReset();
  create.mockReset();
});

describe("useForgeIntegrations identity", () => {
  it("never shows user A's list to user B, even when B's request fails", async () => {
    const { client, wrapper } = setup();
    const stop = watchForgeIntegrationsIdentity(client);
    list.mockResolvedValueOnce({ data: [builtinRow, forgejoRow("A-1")] });
    const seen: Array<string | undefined> = [];
    const { result } = renderHook(
      () => {
        const query = useForgeIntegrations();
        seen.push(query.data?.[1]?.id);
        return query;
      },
      { wrapper },
    );
    await waitFor(() => expect(result.current.data?.[1]?.id).toBe("A-1"));

    list.mockRejectedValueOnce(new Error("500"));
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
    const stop = watchForgeIntegrationsIdentity(client);
    list.mockResolvedValue({ data: [builtinRow] });
    const { result } = renderHook(() => useForgeIntegrations(), { wrapper });
    await waitFor(() => expect(result.current.data).toHaveLength(1));

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

describe("forge mutations stay with the requester", () => {
  it("refreshes the requesting user's list after creating an integration", async () => {
    const { client, wrapper } = setup();
    list.mockResolvedValueOnce({ data: [builtinRow] });
    create.mockResolvedValue({ data: forgejoRow("new-1") });
    const { result } = renderHook(
      () => ({
        query: useForgeIntegrations(),
        mutation: useCreateForgeIntegration(),
      }),
      { wrapper },
    );
    await waitFor(() => expect(result.current.query.data).toHaveLength(1));

    // 创建成功触发的失效重拉返回含新行的列表。
    list.mockResolvedValueOnce({
      data: [builtinRow, forgejoRow("new-1")],
    });
    await act(async () => {
      await result.current.mutation.mutateAsync({
        name: "自建",
        base_url: "https://forge.example.com",
        token: "pat",
      });
    });

    await waitFor(() => expect(result.current.query.data).toHaveLength(2));
    expect(
      client.getQueryData<ForgeIntegration[]>(["forge-integrations", "A", "list"]),
    ).toHaveLength(2);
  });

  it("drops user A's slow create response after switching to user B", async () => {
    const { client, wrapper } = setup();
    const stop = watchForgeIntegrationsIdentity(client);
    let resolveCreate!: (value: { data: ForgeIntegration }) => void;
    create.mockImplementation(
      () => new Promise((resolve) => (resolveCreate = resolve)),
    );
    list.mockReturnValue(new Promise(() => {}));
    const { result } = renderHook(
      () => ({
        query: useForgeIntegrations(),
        mutation: useCreateForgeIntegration(),
      }),
      { wrapper },
    );

    let pending!: Promise<ApiResponse<ForgeIntegration>>;
    act(() => {
      pending = result.current.mutation.mutateAsync({
        name: "自建",
        base_url: "https://forge.example.com",
        token: "pat",
      });
    });
    await waitFor(() => expect(create).toHaveBeenCalledTimes(1));
    act(() => signOut());
    act(() => signInAs("B"));
    await act(async () => {
      resolveCreate({ data: forgejoRow("new-1") });
      await pending;
    });

    await waitFor(() => expect(result.current.mutation.isSuccess).toBe(true));
    // A 的迟到响应既不写 A 的缓存（人已切走），也进不了 B 的缓存。
    expect(
      client.getQueryData(["forge-integrations", "A", "list"]),
    ).toBeUndefined();
    expect(
      client.getQueryData(["forge-integrations", "B", "list"]),
    ).toBeUndefined();
    stop();
  });
});
