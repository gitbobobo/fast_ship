import { describe, expect, it } from "vitest";
import {
  rewriteGitHubMediaHtml,
  toGitHubMediaProxyUrl,
  toProtectedMediaUrl,
} from "@/lib/utils/github-media-proxy";

describe("github media proxy utils", () => {
  it("returns undefined for nullish inputs", () => {
    expect(toGitHubMediaProxyUrl(null)).toBeUndefined();
    expect(toGitHubMediaProxyUrl(undefined)).toBeUndefined();
  });

  it("adds the auth token to raw github media urls", () => {
    expect(toGitHubMediaProxyUrl("https://github.com/user-attachments/assets/demo", "jwt-token")).toBe(
      "/api/github/media-proxy?url=https%3A%2F%2Fgithub.com%2Fuser-attachments%2Fassets%2Fdemo&token=jwt-token",
    );
  });

  it("adds the auth token to existing proxy urls in html content", () => {
    const html = rewriteGitHubMediaHtml(
      '<p><img alt="Image" src="/api/github/media-proxy?url=https%3A%2F%2Fgithub.com%2Fuser-attachments%2Fassets%2Fdemo" /></p>',
      "jwt-token",
    );
    const doc = new DOMParser().parseFromString(html ?? "", "text/html");

    expect(doc.querySelector("img")?.getAttribute("src")).toBe(
      "/api/github/media-proxy?url=https%3A%2F%2Fgithub.com%2Fuser-attachments%2Fassets%2Fdemo&token=jwt-token",
    );
  });

  it("adds the auth token to internal issue asset urls", () => {
    expect(toProtectedMediaUrl("/api/issues/assets/asset-1/content", "jwt-token")).toBe(
      "/api/issues/assets/asset-1/content?token=jwt-token",
    );
  });
});
