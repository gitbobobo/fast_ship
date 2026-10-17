import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { IssueWorkflowStatusBadge } from "./issue-workflow-status-badge";

describe("IssueWorkflowStatusBadge", () => {
  it("renders todo with slate colors", () => {
    render(<IssueWorkflowStatusBadge status="todo" />);

    const badge = screen.getByText("待处理");
    expect(badge).toHaveClass("border-slate-500/20", "bg-slate-500/10", "text-slate-600");
  });

  it("renders in_progress with amber colors", () => {
    render(<IssueWorkflowStatusBadge status="in_progress" />);

    const badge = screen.getByText("开发中");
    expect(badge).toHaveClass("border-amber-500/20", "bg-amber-500/10", "text-amber-600");
  });

  it("renders done with emerald colors", () => {
    render(<IssueWorkflowStatusBadge status="done" />);

    const badge = screen.getByText("已完成");
    expect(badge).toHaveClass("border-emerald-500/20", "bg-emerald-500/10", "text-emerald-600");
  });

  it("renders nothing for empty, null or unknown status", () => {
    const { container, rerender } = render(<IssueWorkflowStatusBadge status="" />);
    expect(container).toBeEmptyDOMElement();

    rerender(<IssueWorkflowStatusBadge status={null} />);
    expect(container).toBeEmptyDOMElement();

    rerender(<IssueWorkflowStatusBadge status="unknown_status" />);
    expect(container).toBeEmptyDOMElement();
  });

  it("applies board pill classes for size xs", () => {
    render(<IssueWorkflowStatusBadge status="todo" size="xs" />);

    expect(screen.getByText("待处理")).toHaveClass(
      "px-2",
      "py-0.5",
      "text-[11px]",
      "font-medium",
    );
  });

  it("defaults to issue list badge classes (size sm)", () => {
    render(<IssueWorkflowStatusBadge status="todo" />);

    expect(screen.getByText("待处理")).toHaveClass(
      "px-2",
      "py-0.5",
      "text-xs",
      "font-medium",
    );
  });

  it("applies detail header classes for size md", () => {
    render(<IssueWorkflowStatusBadge status="todo" size="md" />);

    expect(screen.getByText("待处理")).toHaveClass(
      "px-2.5",
      "py-1",
      "text-xs",
      "font-semibold",
    );
  });
});
