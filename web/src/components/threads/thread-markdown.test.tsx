import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ThreadMarkdown } from "@/components/threads/thread-markdown";

describe("ThreadMarkdown", () => {
  it("renders full GFM content", () => {
    const { container } = render(
      <ThreadMarkdown
        text={"# 标题\n\n- a\n- b\n\n| x | y |\n|---|---|\n| 1 | 2 |\n\n```ts\nconst a = 1;\n```\n\n[link](https://example.com)"}
      />,
    );
    expect(screen.getByRole("heading", { name: "标题" })).toBeInTheDocument();
    expect(container.querySelectorAll("li")).toHaveLength(2);
    expect(container.querySelector("table")).not.toBeNull();
    expect(container.querySelector("pre code")?.textContent).toContain("const a = 1;");
    const link = screen.getByRole("link", { name: "link" });
    expect(link).toHaveAttribute("target", "_blank");
    expect(link.getAttribute("rel")).toContain("noopener");
  });

  it("never renders images, markdown or html", () => {
    const { container } = render(
      <ThreadMarkdown
        text={'![shot](https://example.com/a.png)\n\n<img src="https://example.com/b.png" onerror="alert(1)">\n\n<script>alert(1)</script>'}
      />,
    );
    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector("script")).toBeNull();
  });

  it("drops javascript: link targets", () => {
    render(<ThreadMarkdown text="[x](javascript:alert(1))" />);
    const link = screen.queryByRole("link");
    expect(link?.getAttribute("href") ?? "").not.toContain("javascript:");
  });
});
