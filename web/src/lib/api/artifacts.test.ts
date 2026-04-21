import { beforeEach, describe, expect, it, vi } from "vitest";

const authState = {
  token: null as string | null,
};

vi.mock("@/lib/store/auth-store", () => ({
  useAuthStore: {
    getState: () => authState,
  },
}));

describe("artifactApi.downloadUrl", () => {
  beforeEach(() => {
    authState.token = null;
  });

  it("returns a plain download path when there is no auth token", async () => {
    const { artifactApi } = await import("./artifacts");

    expect(artifactApi.downloadUrl("artifact-1")).toBe(
      "/api/artifacts/artifact-1/download",
    );
  });

  it("appends the auth token for browser downloads", async () => {
    authState.token = "jwt-token";
    const { artifactApi } = await import("./artifacts");

    expect(artifactApi.downloadUrl("artifact-1")).toBe(
      "/api/artifacts/artifact-1/download?token=jwt-token",
    );
  });
});
