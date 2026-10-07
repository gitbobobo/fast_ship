import { beforeEach, describe, expect, it, vi } from "vitest";

const post = vi.fn();
const del = vi.fn();

vi.mock("./client", () => ({
  api: {
    post,
    delete: del,
  },
}));

describe("issueApi pull request links", () => {
  beforeEach(() => {
    post.mockReset();
    del.mockReset();
    post.mockReturnValue({ json: vi.fn() });
    del.mockReturnValue({ json: vi.fn() });
  });

  it("posts the PR url to the attach endpoint", async () => {
    const { issueApi } = await import("./issues");

    issueApi.attachPullRequest(
      "issue-1",
      "https://github.com/acme/alpha/pull/123",
    );

    expect(post).toHaveBeenCalledWith("issues/issue-1/pull-requests", {
      json: { url: "https://github.com/acme/alpha/pull/123" },
    });
  });

  it("posts to the sync endpoint", async () => {
    const { issueApi } = await import("./issues");

    issueApi.syncPullRequests("issue-1");

    expect(post).toHaveBeenCalledWith("issues/issue-1/pull-requests/sync");
  });

  it("deletes the link by id", async () => {
    const { issueApi } = await import("./issues");

    issueApi.detachPullRequest("issue-1", "link-9");

    expect(del).toHaveBeenCalledWith("issues/issue-1/pull-requests/link-9");
  });
});
