import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { GitHubContent } from "@/components/github-content";
import { useAuthStore } from "@/lib/store/auth-store";

vi.mock("@/lib/store/auth-store", () => ({
  useAuthStore: vi.fn(),
}));

describe("GitHubContent", () => {
  const authState = {
    token: null as string | null,
    user: null as User | null,
    setAuth: vi.fn(),
    setUser: vi.fn(),
    logout: vi.fn(),
  };

  beforeEach(() => {
    vi.mocked(useAuthStore).mockImplementation(((selector?: (state: typeof authState) => unknown) =>
      selector ? selector(authState) : authState) as typeof useAuthStore);
    authState.token = null;
  });

  it("re-renders markdown media urls when the auth token changes", () => {
    const markdown = "![Image](https://github.com/user-attachments/assets/demo)";
    const { rerender } = render(<GitHubContent markdown={markdown} />);

    expect(screen.getByRole("img", { name: "Image" })).toHaveAttribute(
      "src",
      "/api/github/media-proxy?url=https%3A%2F%2Fgithub.com%2Fuser-attachments%2Fassets%2Fdemo",
    );

    authState.token = "jwt-token";
    rerender(<GitHubContent markdown={markdown} />);

    expect(screen.getByRole("img", { name: "Image" })).toHaveAttribute(
      "src",
      "/api/github/media-proxy?url=https%3A%2F%2Fgithub.com%2Fuser-attachments%2Fassets%2Fdemo&token=jwt-token",
    );
  });

  it("prefers markdown when html contains expiring private attachment urls", () => {
    authState.token = "jwt-token";

    render(
      <GitHubContent
        markdown="![Image](https://github.com/user-attachments/assets/demo)"
        html={'<p><img alt="Image" src="https://private-user-images.githubusercontent.com/1/demo.png?jwt=expired" /></p>'}
      />,
    );

    expect(screen.getByRole("img", { name: "Image" })).toHaveAttribute(
      "src",
      "/api/github/media-proxy?url=https%3A%2F%2Fgithub.com%2Fuser-attachments%2Fassets%2Fdemo&token=jwt-token",
    );
  });

  it("adds auth token to internal issue asset markdown images", () => {
    authState.token = "jwt-token";

    render(<GitHubContent markdown="![Image](/api/issues/assets/asset-1/content)" />);

    expect(screen.getByRole("img", { name: "Image" })).toHaveAttribute(
      "src",
      "/api/issues/assets/asset-1/content?token=jwt-token",
    );
  });

  it("prefers markdown when markdown contains local issue asset urls", () => {
    authState.token = "jwt-token";

    render(
      <GitHubContent
        markdown="![Image](/api/issues/assets/asset-1/content)"
        html={'<p><img alt="Rendered image" src="https://github.com/acme/alpha/api/issues/assets/asset-1/content" /></p>'}
      />,
    );

    expect(screen.getByRole("img", { name: "Image" })).toHaveAttribute(
      "src",
      "/api/issues/assets/asset-1/content?token=jwt-token",
    );
  });
});
