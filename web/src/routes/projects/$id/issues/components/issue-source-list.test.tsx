import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { IssueSourceList } from "./issue-source-list";

function makeIssue(
  id: string,
  reference: string,
  title: string,
  overrides: Partial<Issue> = {},
): Issue {
  return {
    id,
    project_id: "proj-1",
    source: "github",
    sequence_number: 1,
    reference,
    state: "open",
    state_reason: "",
    title,
    body: "",
    body_html: "",
    author: { login: "alice", avatar_url: "" },
    unread_comments_count: 0,
    closed_at: null,
    created_at: "2026-04-10T10:00:00Z",
    updated_at: "2026-04-12T10:00:00Z",
    ...overrides,
  };
}

function renderList(
  overrides: Partial<Parameters<typeof IssueSourceList>[0]> = {},
) {
  return render(
    <IssueSourceList
      entries={[
        { issue: makeIssue("issue-1", "GH-1", "崩溃") },
        { issue: makeIssue("issue-2", "GH-2", "卡顿", { state: "closed" }) },
      ]}
      currentIssueId="issue-1"
      onSelect={vi.fn()}
      isLoading={false}
      isError={false}
      onRetry={vi.fn()}
      hasNextPage={false}
      isFetchingNextPage={false}
      onLoadMore={vi.fn()}
      {...overrides}
    />,
  );
}

describe("IssueSourceList", () => {
  beforeEach(() => {
    window.HTMLElement.prototype.scrollIntoView = vi.fn();
    vi.stubGlobal(
      "IntersectionObserver",
      class {
        observe() {}
        disconnect() {}
        unobserve() {}
      },
    );
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("keeps loaded entries visible with an inline retry when pagination fails", () => {
    const onRetry = vi.fn();
    renderList({ isError: true, hasNextPage: true, onRetry });

    expect(screen.getByTestId("issue-source-list")).toBeInTheDocument();
    expect(screen.getByText("GH-1")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "加载更多失败，点击重试" }));
    expect(onRetry).toHaveBeenCalledTimes(1);
  });

  it("replaces the list with the error panel only when nothing is loaded", () => {
    renderList({ isError: true, entries: [] });

    expect(screen.queryByTestId("issue-source-list")).not.toBeInTheDocument();
    expect(screen.getByText("来源列表加载失败")).toBeInTheDocument();
  });

  it("renders reference, title, state and highlights the current issue", () => {
    renderList();

    const list = screen.getByTestId("issue-source-list");
    expect(list).toBeInTheDocument();
    const current = screen.getByRole("button", { current: true });
    expect(current).toHaveAttribute("data-issue-id", "issue-1");
    expect(current).toHaveTextContent("GH-1");
    expect(current).toHaveTextContent("崩溃");
    expect(screen.getByText("Closed")).toBeInTheDocument();
    expect(screen.getAllByText("Open").length).toBeGreaterThan(0);
  });

  it("marks unread comments on github issues only", () => {
    renderList({
      entries: [
        { issue: makeIssue("issue-1", "GH-1", "有未读", { unread_comments_count: 2 }) },
        {
          issue: makeIssue("issue-2", "INT-2", "内部未读", {
            source: "internal",
            unread_comments_count: 2,
          }),
        },
      ],
    });

    expect(screen.getAllByLabelText(/有未读评论/).length).toBe(1);
  });

  it("renders the stale entry with the out-of-source marker", () => {
    renderList({
      entries: [
        { issue: makeIssue("issue-1", "GH-1", "崩溃") },
        {
          issue: makeIssue("issue-x", "GH-9", "已移出", { state: "closed" }),
          stale: true,
        },
      ],
    });

    expect(screen.getByText("已不符合来源条件")).toBeInTheDocument();
  });

  it("calls onSelect with the clicked issue", async () => {
    const onSelect = vi.fn();
    renderList({ onSelect });

    screen.getByRole("button", { name: /GH-2 卡顿/ }).click();

    expect(onSelect).toHaveBeenCalledTimes(1);
    expect(onSelect.mock.calls[0][0].id).toBe("issue-2");
  });

  it("shows loading, empty and error states", () => {
    const { rerender } = renderList({ isLoading: true });
    expect(screen.getByText("加载中…")).toBeInTheDocument();

    rerender(
      <IssueSourceList
        entries={[]}
        currentIssueId=""
        onSelect={vi.fn()}
        isLoading={false}
        isError={false}
        onRetry={vi.fn()}
        hasNextPage={false}
        isFetchingNextPage={false}
        onLoadMore={vi.fn()}
      />,
    );
    expect(screen.getByText("来源列表暂无问题")).toBeInTheDocument();

    rerender(
      <IssueSourceList
        entries={[]}
        currentIssueId=""
        onSelect={vi.fn()}
        isLoading={false}
        isError={true}
        onRetry={vi.fn()}
        hasNextPage={false}
        isFetchingNextPage={false}
        onLoadMore={vi.fn()}
      />,
    );
    expect(screen.getByText("来源列表加载失败")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "重试" })).toBeInTheDocument();
  });

  it("auto-scrolls to the current issue", () => {
    const scrollIntoView = vi.fn();
    window.HTMLElement.prototype.scrollIntoView = scrollIntoView;

    renderList();

    expect(scrollIntoView).toHaveBeenCalledWith({ block: "nearest" });
  });
});
