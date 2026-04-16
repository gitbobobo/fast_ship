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
});
