import type { ReactNode } from "react";
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type {
  CreateThreadInput,
  ThreadDetail,
  ThreadItem,
  ThreadStatus,
} from "@/lib/api/threads";
import {
  THREAD_DETAIL_POLL_MS,
  THREAD_LIST_DEGRADED_POLL_MS,
  useCreateThread,
  useThreadDetail,
  useThreadItemDetail,
  useThreads,
  watchThreadsIdentity,
} from "@/lib/hooks/use-threads";
import { useThreadListStore } from "@/lib/store/thread-list-store";
import { makeActivity, makeQueue } from "@/test/threads";
import { signInAs, signOut, teardownAuth } from "@/test/auth";

const { create, detail, list, itemDetail } = vi.hoisted(() => ({
  create: vi.fn(),
  detail: vi.fn(),
  list: vi.fn(),
  itemDetail: vi.fn(),
}));

vi.mock("@/lib/api/threads", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/threads")>()),
  threadApi: { create, list, detail, itemDetail },
}));

function response(status: ThreadStatus, title = "T"): { data: ThreadDetail } {
  return {
    data: {
      thread: { id: "t1", title, status } as ThreadDetail["thread"],
      items: [],
      queue: makeQueue(),
      activity: makeActivity(),
      access: "operate",
    },
  };
}

function detailWith(
  over: Partial<Pick<ThreadDetail, "queue" | "activity">>,
  status: ThreadStatus = "idle",
) {
  const base = response(status).data;
  return { data: { ...base, ...over } };
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
  create.mockReset();
  detail.mockReset();
  itemDetail.mockReset();
  list.mockReset();
  useThreadListStore.getState().resetFilters();
});

describe("useThreadDetail polling", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("polls while running and stops once the thread is no longer running", async () => {
    detail
      .mockResolvedValueOnce(response("running"))
      .mockResolvedValueOnce(response("running"))
      .mockResolvedValue(response("idle"));
    const { wrapper } = setup();

    renderHook(() => useThreadDetail("e1", "t1"), { wrapper });
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(detail).toHaveBeenCalledTimes(1);

    await act(() => vi.advanceTimersByTimeAsync(THREAD_DETAIL_POLL_MS));
    expect(detail).toHaveBeenCalledTimes(2);

    await act(() => vi.advanceTimersByTimeAsync(THREAD_DETAIL_POLL_MS));
    expect(detail).toHaveBeenCalledTimes(3);

    await act(() => vi.advanceTimersByTimeAsync(THREAD_DETAIL_POLL_MS * 5));
    expect(detail).toHaveBeenCalledTimes(3);
  });

  it("stops polling after a refetch failure even while cached data says running", async () => {
    detail
      .mockResolvedValueOnce(response("running"))
      .mockRejectedValueOnce(new Error("gone"))
      .mockResolvedValue(response("running"));
    const { wrapper } = setup();

    renderHook(() => useThreadDetail("e1", "t1"), { wrapper });
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(detail).toHaveBeenCalledTimes(1);

    await act(() => vi.advanceTimersByTimeAsync(THREAD_DETAIL_POLL_MS));
    expect(detail).toHaveBeenCalledTimes(2);

    await act(() => vi.advanceTimersByTimeAsync(THREAD_DETAIL_POLL_MS * 5));
    expect(detail).toHaveBeenCalledTimes(2);
  });
});

