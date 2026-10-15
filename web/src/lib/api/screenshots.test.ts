import { beforeEach, describe, expect, it, vi } from "vitest";

const authState = {
  token: null as string | null,
};

vi.mock("@/lib/store/auth-store", () => ({
  useAuthStore: {
    getState: () => authState,
  },
}));

const screen = {
  id: "scr-1",
  content_url: "/api/screenshot-screens/scr-1/content",
};

describe("screenshotApi.contentUrl", () => {
  beforeEach(() => {
    authState.token = null;
  });

  it("returns the content path without token when logged out", async () => {
    const { screenshotApi } = await import("./screenshots");

    expect(screenshotApi.contentUrl(screen)).toBe(
      "/api/screenshot-screens/scr-1/content",
    );
  });

  it("appends the auth token for <img> requests", async () => {
    authState.token = "jwt-token";
    const { screenshotApi } = await import("./screenshots");

    expect(screenshotApi.contentUrl(screen)).toBe(
      "/api/screenshot-screens/scr-1/content?token=jwt-token",
    );
  });

  it("falls back to the id-based path when content_url is empty", async () => {
    authState.token = "jwt-token";
    const { screenshotApi } = await import("./screenshots");

    expect(
      screenshotApi.contentUrl({ id: "scr-9", content_url: "" }),
    ).toBe("/api/screenshot-screens/scr-9/content?token=jwt-token");
  });
});
