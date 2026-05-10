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
});