describe("useThreadDetail polling conditions", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  async function callsAfterTwoTicks(first: ReturnType<typeof detailWith>) {
    detail.mockResolvedValue(first);
    const { wrapper } = setup();
    renderHook(() => useThreadDetail("e1", "t1"), { wrapper });
    await act(() => vi.advanceTimersByTimeAsync(0));
    await act(() => vi.advanceTimersByTimeAsync(THREAD_DETAIL_POLL_MS * 2));
    return detail.mock.calls.length;
  }

  const item = { run_id: "r1", message_id: "m1", text: "q" };

  it("keeps polling while stopping", async () => {
    expect(await callsAfterTwoTicks(detailWith({ activity: makeActivity({ stopping: true }) }))).toBe(3);
  });

  it("keeps polling while a request awaits an answer", async () => {
    expect(
      await callsAfterTwoTicks(detailWith({ activity: makeActivity({ has_pending_requests: true }) })),
    ).toBe(3);
  });

  it("keeps polling while an unheld queue has work", async () => {
    expect(await callsAfterTwoTicks(detailWith({ queue: makeQueue({ items: [item] }) }))).toBe(3);
  });

  it("stops when idle with only a held queue", async () => {
    expect(
      await callsAfterTwoTicks(detailWith({ queue: makeQueue({ held: true, items: [item] }) })),
    ).toBe(1);
  });
});

describe("identity isolation", () => {
  it("does not read anything without a user", async () => {
    signOut();
    const { wrapper } = setup();
    renderHook(() => useThreadDetail("e1", "t1"), { wrapper });
    renderHook(() => useThreads(), { wrapper });
    await act(() => Promise.resolve());
    expect(detail).not.toHaveBeenCalled();
    expect(list).not.toHaveBeenCalled();
  });

  it("never shows user A's thread to user B, even when B's request 404s", async () => {
    const { client, wrapper } = setup();
    const stop = watchThreadsIdentity(client);
    detail.mockResolvedValueOnce(response("idle", "A-SECRET"));
    const seen: Array<string | undefined> = [];
    const { result } = renderHook(
      () => {
        const query = useThreadDetail("e1", "t1");
        seen.push(query.data?.thread.title);
        return query;
      },
      { wrapper },
    );
    await waitFor(() => expect(result.current.data?.thread.title).toBe("A-SECRET"));

    detail.mockRejectedValueOnce(new Error("404"));
    seen.length = 0;
    act(() => signOut());
    act(() => signInAs("B"));
    await waitFor(() => expect(result.current.isError).toBe(true));

    expect(result.current.data).toBeUndefined();
    expect(seen.every((title) => title === undefined)).toBe(true);
    expect(detail).toHaveBeenCalledTimes(2);
    stop();
  });

  it("aborts user A's in-flight request on logout and clears feature cache and filters", async () => {
    const { client, wrapper } = setup();
    const stop = watchThreadsIdentity(client);
    let signal!: AbortSignal;
    detail.mockImplementationOnce(
      (_e: string, _t: string, s: AbortSignal) => {
        signal = s;
        return new Promise(() => {});
      },
    );
    list.mockResolvedValue({ data: { items: [], environments: [] } });
    renderHook(() => useThreadDetail("e1", "t1"), { wrapper });
    const threads = renderHook(() => useThreads(), { wrapper });
    await waitFor(() => expect(signal).toBeDefined());
    await waitFor(() => expect(threads.result.current.data).toBeDefined());
    act(() => useThreadListStore.getState().setFilters({ query: "secret", project: "p" }));
    act(() => useThreadListStore.getState().loadMore());

    act(() => signOut());

    expect(signal.aborted).toBe(true);
    expect(client.getQueryCache().findAll().filter((q) => q.queryKey[1] === "A")).toHaveLength(0);
    expect(useThreadListStore.getState().filters.query).toBe("");
    expect(useThreadListStore.getState().limit).toBe(60);
    stop();
  });

  it("keeps the cache when the same user profile is updated", async () => {
    const { client, wrapper } = setup();
    const stop = watchThreadsIdentity(client);
    list.mockResolvedValue({ data: { items: [], environments: [] } });
    const threads = renderHook(() => useThreads(), { wrapper });
    await waitFor(() => expect(threads.result.current.data).toBeDefined());

    act(() => signInAs("A"));
    expect(client.getQueryCache().findAll()).toHaveLength(1);
    expect(threads.result.current.data).toBeDefined();
    stop();
  });
});

