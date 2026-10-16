import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { QuotaProviderIcon } from "./provider-icons";

const IDS = [
  "commandcode",
  "opencodego",
  "zai",
  "grok",
  "codex",
  "devin",
  "factory",
];

describe("QuotaProviderIcon", () => {
  it("7 个供应商图标都用 currentColor 随主题着色", () => {
    for (const id of IDS) {
      const { container, unmount } = render(
        <QuotaProviderIcon provider={id} />,
      );
      const svg = container.querySelector(
        `[data-quota-provider-icon="${id}"]`,
      );
      expect(svg, `${id} 应渲染对应 svg`).not.toBeNull();
      // 不允许残留固定填充/描边色——浅色深色都靠 currentColor
      for (const el of svg!.querySelectorAll("[fill], [stroke]")) {
        const paint = el.getAttribute("fill") ?? el.getAttribute("stroke");
        expect(
          ["currentColor", "none"],
          `${id} 的 ${el.tagName} 用了固定色 ${paint}`,
        ).toContain(paint);
      }
      unmount();
    }
  });

  it("未知供应商回退通用图标", () => {
    const { container } = render(<QuotaProviderIcon provider="unknown-x" />);
    expect(
      container.querySelector("[data-quota-provider-icon]"),
    ).toBeNull();
    expect(container.querySelector("svg")).not.toBeNull();
  });
});
