import { beforeEach, describe, expect, it, vi } from "vitest";

const post = vi.fn();

vi.mock("./client", () => ({
  api: {
    post,
  },
}));

describe("versionApi.ship", () => {
  beforeEach(() => {
    post.mockReset();
    post.mockReturnValue({
      json: vi.fn(),
    });
  });

  it("disables ky timeout for long-running ship requests", async () => {
    const { versionApi } = await import("./versions");

    versionApi.ship("ver-1");

    expect(post).toHaveBeenCalledWith("versions/ver-1/ship", {
      timeout: false,
    });
  });
});
