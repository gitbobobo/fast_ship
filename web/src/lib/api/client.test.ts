import { beforeEach, describe, expect, it, vi } from "vitest";

const kyMock = vi.fn();
const kyCreateMock = vi.fn();
const refreshPostMock = vi.fn();

const authState = {
  token: "access-token",
  refreshToken: "refresh-token",
  setToken: vi.fn(),
  setRefreshToken: vi.fn(),
  logout: vi.fn(),
};

vi.mock("ky", () => {
  const ky = Object.assign(kyMock, { create: kyCreateMock });
  return { default: ky };
});

vi.mock("@/lib/store/auth-store", () => ({
  useAuthStore: {
    getState: () => authState,
  },
}));

function prepareKyMocks() {
  kyCreateMock.mockReset();
  kyCreateMock
    .mockReturnValueOnce({ post: refreshPostMock })
    .mockReturnValueOnce(kyMock);
}

describe("api client auth retry", () => {
  beforeEach(() => {
    vi.resetModules();
    kyMock.mockReset();
    refreshPostMock.mockReset();
    authState.setToken.mockReset();
    authState.setRefreshToken.mockReset();
    authState.logout.mockReset();
    authState.token = "access-token";
    authState.refreshToken = "refresh-token";
    prepareKyMocks();
  });

  it("updates both tokens after a successful refresh", async () => {
    refreshPostMock.mockReturnValue({
      json: vi.fn().mockResolvedValue({
        data: {
          token: "new-access-token",
          refresh_token: "new-refresh-token",
        },
      }),
    });

    const { tryRefreshToken } = await import("./client");

    await expect(tryRefreshToken()).resolves.toBe("new-access-token");
    expect(refreshPostMock).toHaveBeenCalledWith("auth/refresh", {
      json: { refresh_token: "refresh-token" },
    });
    expect(authState.setToken).toHaveBeenCalledWith("new-access-token");
    expect(authState.setRefreshToken).toHaveBeenCalledWith("new-refresh-token");
  });

  it("marks retried 401 requests so they do not loop forever", async () => {
    refreshPostMock.mockReturnValue({
      json: vi.fn().mockResolvedValue({
        data: {
          token: "new-access-token",
          refresh_token: "new-refresh-token",
        },
      }),
    });
    kyMock.mockResolvedValue("retried-response");

    await import("./client");
    const apiConfig = kyCreateMock.mock.calls[1][0];
    const afterResponse = apiConfig.hooks.afterResponse[0];
    const request = new Request("http://localhost/api/projects");

    const result = await afterResponse(request, {}, new Response(null, { status: 401 }));

    expect(result).toBe("retried-response");
    expect(kyMock).toHaveBeenCalledTimes(1);
    const retriedRequest = kyMock.mock.calls[0][0] as Request;
    expect(retriedRequest.headers.get("x-fast-ship-refresh-retried")).toBe("1");
  });

  it("skips retry when the request was already retried once", async () => {
    await import("./client");
    const apiConfig = kyCreateMock.mock.calls[1][0];
    const afterResponse = apiConfig.hooks.afterResponse[0];
    const request = new Request("http://localhost/api/projects", {
      headers: { "x-fast-ship-refresh-retried": "1" },
    });

    const result = await afterResponse(request, {}, new Response(null, { status: 401 }));

    expect(result).toBeUndefined();
    expect(kyMock).not.toHaveBeenCalled();
    expect(refreshPostMock).not.toHaveBeenCalled();
  });

  // 改密续签窗口：服务端已吊销旧凭证、新凭证随后写进 store；并发请求的旧
  // token 401 应等待轮换并直接用新 token 重试，绝不能拿旧 refresh token 走
  // 刷新（它已被吊销，失败会 logout 弹走发起改密的浏览器）。
  it("waits for credential rotation and retries a stale-token 401 with the rotated token", async () => {
    kyMock.mockResolvedValue("retried-response");
    authState.token = "rotated-access-token"; // 轮换落地后 store 持有的新 token

    const { withCredentialRotation } = await import("./client");
    const apiConfig = kyCreateMock.mock.calls[1][0];
    const afterResponse = apiConfig.hooks.afterResponse[0];
    const request = new Request("http://localhost/api/notifications/unread-count", {
      headers: { Authorization: "Bearer stale-access-token" },
    });

    let resolveRotation: () => void = () => {};
    const rotation = withCredentialRotation(
      () => new Promise<void>((resolve) => { resolveRotation = resolve; }),
    );

    const resultPromise = afterResponse(
      request,
      {},
      new Response(null, { status: 401 }),
    );

    await Promise.resolve();
    expect(refreshPostMock).not.toHaveBeenCalled();
    expect(kyMock).not.toHaveBeenCalled();

    resolveRotation();
    const result = await resultPromise;
    await rotation;

    expect(result).toBe("retried-response");
    const retriedRequest = kyMock.mock.calls[0][0] as Request;
    expect(retriedRequest.headers.get("Authorization")).toBe(
      "Bearer rotated-access-token",
    );
    expect(refreshPostMock).not.toHaveBeenCalled();
  });

  // 轮换发起请求自身（带 ROTATION_OWNER_HEADER）拿到 401 时绝不能等待
  // credentialRotation——它等待的正是自己，否则死锁。
  it("does not let the rotation-owning request await its own rotation", async () => {
    refreshPostMock.mockReturnValue({
      json: vi.fn().mockResolvedValue({
        data: { token: "refreshed-token", refresh_token: "new-refresh" },
      }),
    });
    kyMock.mockResolvedValue("retried-response");

    const { withCredentialRotation, ROTATION_OWNER_HEADER } = await import("./client");
    const apiConfig = kyCreateMock.mock.calls[1][0];
    const afterResponse = apiConfig.hooks.afterResponse[0];
    const request = new Request("http://localhost/api/auth/password", {
      method: "PUT",
      headers: {
        Authorization: "Bearer access-token",
        [ROTATION_OWNER_HEADER]: "1",
      },
    });

    // 挂起轮换窗口：若钩子误等自己，resultPromise 永远不会 settle。
    let resolveRotation: () => void = () => {};
    const rotation = withCredentialRotation(
      () => new Promise<void>((resolve) => { resolveRotation = resolve; }),
    );

    const resultPromise = afterResponse(
      request,
      {},
      new Response(null, { status: 401 }),
    );
    const result = await Promise.race([
      resultPromise,
      new Promise((_, reject) => setTimeout(() => reject(new Error("deadlock")), 1000)),
    ]);

    expect(result).toBe("retried-response");
    expect(refreshPostMock).toHaveBeenCalledWith("auth/refresh", {
      json: { refresh_token: "refresh-token" },
    });
    const retriedRequest = kyMock.mock.calls[0][0] as Request;
    expect(retriedRequest.headers.get("Authorization")).toBe("Bearer refreshed-token");

    resolveRotation();
    await rotation;
  });

  it("still refreshes when the 401 response came back on the current token", async () => {
    refreshPostMock.mockReturnValue({
      json: vi.fn().mockResolvedValue({
        data: {
          token: "refreshed-token",
          refresh_token: "new-refresh-token",
        },
      }),
    });
    kyMock.mockResolvedValue("retried-response");

    await import("./client");
    const apiConfig = kyCreateMock.mock.calls[1][0];
    const afterResponse = apiConfig.hooks.afterResponse[0];
    const request = new Request("http://localhost/api/projects", {
      headers: { Authorization: "Bearer access-token" },
    });

    const result = await afterResponse(request, {}, new Response(null, { status: 401 }));

    expect(refreshPostMock).toHaveBeenCalledWith("auth/refresh", {
      json: { refresh_token: "refresh-token" },
    });
    expect(result).toBe("retried-response");
    const retriedRequest = kyMock.mock.calls[0][0] as Request;
    expect(retriedRequest.headers.get("Authorization")).toBe("Bearer refreshed-token");
  });
});
