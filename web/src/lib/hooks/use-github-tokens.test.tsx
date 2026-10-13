import type { ReactNode } from "react";
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  useGithubTokens,
  watchGithubTokensIdentity,
} from "@/lib/hooks/use-github-tokens";
import { signInAs, signOut, teardownAuth } from "@/test/auth";

const { list } = vi.hoisted(() => ({ list: vi.fn() }));

vi.mock("@/lib/api/github-tokens", () => ({
  githubTokenApi: { list },
}));

function tokenList(title: string): { data: GitHubToken[] } {
  return {
    data: [
      {
        id: "tok-1",
        title,
        token_prefix: "ghp_AAAA",
        detection_status: "ok",
        detection_token_type: "classic",
        detection_scopes: ["repo"],
        detection_login: "octocat",
        detection_detail: "",
        detected_at: "2026-10-01T00:00:00Z",
        referenced_projects: [{ id: "p1", name: "secret-project" }],
        created_at: "2026-10-01T00:00:00Z",
        updated_at: "2026-10-01T00:00:00Z",
      },
    ],
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
});

describe("useGithubTokens identity", () => {
  it("never shows user A's tokens to user B, even when B's request fails", async () => {
    const { client, wrapper } = setup();
    const stop = watchGithubTokensIdentity(client);
    list.mockResolvedValueOnce(tokenList("A-SECRET"));
    const seen: Array<string | undefined> = [];
    const { result } = renderHook(
      () => {
        const query = useGithubTokens();
        seen.push(query.data?.[0]?.title);
        return query;
      },
      { wrapper },
    );
    await waitFor(() => expect(result.current.data?.[0]?.title).toBe("A-SECRET"));

    list.mockRejectedValueOnce(new Error("404"));
    seen.length = 0;
    act(() => signOut());
    act(() => signInAs("B"));
    await waitFor(() => expect(result.current.isError).toBe(true));

    expect(result.current.data).toBeUndefined();
    expect(seen.every((title) => title === undefined)).toBe(true);
    stop();
  });

  it("clears the cache on logout", async () => {
    const { client, wrapper } = setup();
    const stop = watchGithubTokensIdentity(client);
    list.mockResolvedValue(tokenList("A-SECRET"));
    const { result } = renderHook(() => useGithubTokens(), { wrapper });
    await waitFor(() => expect(result.current.data?.[0]?.title).toBe("A-SECRET"));

    act(() => signOut());

    expect(client.getQueryCache().findAll().filter((q) => q.queryKey[1] === "A")).toHaveLength(0);
    stop();
  });
});
