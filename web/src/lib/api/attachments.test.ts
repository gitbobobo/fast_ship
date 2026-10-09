import { beforeEach, describe, expect, it, vi } from "vitest";

const authState = {
  token: null as string | null,
};

vi.mock("@/lib/store/auth-store", () => ({
  useAuthStore: {
    getState: () => authState,
  },
}));

describe("attachmentApi.downloadUrl", () => {
  beforeEach(() => {
    authState.token = null;
  });

  it("returns a plain download path when there is no auth token", async () => {
    const { attachmentApi } = await import("./attachments");

    expect(
      attachmentApi.downloadUrl({ id: "att-1", download_url: "" }),
    ).toBe("/api/attachments/att-1/download");
  });

  it("appends the auth token for browser downloads", async () => {
    authState.token = "jwt-token";
    const { attachmentApi } = await import("./attachments");

    expect(
      attachmentApi.downloadUrl({ id: "att-1", download_url: "" }),
    ).toBe("/api/attachments/att-1/download?token=jwt-token");
  });

  it("prefers the download_url carried by the issue detail response", async () => {
    authState.token = "jwt-token";
    const { attachmentApi } = await import("./attachments");

    expect(
      attachmentApi.downloadUrl({
        id: "att-1",
        download_url: "/api/attachments/att-1/download",
      }),
    ).toBe("/api/attachments/att-1/download?token=jwt-token");
  });
});
