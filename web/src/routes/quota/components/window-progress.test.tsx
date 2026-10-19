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
  it("used/limit 明细行的 tooltip trigger 可聚焦且有可访问名称", () => {
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

    // 明细从正文移进 tooltip 后，trigger 必须键盘可达：
    // 可聚焦 + 窗口名与明细组成可访问名称
    const trigger = container.querySelector('[data-slot="tooltip-trigger"]');
    expect(trigger).not.toBeNull();
    expect(trigger).toHaveAttribute("tabindex", "0");
    expect(trigger).toHaveAttribute("aria-label", "5 小时：9.7 / 10");

    (trigger as HTMLElement).focus();
    expect(document.activeElement).toBe(trigger);
  });

  it("没有 used/limit 明细的行不渲染 tooltip，也不需要可聚焦", () => {
    const { container } = render(
      <QuotaWindowProgress window={makeWindow({ used_percent: 38 })} />,
    );
    expect(
      container.querySelector('[data-slot="tooltip-trigger"]'),
    ).toBeNull();
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
