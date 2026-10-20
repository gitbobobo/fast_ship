import { render, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import type { ThreadSummary } from "@/lib/api/threads";
import { ThreadPreviewContent, ThreadRow } from "@/components/threads/thread-row";

function thread(over: Partial<ThreadSummary> = {}): ThreadSummary {
  return {
    id: "t1",
    environment_id: "e1",
    environment_name: "Mac",
    project_id: "p1",
    project_name: "Alpha",
    title: "Fix login",
    status: "idle",
    branch: "feature/x",
    provider: "codex",
    model: "gpt-5",
    updated_at: new Date(Date.now() - 42 * 60_000).toISOString(),
    pull_request_count: 0,
    settled: false,
    ...over,
  };
}

describe("ThreadPreviewContent", () => {
  it("shows the full facts the compact row leaves out", () => {
    render(<ThreadPreviewContent thread={thread({ pull_request_count: 2 })} />);

    expect(
      screen.getByRole("heading", { name: "Fix login" }),
    ).toBeInTheDocument();
    expect(screen.getByText("Mac · Alpha")).toBeInTheDocument();
    expect(screen.getByText("feature/x")).toBeInTheDocument();
    expect(screen.getByText("codex / gpt-5")).toBeInTheDocument();
    expect(
      document.querySelector("[data-provider-icon='codex']"),
    ).not.toBeNull();
    expect(screen.getByText("空闲")).toBeInTheDocument();
    expect(screen.getByText(/42 分钟前（/)).toBeInTheDocument();
    expect(screen.getByText("2 个")).toBeInTheDocument();
  });

  it("shows the run duration, settled marker and dashes for missing values", () => {
    render(
      <ThreadPreviewContent
        thread={thread({
          status: "running",
          run_started_at: new Date(Date.now() - 65 * 60_000).toISOString(),
          branch: "",
          provider: "",
          model: "",
          settled: true,
        })}
      />,
    );

    expect(screen.getByText(/运行中 · 1 小时 5 分钟/)).toBeInTheDocument();
    expect(screen.getByText("已完结")).toBeInTheDocument();
    // 分支、模型、PR 缺值各一个占位。
    expect(screen.getAllByText("—")).toHaveLength(3);
  });
});

describe("ThreadRow", () => {
  it("links to the thread detail and keeps the three-line facts", () => {
    render(
      <MemoryRouter>
        <ThreadRow thread={thread({ pull_request_count: 2 })} />
      </MemoryRouter>,
    );

    const link = screen.getByTestId("thread-card");
    expect(link).toHaveAttribute("href", "/threads/e1/t1");
    expect(link).not.toHaveAttribute("data-settled");
    // 第一行：项目名 + 状态；第二行：标题；第三行：分支 + PR + 相对时间。
    expect(within(link).getByText("Alpha")).toBeInTheDocument();
    expect(within(link).getByText("空闲")).toBeInTheDocument();
    expect(within(link).getByRole("heading")).toHaveTextContent("Fix login");
    expect(within(link).getByText("feature/x")).toBeInTheDocument();
    expect(within(link).getByLabelText("2 个 PR")).toBeInTheDocument();
    expect(within(link).getByText("42 分钟前")).toBeInTheDocument();
    expect(link.querySelector(".bg-amber-500")).toBeNull();
  });

  it("opens the preview on keyboard focus and closes it with Escape", async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <ThreadRow thread={thread({ pull_request_count: 2 })} />
      </MemoryRouter>,
    );

    await user.tab();
    expect(screen.getByTestId("thread-card")).toHaveFocus();
    expect(await screen.findByTestId("thread-preview")).toBeInTheDocument();

    await user.keyboard("{Escape}");
    await waitFor(() =>
      expect(screen.queryByTestId("thread-preview")).not.toBeInTheDocument(),
    );
  });

  it("marks waiting rows with an amber bar and dims settled rows", () => {
    render(
      <MemoryRouter>
        <ThreadRow thread={thread({ status: "waiting" })} />
        <ThreadRow thread={thread({ id: "t2", title: "Done", settled: true })} />
      </MemoryRouter>,
    );

    const [waiting, settled] = screen.getAllByTestId("thread-card");
    expect(waiting.querySelector(".bg-amber-500")).not.toBeNull();
    expect(settled).toHaveAttribute("data-settled", "true");
    expect(settled).toHaveClass("opacity-60");
    expect(within(settled).getByRole("heading")).toHaveTextContent(
      "已完结，Done",
    );
  });
});
