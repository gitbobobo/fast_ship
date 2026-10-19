import type { ReactNode } from "react";
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  useReviewProjects,
  useReviewRecords,
  useReviewSettings,
  useRerunReview,
  useUpdateReviewSettings,
  watchReviewsIdentity,
} from "@/lib/hooks/use-reviews";
import { signInAs, teardownAuth } from "@/test/auth";

const api = vi.hoisted(() => ({
  listProjects: vi.fn(),
  getSettings: vi.fn(),
  updateSettings: vi.fn(),
  listRecords: vi.fn(),
  rerun: vi.fn(),
}));

vi.mock("@/lib/api/reviews", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/reviews")>()),
  reviewApi: api,
}));

function setup() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  return { client, wrapper };
}

const settings = {
  project_id: "p1",
  enabled: false,
  trigger_opened: true,
  trigger_synchronize: true,
  trigger_ready_for_review: false,
  trigger_reopened: false,
  code_combinations: [],
  visual_combinations: [],
  prerequisites: [],
  can_manage: true,
};

beforeEach(() => {
  signInAs("A");
  api.listProjects.mockResolvedValue({ data: { items: [] } });
  api.getSettings.mockResolvedValue({ data: settings });
  api.updateSettings.mockResolvedValue({ data: settings });
  api.listRecords.mockResolvedValue({ data: { items: [] } });
  api.rerun.mockResolvedValue({
    data: { accepted: true, head_sha: "x", round_number: 2 },
  });
});

afterEach(() => {
  teardownAuth();
  for (const fn of Object.values(api)) fn.mockReset();
});

describe("use-reviews query keys", () => {
  it("projects/settings/records 的 key 挂在 [reviews, userId] 下", async () => {
    const { client, wrapper } = setup();

    const { result: projects } = renderHook(() => useReviewProjects(), {
      wrapper,
    });
    await waitFor(() => expect(projects.current.isSuccess).toBe(true));
    expect(
      client.getQueryCache().find({ queryKey: ["reviews", "A", "projects"] }),
    ).toBeTruthy();

    const { result: settingsResult } = renderHook(
      () => useReviewSettings("p1"),
      { wrapper },
    );
    await waitFor(() => expect(settingsResult.current.isSuccess).toBe(true));
    expect(
      client
        .getQueryCache()
        .find({ queryKey: ["reviews", "A", "settings", "p1"] }),
    ).toBeTruthy();

    const { result: records } = renderHook(
      () => useReviewRecords("p1", 5),
      { wrapper },
    );
    await waitFor(() => expect(records.current.isSuccess).toBe(true));
    expect(api.listRecords).toHaveBeenCalledWith("p1", 5);
    expect(
      client
        .getQueryCache()
        .find({ queryKey: ["reviews", "A", "records", "p1", 5] }),
    ).toBeTruthy();
  });
});

describe("use-reviews invalidate", () => {
  it("保存设置后失效 [reviews, userId] 前缀", async () => {
    const { client, wrapper } = setup();
    const { result: settingsResult } = renderHook(
      () => useReviewSettings("p1"),
      { wrapper },
    );
    await waitFor(() => expect(settingsResult.current.isSuccess).toBe(true));

    const invalidate = vi.spyOn(client, "invalidateQueries");
    const { result: update } = renderHook(
      () => useUpdateReviewSettings("p1"),
      { wrapper },
    );
    await act(() =>
      update.current.mutateAsync({
        enabled: true,
        trigger_opened: true,
        trigger_synchronize: true,
        trigger_ready_for_review: false,
        trigger_reopened: false,
        code_combinations: [],
        visual_combinations: [],
      }),
    );

    expect(invalidate).toHaveBeenCalledWith({
      queryKey: ["reviews", "A"],
    });
  });

  it("重新审查后失效 records 前缀", async () => {
    const { client, wrapper } = setup();
    const invalidate = vi.spyOn(client, "invalidateQueries");
    const { result } = renderHook(() => useRerunReview("p1"), { wrapper });

    await act(() => result.current.mutateAsync(12));

    expect(api.rerun).toHaveBeenCalledWith("p1", 12);
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: ["reviews", "A"],
    });
  });
});

describe("watchReviewsIdentity", () => {
  it("用户切换时清空 reviews 缓存", async () => {
    const { client, wrapper } = setup();
    const stop = watchReviewsIdentity(client);

    const { result, unmount } = renderHook(() => useReviewProjects(), {
      wrapper,
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(
      client.getQueryCache().findAll({ queryKey: ["reviews"] }).length,
    ).toBeGreaterThan(0);
    // 先卸载 observer：仍在挂载时它会立刻以新用户重建缓存项（这是隔离的正确表现）。
    unmount();

    act(() => signInAs("B"));

    expect(
      client.getQueryCache().findAll({ queryKey: ["reviews"] }),
    ).toHaveLength(0);
    stop();
  });
});
