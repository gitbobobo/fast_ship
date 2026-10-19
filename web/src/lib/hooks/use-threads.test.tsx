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
  applyThreadStreamEvent,
  mergeThreadSummary,
  useCreateThread,
  useThreadDetail,
  useThreadItemDetail,
  useThreads,
  useThreadStream,
  watchThreadsIdentity,
} from "@/lib/hooks/use-threads";
import type { ThreadStreamEvent } from "@/lib/thread-stream";
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

const stream = vi.hoisted(() => ({
  close: vi.fn(),
  subscribe: vi.fn(),
  captured: null as null | {
    onEvent: (event: ThreadStreamEvent) => void;
    onStatus?: (status: string, detail?: unknown) => void;
  },
}));

vi.mock("@/lib/thread-stream", () => ({
  subscribeThreadStream: (options: never) => {
    stream.captured = options as (typeof stream)["captured"];
    stream.subscribe(options);
    return { close: stream.close };
  },
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

function setup() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  return { client, wrapper };
}

beforeEach(() => {
  signInAs("A");
  stream.captured = null;
});
afterEach(() => {
  teardownAuth();
  create.mockReset();
  detail.mockReset();
  itemDetail.mockReset();
  list.mockReset();
  stream.close.mockClear();
  stream.subscribe.mockClear();
  useThreadListStore.getState().resetFilters();
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

describe("applyThreadStreamEvent", () => {
  const listData = { data: { items: [], environments: [] } };

  // 查询必须在挂载中，invalidateQueries 才会真的发起重读；detail mock 的
  // 前两参是 (environmentId, threadId)，按调用参数区分重读对象。
  async function mountQueries() {
    list.mockResolvedValue(listData);
    detail.mockResolvedValue(response("idle"));
    const { client, wrapper } = setup();
    renderHook(() => useThreads(), { wrapper });
    renderHook(() => useThreadDetail("e1", "t1"), { wrapper });
    renderHook(() => useThreadDetail("e1", "t2"), { wrapper });
    renderHook(() => useThreadDetail("e2", "t3"), { wrapper });
    await waitFor(() => expect(detail).toHaveBeenCalledTimes(3));
    await waitFor(() => expect(list).toHaveBeenCalledTimes(1));
    return { client };
  }

  const detailRefetches = () =>
    detail.mock.calls.slice(3).map(([env, thread]) => `${env}/${thread}`);

  it("invalidates the whole user subtree on an event without environment_id", async () => {
    const { client } = await mountQueries();

    act(() => applyThreadStreamEvent(client, "A", { full: true }));

    await waitFor(() => expect(list).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(detail).toHaveBeenCalledTimes(6));
    expect(detailRefetches().sort()).toEqual(["e1/t1", "e1/t2", "e2/t3"]);
  });

  it("invalidates the list and every environment detail when state changes", async () => {
    const { client } = await mountQueries();

    // 断连恢复时 state 与同环境的线程增量可能合并在同一条事件里：断连期间
    // 失败的详情重读必须全部重试，不能只看 thread_ids。
    act(() =>
      applyThreadStreamEvent(client, "A", {
        environment_id: "e1",
        thread_ids: ["t9"],
        state: "ready",
      }),
    );

    await waitFor(() => expect(list).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(detail).toHaveBeenCalledTimes(5));
    expect(detailRefetches().sort()).toEqual(["e1/t1", "e1/t2"]);
  });

  it("invalidates all of an environment's details on full, not other environments", async () => {
    const { client } = await mountQueries();

    act(() =>
      applyThreadStreamEvent(client, "A", { environment_id: "e1", full: true }),
    );

    await waitFor(() => expect(detail).toHaveBeenCalledTimes(5));
    expect(detailRefetches().sort()).toEqual(["e1/t1", "e1/t2"]);
  });

  it("invalidates only the listed threads of the environment", async () => {
    const { client } = await mountQueries();

    act(() =>
      applyThreadStreamEvent(client, "A", {
        environment_id: "e1",
        thread_ids: ["t2"],
      }),
    );

    await waitFor(() => expect(detail).toHaveBeenCalledTimes(4));
    expect(detailRefetches()).toEqual(["e1/t2"]);
  });

  it("refetches when an event arrives during the very first load", async () => {
    // 首次加载（还没有 data）时 TanStack 会复用在飞 promise 并清掉失效标记；
    // 事件若发生在服务端取完旧快照之后、响应到达之前，旧数据会被永久留下。
    let resolveFirst!: (value: typeof listData) => void;
    list.mockResolvedValue(listData);
    list.mockImplementationOnce(
      () => new Promise<typeof listData>((resolve) => (resolveFirst = resolve)),
    );
    const { client, wrapper } = setup();
    renderHook(() => useThreads(), { wrapper });
    await waitFor(() => expect(list).toHaveBeenCalledTimes(1));

    act(() => applyThreadStreamEvent(client, "A", { full: true }));

    await waitFor(() => expect(list).toHaveBeenCalledTimes(2));
    // 迟到的首个响应不得覆盖重读结果（取消后它已被丢弃）。
    resolveFirst(listData);
    await act(() => Promise.resolve());
  });

  it("treats a missing thread_ids as every thread of the environment", async () => {
    const { client } = await mountQueries();

    act(() =>
      applyThreadStreamEvent(client, "A", { environment_id: "e2", state: "stale" }),
    );

    await waitFor(() => expect(detail).toHaveBeenCalledTimes(4));
    expect(detailRefetches()).toEqual(["e2/t3"]);
  });
});

describe("useThreadStream", () => {
  it("subscribes while signed in and closes on unmount", () => {
    const { client, wrapper } = setup();
    const view = renderHook(() => useThreadStream(client), { wrapper });
    expect(stream.subscribe).toHaveBeenCalledTimes(1);
    expect(stream.captured?.onEvent).toBeTypeOf("function");

    view.unmount();
    expect(stream.close).toHaveBeenCalledTimes(1);
  });

  it("never subscribes without a user, and rebuilds on user change", async () => {
    signOut();
    const { client, wrapper } = setup();
    renderHook(() => useThreadStream(client), { wrapper });
    await act(() => Promise.resolve());
    expect(stream.subscribe).not.toHaveBeenCalled();

    act(() => signInAs("A"));
    expect(stream.subscribe).toHaveBeenCalledTimes(1);

    act(() => signInAs("B"));
    // 换用户：旧订阅关闭、新订阅建立，互不泄漏。
    expect(stream.close).toHaveBeenCalledTimes(1);
    expect(stream.subscribe).toHaveBeenCalledTimes(2);
  });

  it("routes pushed events into query invalidation", async () => {
    list.mockResolvedValue({ data: { items: [], environments: [] } });
    detail.mockResolvedValue(response("idle"));
    const { client, wrapper } = setup();
    renderHook(() => useThreads(), { wrapper });
    renderHook(() => useThreadDetail("e1", "t1"), { wrapper });
    renderHook(() => useThreadStream(client), { wrapper });
    await waitFor(() => expect(list).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(detail).toHaveBeenCalledTimes(1));

    act(() =>
      stream.captured?.onEvent({ environment_id: "e1", thread_ids: ["t1"] }),
    );

    await waitFor(() => expect(list).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(detail).toHaveBeenCalledTimes(2));
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

describe("useThreadDetail list sync", () => {
  it("writes the fetched thread summary into the list cache", async () => {
    const { client, wrapper } = setup();
    const threadsKey = ["t3-threads", "A", "threads"];
    client.setQueryData(threadsKey, {
      items: [{ id: "t1", environment_id: "e1", status: "running" }],
      environments: [],
    });
    detail.mockResolvedValue({
      data: {
        ...response("idle").data,
        thread: {
          id: "t1",
          environment_id: "e1",
          status: "idle",
        } as ThreadDetail["thread"],
      },
    });

    const { result } = renderHook(() => useThreadDetail("e1", "t1"), { wrapper });
    await waitFor(() => expect(result.current.data?.thread.status).toBe("idle"));

    const list = client.getQueryData<{ items: { id: string; status: string }[] }>(
      threadsKey,
    );
    expect(list?.items[0]).toMatchObject({ id: "t1", status: "idle" });
  });
});

describe("mergeThreadSummary", () => {
  it("writes the fetched detail summary back into the cached list", () => {
    const { client } = setup();
    const threadsKey = ["t3-threads", "u1", "threads"];
    client.setQueryData(threadsKey, {
      items: [
        { id: "t1", environment_id: "e1", status: "running", updated_at: "1" },
        { id: "t2", environment_id: "e1", status: "idle", updated_at: "2" },
      ],
      environments: [],
    });

    mergeThreadSummary(client, "u1", {
      id: "t1",
      environment_id: "e1",
      status: "idle",
      updated_at: "3",
    } as ThreadDetail["thread"]);

    const list = client.getQueryData<{ items: ThreadDetail["thread"][] }>(threadsKey);
    expect(list?.items[0]).toMatchObject({ id: "t1", status: "idle", updated_at: "3" });
    // 其它线程原样保留。
    expect(list?.items[1]).toMatchObject({ id: "t2", status: "idle", updated_at: "2" });
  });

  it("does not let an older detail summary overwrite a newer list item", () => {
    const { client } = setup();
    const threadsKey = ["t3-threads", "u1", "threads"];
    client.setQueryData(threadsKey, {
      items: [
        { id: "t1", environment_id: "e1", status: "running", updated_at: "2026-10-19T10:00:00Z" },
      ],
      environments: [],
    });

    // 迟到/更旧的详情快照（例如详情读的是更早的 shell 快照）。
    mergeThreadSummary(client, "u1", {
      id: "t1",
      environment_id: "e1",
      status: "idle",
      updated_at: "2026-10-19T09:59:00Z",
    } as ThreadDetail["thread"]);
    expect(
      client.getQueryData<{ items: ThreadDetail["thread"][] }>(threadsKey)?.items[0]
        .status,
    ).toBe("running");

    // 同一时刻或更新时正常覆盖。
    mergeThreadSummary(client, "u1", {
      id: "t1",
      environment_id: "e1",
      status: "idle",
      updated_at: "2026-10-19T10:00:00Z",
    } as ThreadDetail["thread"]);
    expect(
      client.getQueryData<{ items: ThreadDetail["thread"][] }>(threadsKey)?.items[0]
        .status,
    ).toBe("idle");
  });

  it("ignores threads from another environment and a missing list cache", () => {
    const { client } = setup();
    const threadsKey = ["t3-threads", "u1", "threads"];
    client.setQueryData(threadsKey, {
      items: [{ id: "t1", environment_id: "e1", status: "running" }],
      environments: [],
    });

    mergeThreadSummary(client, "u1", {
      id: "t1",
      environment_id: "e2",
      status: "idle",
    } as ThreadDetail["thread"]);
    expect(
      client.getQueryData<{ items: ThreadDetail["thread"][] }>(threadsKey)?.items[0]
        .status,
    ).toBe("running");

    expect(() =>
      mergeThreadSummary(client, "u2", {
        id: "t1",
        environment_id: "e1",
        status: "idle",
      } as ThreadDetail["thread"]),
    ).not.toThrow();
  });
});

describe("useThreads detail overlay", () => {
  it("keeps a newer cached detail summary when the list response is older", async () => {
    const { client, wrapper } = setup();
    // 详情已经拿到「空闲」的较新摘要
    client.setQueryData(["t3-threads", "A", "thread", "e1", "t1"], {
      thread: {
        id: "t1",
        environment_id: "e1",
        status: "idle",
        updated_at: "2026-10-19T10:05:00Z",
      },
      items: [],
      queue: makeQueue(),
      activity: makeActivity(),
      access: "operate",
    });
    // 列表响应是状态变化前的快照（updated_at 更早）
    list.mockResolvedValue({
      data: {
        items: [
          {
            id: "t1",
            environment_id: "e1",
            status: "running",
            updated_at: "2026-10-19T10:00:00Z",
          },
        ],
        environments: [],
      },
    });

    const { result } = renderHook(() => useThreads(), { wrapper });
    await waitFor(() => expect(result.current.data?.items).toHaveLength(1));
    expect(result.current.data?.items[0].status).toBe("idle");
  });

  it("leaves the list response alone when the cached detail is older", async () => {
    const { client, wrapper } = setup();
    client.setQueryData(["t3-threads", "A", "thread", "e1", "t1"], {
      thread: {
        id: "t1",
        environment_id: "e1",
        status: "idle",
        updated_at: "2026-10-19T09:59:00Z",
      },
      items: [],
      queue: makeQueue(),
      activity: makeActivity(),
      access: "operate",
    });
    list.mockResolvedValue({
      data: {
        items: [
          {
            id: "t1",
            environment_id: "e1",
            status: "running",
            updated_at: "2026-10-19T10:00:00Z",
          },
        ],
        environments: [],
      },
    });

    const { result } = renderHook(() => useThreads(), { wrapper });
    await waitFor(() => expect(result.current.data?.items).toHaveLength(1));
    expect(result.current.data?.items[0].status).toBe("running");
  });
});