describe("useThreads degraded polling", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("re-reads while an environment is degraded and stops once it recovers", async () => {
    list
      .mockResolvedValueOnce({
        data: { items: [], environments: [{ id: "e1", status: "degraded" }] },
      })
      .mockResolvedValue({
        data: { items: [], environments: [{ id: "e1", status: "ok" }] },
      });
    const { wrapper } = setup();

    renderHook(() => useThreads(), { wrapper });
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(list).toHaveBeenCalledTimes(1);

    await act(() => vi.advanceTimersByTimeAsync(THREAD_LIST_DEGRADED_POLL_MS));
    expect(list).toHaveBeenCalledTimes(2);

    await act(() => vi.advanceTimersByTimeAsync(THREAD_LIST_DEGRADED_POLL_MS * 5));
    expect(list).toHaveBeenCalledTimes(2);
  });

  it("never polls while every environment is ok", async () => {
    list.mockResolvedValue({
      data: { items: [], environments: [{ id: "e1", status: "ok" }] },
    });
    const { wrapper } = setup();

    renderHook(() => useThreads(), { wrapper });
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(list).toHaveBeenCalledTimes(1);

    await act(() => vi.advanceTimersByTimeAsync(THREAD_LIST_DEGRADED_POLL_MS * 5));
    expect(list).toHaveBeenCalledTimes(1);
  });
});

describe("useCreateThread refresh decoupling", () => {
  const input = {
    command_id: "c1",
    thread_id: "t9",
    project_id: "p1",
    message: { message_id: "m1", text: "hi" },
    runtime_mode: "full-access",
    workspace: { type: "root" },
  } satisfies CreateThreadInput;

  // 列表查询必须在挂载中，invalidateQueries 才会真的发起重读。
  async function mountWithWarmList() {
    list.mockResolvedValue({ data: { items: [], environments: [] } });
    const { wrapper } = setup();
    const threads = renderHook(() => useThreads(), { wrapper });
    await waitFor(() => expect(threads.result.current.data).toBeDefined());
    expect(list).toHaveBeenCalledTimes(1);
    return { wrapper };
  }

  it("resolves as soon as the thread is accepted, without waiting for the list refresh", async () => {
    const { wrapper } = await mountWithWarmList();
    let releaseList!: (value: { data: { items: []; environments: [] } }) => void;
    list.mockImplementationOnce(
      () => new Promise((resolve) => (releaseList = resolve)),
    );
    create.mockResolvedValue({ thread_id: "t9", resumed: false });

    const { result } = renderHook(() => useCreateThread("e1"), { wrapper });
    const settled = await act(async () => result.current.mutateAsync(input));

    expect(settled.thread_id).toBe("t9");
    await waitFor(() => expect(list).toHaveBeenCalledTimes(2));
    // 列表重读仍在飞行，但创建已结算：按钮不再被刷新拖住。
    expect(result.current.isPending).toBe(false);
    releaseList({ data: { items: [], environments: [] } });
  });

  it("still refreshes the list after a failure so an accepted write gets reconciled", async () => {
    const { wrapper } = await mountWithWarmList();
    create.mockRejectedValue(new Error("timeout"));

    const { result } = renderHook(() => useCreateThread("e1"), { wrapper });
    await act(async () => {
      await expect(result.current.mutateAsync(input)).rejects.toThrow("timeout");
    });

    await waitFor(() => expect(list).toHaveBeenCalledTimes(2));
  });
});

