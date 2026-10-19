import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { QuotaWindowProgress } from "./window-progress";

function makeWindow(overrides: Partial<QuotaWindow> = {}): QuotaWindow {
  return {
    kind: "monthly",
    label: "每月",
    used_percent: null,
    used: null,
    limit: null,
    unit: "percent",
    reset_at: null,
    ...overrides,
  };
}

describe("QuotaWindowProgress", () => {
  it("是纯展示行：不渲染 tooltip，行本身也不可聚焦（明细由卡片悬浮面板承担）", () => {
    const { container } = render(
      <QuotaWindowProgress
        window={makeWindow({
          kind: "five_hour",
          label: "5 小时",
          used_percent: 97,
          used: 9.7,
          limit: 10,
          unit: "count",
        })}
      />,
    );

    expect(
      container.querySelector('[data-slot="tooltip-trigger"]'),
    ).toBeNull();
    expect(container.querySelector("[tabindex]")).toBeNull();
    // used/limit 明细不进正文行
    expect(container.textContent).not.toContain("9.7 / 10");
  });

  it("used_percent 为 null 时省略 aria-valuenow，不谎报 0%", () => {
    render(<QuotaWindowProgress window={makeWindow({ used_percent: null })} />);
    expect(screen.getByRole("progressbar")).not.toHaveAttribute(
      "aria-valuenow",
    );
  });

  it("used_percent 有值时 aria-valuenow 跟随数值", () => {
    render(<QuotaWindowProgress window={makeWindow({ used_percent: 42 })} />);
    expect(screen.getByRole("progressbar")).toHaveAttribute(
      "aria-valuenow",
      "42",
    );
  });

  it("受限窗口整行加粗，普通窗口不加粗", () => {
    const { container, rerender } = render(
      <QuotaWindowProgress
        window={makeWindow({ used_percent: 82 })}
        limited
      />,
    );
    expect(
      container.querySelector('[data-testid="quota-window"] > div'),
    ).toHaveClass("font-medium");

    rerender(
      <QuotaWindowProgress window={makeWindow({ used_percent: 82 })} />,
    );
    expect(
      container.querySelector('[data-testid="quota-window"] > div'),
    ).not.toHaveClass("font-medium");
  });

  it("标签列自适应、进度条固定宽度：长标签不再被 56px 截掉", () => {
    render(
      <QuotaWindowProgress window={makeWindow({ label: "Token 额度" })} />,
    );
    const label = screen.getByText("Token 额度");
    // 回归点：标签曾固定 w-14（56px）截掉「Token 额度」「Credit 额度」
    expect(label).toHaveClass("min-w-0", "flex-1", "truncate");
    expect(label).not.toHaveClass("w-14");
    // 进度条改固定宽度，窄卡片里不被长标签挤没
    expect(screen.getByRole("progressbar")).toHaveClass("w-16", "shrink-0");
  });
});
