import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { BoardColumnFilter } from "./board-column-filter";

async function openMenu() {
  fireEvent.click(screen.getByRole("button", { name: "筛选" }));
  await waitFor(() => {
    expect(screen.getByRole("menu")).toBeInTheDocument();
  });
}

describe("BoardColumnFilter", () => {
  it("renders the filter trigger button", () => {
    render(
      <BoardColumnFilter
        labels={["bug", "feature"]}
        value={{ label: "", source: "all" }}
        onChange={() => {}}
      />,
    );
    expect(screen.getByRole("button", { name: "筛选" })).toBeInTheDocument();
  });

  it("calls onChange with the selected source", async () => {
    const onChange = vi.fn();
    render(
      <BoardColumnFilter
        labels={["bug"]}
        value={{ label: "", source: "all" }}
        onChange={onChange}
      />,
    );

    await openMenu();
    fireEvent.click(
      screen.getByRole("menuitemradio", { name: "GitHub 问题" }),
    );

    expect(onChange).toHaveBeenCalledWith({ label: "", source: "github" });
  });

  it("calls onChange with the selected label", async () => {
    const onChange = vi.fn();
    render(
      <BoardColumnFilter
        labels={["bug", "feature"]}
        value={{ label: "", source: "all" }}
        onChange={onChange}
      />,
    );

    await openMenu();
    fireEvent.click(screen.getByRole("menuitemradio", { name: "bug" }));

    expect(onChange).toHaveBeenCalledWith({ label: "bug", source: "all" });
  });

  it("resets to default when clicking 清除筛选", async () => {
    const onChange = vi.fn();
    render(
      <BoardColumnFilter
        labels={["bug"]}
        value={{ label: "bug", source: "github" }}
        onChange={onChange}
      />,
    );

    await openMenu();
    fireEvent.click(screen.getByRole("menuitem", { name: "清除筛选" }));

    expect(onChange).toHaveBeenCalledWith({ label: "", source: "all" });
  });

  it("applies active styles and shows indicator dot when filter is active", () => {
    const { rerender } = render(
      <BoardColumnFilter
        labels={["bug"]}
        value={{ label: "", source: "all" }}
        onChange={() => {}}
      />,
    );
    const inactive = screen.getByRole("button", { name: "筛选" });
    expect(inactive).not.toHaveClass("bg-primary/10");
    expect(inactive).not.toHaveClass("text-primary");
    expect(inactive.querySelector(".rounded-full.bg-primary")).toBeNull();

    rerender(
      <BoardColumnFilter
        labels={["bug"]}
        value={{ label: "bug", source: "all" }}
        onChange={() => {}}
      />,
    );
    const active = screen.getByRole("button", { name: "筛选" });
    expect(active).toHaveClass("bg-primary/10");
    expect(active).toHaveClass("text-primary");
    // 激活态时 trigger 右上角显示小圆点徽标
    expect(active.querySelector(".rounded-full.bg-primary")).toBeInTheDocument();
  });

  it("disables the trigger when disabled prop is set", () => {
    render(
      <BoardColumnFilter
        labels={["bug"]}
        value={{ label: "", source: "all" }}
        onChange={() => {}}
        disabled
      />,
    );
    expect(screen.getByRole("button", { name: "筛选" })).toBeDisabled();
  });
});
