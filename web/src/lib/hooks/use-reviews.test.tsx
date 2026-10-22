import type { ReactNode } from "react";
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  REVIEW_POLL_INTERVAL,
  useReviewProjects,
  useReviewPullRequestDetail,
  useReviewPullRequests,
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
  listPullRequests: vi.fn(),
  getPullRequestDetail: vi.fn(),
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
  repo_full_name: "acme/alpha",
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

const emptyActivity = (inProgress = 0) => ({
  data: {
    items: [],
    counts: {
      in_progress: inProgress,
      needs_action: 0,
      recently_passed: 0,
    },
    total: 0,
  },
});

const prDetail = {
  repo_full_name: "acme/alpha",
  pr_number: 12,
  pr_title: "修复登录",
  pr_url: "https://github.com/acme/alpha/pull/12",
  head_sha: "abcdef123456",
  updated_at: "2026-10-20T00:00:00Z",
  can_rerun: true,
  rounds: [],
};

beforeEach(() => {
  signInAs("A");
  api.listProjects.mockResolvedValue({ data: { items: [] } });
  api.getSettings.mockResolvedValue({ data: settings });
  api.updateSettings.mockResolvedValue({ data: settings });
  api.listPullRequests.mockResolvedValue(emptyActivity());
  api.getPullRequestDetail.mockResolvedValue({ data: prDetail });
  api.rerun.mockResolvedValue({
    data: { accepted: true, head_sha: "x", round_number: 2 },
  });
});

afterEach(() => {
  teardownAuth();
  vi.useRealTimers();
  for (const fn of Object.values(api)) fn.mockReset();
});

describe("use-reviews query keys", () => {
  it("projects/settings/pull-requests/pull-request 的 key 挂在 [reviews, userId] 下", async () => {
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

    const { result: activity } = renderHook(
      () => useReviewPullRequests("p1", 5),
      { wrapper },
    );
    await waitFor(() => expect(activity.current.isSuccess).toBe(true));
    expect(api.listPullRequests).toHaveBeenCalledWith({
      projectId: "p1",
      limit: 5,
      offset: 0,
    });
    expect(
      client
        .getQueryCache()
        .find({ queryKey: ["reviews", "A", "pull-requests", "p1", 5, 0] }),
    ).toBeTruthy();

    const { result: allProjects } = renderHook(
      () => useReviewPullRequests(),
      { wrapper },
    );
    await waitFor(() => expect(allProjects.current.isSuccess).toBe(true));
    expect(api.listPullRequests).toHaveBeenCalledWith({
      projectId: undefined,
      limit: 50,
      offset: 0,
    });
    expect(
      client
        .getQueryCache()
        .find({ queryKey: ["reviews", "A", "pull-requests", "", 50, 0] }),
    ).toBeTruthy();

    const { result: detail } = renderHook(
      () => useReviewPullRequestDetail("p1", 12),
      { wrapper },
    );
    await waitFor(() => expect(detail.current.isSuccess).toBe(true));
    expect(api.getPullRequestDetail).toHaveBeenCalledWith("p1", 12);
    expect(
      client
        .getQueryCache()
        .find({ queryKey: ["reviews", "A", "pull-request", "p1", 12] }),
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

    // 列表与详情同挂在 [reviews, userId] 下，一次失效同时覆盖两者。
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: ["reviews", "A"],
    });
  });

  it("重新审查后失效 [reviews, userId] 前缀（覆盖列表与详情）", async () => {
    const { client, wrapper } = setup();
    const invalidate = vi.spyOn(client, "invalidateQueries");
    const { result } = renderHook(() => useRerunReview("p1"), { wrapper });

    await act(() =>
      result.current.mutateAsync({ prNumber: 12, repoFullName: "acme/alpha" }),
    );

    expect(api.rerun).toHaveBeenCalledWith("p1", 12, "acme/alpha");
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: ["reviews", "A"],
    });
  });
});

describe("use-reviews 轮询开关", () => {
  it("活动响应 counts.in_progress>0 时按间隔自动刷新", async () => {
    vi.useFakeTimers();
    api.listPullRequests.mockResolvedValue(emptyActivity(1));
    const { wrapper } = setup();

    renderHook(() => useReviewPullRequests(), { wrapper });
    // act 的 await 刷新微任务：首轮请求落地后才会按数据里的 counts 排定轮询。
    await act(async () => {});
    expect(api.listPullRequests).toHaveBeenCalledTimes(1);

    await act(async () => {
      vi.advanceTimersByTime(REVIEW_POLL_INTERVAL);
    });
    expect(api.listPullRequests).toHaveBeenCalledTimes(2);

    await act(async () => {
      vi.advanceTimersByTime(REVIEW_POLL_INTERVAL);
    });
    expect(api.listPullRequests).toHaveBeenCalledTimes(3);
  });

  it("活动响应 counts.in_progress=0 时不轮询", async () => {
    vi.useFakeTimers();
    api.listPullRequests.mockResolvedValue(emptyActivity(0));
    const { wrapper } = setup();

    renderHook(() => useReviewPullRequests(), { wrapper });
    await act(async () => {});
    expect(api.listPullRequests).toHaveBeenCalledTimes(1);

    await act(async () => {
      vi.advanceTimersByTime(REVIEW_POLL_INTERVAL * 3);
    });
    expect(api.listPullRequests).toHaveBeenCalledTimes(1);
  });

  it("抽屉打开期间详情每 10s 轮询，卸载后停止", async () => {
    vi.useFakeTimers();
    const { wrapper } = setup();

    const { unmount } = renderHook(
      () => useReviewPullRequestDetail("p1", 12),
      { wrapper },
    );
    await act(async () => {});
    expect(api.getPullRequestDetail).toHaveBeenCalledTimes(1);

    await act(async () => {
      vi.advanceTimersByTime(REVIEW_POLL_INTERVAL);
    });
    expect(api.getPullRequestDetail).toHaveBeenCalledTimes(2);

    await act(async () => {
      vi.advanceTimersByTime(REVIEW_POLL_INTERVAL);
    });
    await act(async () => {
      vi.advanceTimersByTime(REVIEW_POLL_INTERVAL);
    });
    expect(api.getPullRequestDetail).toHaveBeenCalledTimes(4);

    unmount();
    await act(async () => {
      vi.advanceTimersByTime(REVIEW_POLL_INTERVAL * 3);
    });
    expect(api.getPullRequestDetail).toHaveBeenCalledTimes(4);
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