describe("useThreadItemDetail body isolation", () => {
  type Target = { env: string; thread: string; item: string; revision: string };
  const A: Target = { env: "e1", thread: "t1", item: "i1", revision: "r1" };

  function body(text: string): { data: ThreadItem } {
    return { data: { id: "i1", type: "command_execution", text, summary: "", detail_available: false } };
  }

  function mount(wrapper: ReturnType<typeof setup>["wrapper"]) {
    const seen: Array<string | undefined> = [];
    const view = renderHook(
      ({ target }: { target: Target }) => {
        const query = useThreadItemDetail(target.env, target.thread, target.item, target.revision, true);
        seen.push(query.data?.text);
        return query;
      },
      { wrapper, initialProps: { target: A } },
    );
    return { ...view, seen };
  }

  it("never shows user A's body to user B on the same observer while B is pending", async () => {
    const { client, wrapper } = setup();
    const stop = watchThreadsIdentity(client);
    itemDetail.mockResolvedValueOnce(body("A-SECRET"));
    const { result, seen } = mount(wrapper);
    await waitFor(() => expect(result.current.data?.text).toBe("A-SECRET"));

    itemDetail.mockImplementationOnce(() => new Promise(() => {}));
    seen.length = 0;
    act(() => signOut());
    act(() => signInAs("B"));
    await waitFor(() => expect(itemDetail).toHaveBeenCalledTimes(2));

    expect(result.current.isPending).toBe(true);
    expect(seen).not.toContain("A-SECRET");
    stop();
  });

  it("never shows user A's body to user B when B's request 404s", async () => {
    const { client, wrapper } = setup();
    const stop = watchThreadsIdentity(client);
    itemDetail.mockResolvedValueOnce(body("A-SECRET"));
    const { result, seen } = mount(wrapper);
    await waitFor(() => expect(result.current.data?.text).toBe("A-SECRET"));

    itemDetail.mockRejectedValueOnce(new Error("404"));
    seen.length = 0;
    act(() => signOut());
    act(() => signInAs("B"));
    await waitFor(() => expect(result.current.isError).toBe(true));

    expect(result.current.data).toBeUndefined();
    expect(seen).not.toContain("A-SECRET");
    stop();
  });

  it.each<[string, Partial<Target>]>([
    ["environment", { env: "e2" }],
    ["thread", { thread: "t2" }],
    ["item", { item: "i2" }],
  ])("does not reuse the body across a different %s", async (_name, change) => {
    const { wrapper } = setup();
    itemDetail.mockResolvedValueOnce(body("A-BODY"));
    const { result, rerender, seen } = mount(wrapper);
    await waitFor(() => expect(result.current.data?.text).toBe("A-BODY"));

    itemDetail.mockImplementationOnce(() => new Promise(() => {}));
    seen.length = 0;
    rerender({ target: { ...A, ...change } });
    await waitFor(() => expect(itemDetail).toHaveBeenCalledTimes(2));

    expect(result.current.isPending).toBe(true);
    expect(seen).not.toContain("A-BODY");
  });

  it("does not re-request the body when rerendered with the same revision", async () => {
    const { wrapper } = setup();
    itemDetail.mockResolvedValue(body("OUT-v1"));
    const { result, rerender } = mount(wrapper);
    await waitFor(() => expect(result.current.data?.text).toBe("OUT-v1"));

    rerender({ target: { ...A } });
    await waitFor(() => expect(result.current.isFetching).toBe(false));
    expect(itemDetail).toHaveBeenCalledTimes(1);
    expect(result.current.data?.text).toBe("OUT-v1");
  });

  it("keeps the old body while the same item's new revision loads", async () => {
    const { wrapper } = setup();
    itemDetail.mockResolvedValueOnce(body("OUT-v1"));
    const { result, rerender } = mount(wrapper);
    await waitFor(() => expect(result.current.data?.text).toBe("OUT-v1"));

    let resolve!: (value: { data: ThreadItem }) => void;
    itemDetail.mockReturnValueOnce(new Promise((r) => (resolve = r)));
    rerender({ target: { ...A, revision: "r2" } });
    await waitFor(() => expect(itemDetail).toHaveBeenCalledTimes(2));
    expect(result.current.data?.text).toBe("OUT-v1");

    await act(async () => resolve(body("OUT-v2")));
    await waitFor(() => expect(result.current.data?.text).toBe("OUT-v2"));
  });
});
