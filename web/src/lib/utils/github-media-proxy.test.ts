import { describe, expect, it } from "vitest";
import {
  convertMediaUrlsToAbsolute,
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

  describe("convertMediaUrlsToAbsolute", () => {
    it("returns empty string unchanged", () => {
      expect(convertMediaUrlsToAbsolute("", "jwt-token")).toBe("");
    });

    it("converts markdown image with relative issue asset path to absolute URL with token", () => {
      const result = convertMediaUrlsToAbsolute(
        "![screenshot](/api/issues/assets/abc-123/content)",
        "jwt-token",
      );
      expect(result).toContain("token=jwt-token");
      expect(result).toMatch(/^!\[screenshot\]\(https?:\/\/[^/]+\/api\/issues\/assets\/abc-123\/content\?token=jwt-token\)$/);
    });

    it("leaves external GitHub URLs untouched", () => {
      const input = "![img](https://github.com/user-attachments/assets/demo.png)";
      expect(convertMediaUrlsToAbsolute(input, "jwt-token")).toBe(input);
    });

    it("extracts original URL from media proxy references in markdown", () => {
      const result = convertMediaUrlsToAbsolute(
        "![img](/api/github/media-proxy?url=https%3A%2F%2Fgithub.com%2Fuser-attachments%2Fassets%2Fdemo)",
        "jwt-token",
      );
      expect(result).toBe("![img](https://github.com/user-attachments/assets/demo)");
    });

    it("converts HTML img src with relative issue asset path", () => {
      const result = convertMediaUrlsToAbsolute(
        '<img src="/api/issues/assets/xyz/content" alt="photo">',
        "jwt-token",
      );
      expect(result).toContain("token=jwt-token");
      expect(result).toMatch(/src="https?:\/\/[^/]+\/api\/issues\/assets\/xyz\/content\?token=jwt-token"/);
    });

    it("preserves text without media URLs unchanged", () => {
      const input = "Hello world, no images here.";
      expect(convertMediaUrlsToAbsolute(input, "jwt-token")).toBe(input);
    });
  });
});
