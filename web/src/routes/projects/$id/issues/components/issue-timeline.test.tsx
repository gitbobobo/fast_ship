import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  IssueCommentComposer,
  IssueTimeline,
  type IssueTimelineItem,
} from "./issue-timeline";
import { renderWithRoute } from "@/test/render";
import { toast } from "sonner";

vi.mock("sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));

const comment: IssueComment = {
  id: "comment-1",
  issue_id: "issue-1",
  source: "github",
  github_comment_id: 5001,
  github_node_id: "IC_kw",
  body: "Looks good",
  body_html: "<p>Looks <strong>good</strong></p>",
  html_url: "https://github.com/acme/alpha/issues/42#issuecomment-1",
  author: { login: "charlie", avatar_url: "" },
  author_association: "CONTRIBUTOR",
  reactions: {
    total_count: 0,
    "+1": 0,
    "-1": 0,
    laugh: 0,
    hooray: 0,
    confused: 0,
    heart: 0,
    rocket: 0,
    eyes: 0,
  },
  created_at: "2026-04-11T10:00:00Z",
  updated_at: "2026-04-11T10:00:00Z",
};

const labeledEvent: IssueTimelineEvent = {
  id: "event-1",
  issue_id: "issue-1",
  event_key: "gh:1",
  event_type: "labeled",
  github_event_id: 1,
  actor: { login: "alice", avatar_url: "" },
  body: "",
  summary: "添加了标签",
  payload: { label: { name: "bug", color: "d73a4a" } },
  created_at: "2026-04-12T10:00:00Z",
};

function buildItems(): IssueTimelineItem[] {
  return [
    { type: "comment", data: comment, created_at: comment.created_at },
    { type: "event", data: labeledEvent, created_at: labeledEvent.created_at },
  ];
}

function renderTimeline(overrides: Partial<Parameters<typeof IssueTimeline>[0]> = {}) {
  const onLoadMore = vi.fn();
  const onNavigateToAnchor = vi.fn();
  const onCopyGithubLink = vi.fn();

  const { container } = renderWithRoute(
    <IssueTimeline
      items={buildItems()}
      isLoading={false}
      hasMore={false}
      isFetchingMore={false}
      onLoadMore={onLoadMore}
      flashHighlightId={null}
      onNavigateToAnchor={onNavigateToAnchor}
      onCopyGithubLink={onCopyGithubLink}
      fallbackGithubUrl="https://github.com/acme/alpha/issues/42"
      {...overrides}
    />,
  );

  return { container, onLoadMore, onNavigateToAnchor, onCopyGithubLink };
}

describe("IssueTimeline", () => {
  beforeEach(() => {
    vi.mocked(toast.success).mockClear();
    vi.mocked(toast.error).mockClear();
  });

  it("renders loading skeletons while loading", () => {
    const { container } = renderTimeline({ isLoading: true });

    expect(container.querySelectorAll("[data-slot='skeleton']")).toHaveLength(3);
    expect(screen.queryByText("暂无评论和动态")).not.toBeInTheDocument();
  });

  it("renders the empty state when there are no items", () => {
    renderTimeline({ items: [] });

    expect(screen.getByText("暂无评论和动态")).toBeInTheDocument();
  });

  it("renders comments and event summaries", () => {
    renderTimeline();

    expect(screen.getByText("@charlie")).toBeInTheDocument();
    expect(screen.getByText("alice")).toBeInTheDocument();
    expect(screen.getByText("添加了标签")).toBeInTheDocument();
    expect(screen.getByText("bug")).toBeInTheDocument();
    expect(document.getElementById("issuecomment-5001")).not.toBeNull();
    expect(document.getElementById("issueevent-1")).not.toBeNull();
  });

  it("falls back to the event summary text for unknown event types", () => {
    renderTimeline({
      items: [
        {
          type: "event",
          data: { ...labeledEvent, event_type: "custom", summary: "自定义动态" },
          created_at: labeledEvent.created_at,
        },
      ],
    });

    expect(screen.getByText("自定义动态")).toBeInTheDocument();
  });

  it("highlights the item matching flashHighlightId", () => {
    renderTimeline({ flashHighlightId: "issuecomment-5001" });

    expect(document.getElementById("issuecomment-5001")).toHaveClass("ring-2");
  });

  it("navigates to comment and event anchors", () => {
    const { onNavigateToAnchor } = renderTimeline();

    fireEvent.click(screen.getByRole("button", { name: "定位到评论 5001" }));
    expect(onNavigateToAnchor).toHaveBeenCalledWith("#issuecomment-5001");

    fireEvent.click(screen.getByRole("button", { name: "定位到动态 1" }));
    expect(onNavigateToAnchor).toHaveBeenCalledWith("#issueevent-1");
  });

  it("copies the comment deep link", () => {
    const { onCopyGithubLink } = renderTimeline();

    fireEvent.click(
      screen.getByRole("button", { name: "复制评论 5001 的 GitHub 深链接" }),
    );
    expect(onCopyGithubLink).toHaveBeenCalledWith(
      "https://github.com/acme/alpha/issues/42#issuecomment-1",
      "已复制 GitHub 深链接",
    );
  });

  it("prefers the payload html_url for events", () => {
    const { onCopyGithubLink } = renderTimeline({
      items: [
        {
          type: "event",
          data: {
            ...labeledEvent,
            payload: {
              label: {
                html_url: "https://github.com/acme/alpha/issues/42#event-1",
              },
            },
          },
          created_at: labeledEvent.created_at,
        },
      ],
    });

    fireEvent.click(
      screen.getByRole("button", { name: "复制动态 1 的 GitHub 深链接" }),
    );
    expect(onCopyGithubLink).toHaveBeenCalledWith(
      "https://github.com/acme/alpha/issues/42#event-1",
      "已复制 GitHub 深链接",
    );
  });

  it("falls back to the issue url when the event has no precise link", () => {
    const { onCopyGithubLink } = renderTimeline();

    fireEvent.click(
      screen.getByRole("button", { name: "复制动态 1 的 GitHub 深链接" }),
    );
    expect(onCopyGithubLink).toHaveBeenCalledWith(
      "https://github.com/acme/alpha/issues/42",
      "当前动态没有精确 GitHub 链接，已复制问题链接",
    );
  });

  it("passes null when neither payload nor fallback url exists", () => {
    const { onCopyGithubLink } = renderTimeline({ fallbackGithubUrl: null });

    fireEvent.click(
      screen.getByRole("button", { name: "复制动态 1 的 GitHub 深链接" }),
    );
    expect(onCopyGithubLink).toHaveBeenCalledWith(
      null,
      "当前动态没有精确 GitHub 链接，已复制问题链接",
    );
  });

  it("observes the sentinel and loads more when visible", () => {
    const observers: FakeIntersectionObserver[] = [];
    class FakeIntersectionObserver {
      callback: IntersectionObserverCallback;
      observe = vi.fn();
      unobserve = vi.fn();
      disconnect = vi.fn();

      constructor(callback: IntersectionObserverCallback) {
        this.callback = callback;
        observers.push(this);
      }
    }
    vi.stubGlobal("IntersectionObserver", FakeIntersectionObserver);

    try {
      const { onLoadMore } = renderTimeline({ hasMore: true });

      expect(observers).toHaveLength(1);
      observers[0].callback(
        [{ isIntersecting: true } as IntersectionObserverEntry],
        observers[0] as unknown as IntersectionObserver,
      );
      expect(onLoadMore).toHaveBeenCalledTimes(1);

      observers[0].callback(
        [{ isIntersecting: false } as IntersectionObserverEntry],
        observers[0] as unknown as IntersectionObserver,
      );
      expect(onLoadMore).toHaveBeenCalledTimes(1);
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("does not load more while fetching", () => {
    const observers: FakeIntersectionObserverNoop[] = [];
    class FakeIntersectionObserverNoop {
      callback: IntersectionObserverCallback;
      observe = vi.fn();
      unobserve = vi.fn();
      disconnect = vi.fn();

      constructor(callback: IntersectionObserverCallback) {
        this.callback = callback;
        observers.push(this);
      }
    }
    vi.stubGlobal("IntersectionObserver", FakeIntersectionObserverNoop);

    try {
      const { onLoadMore } = renderTimeline({ hasMore: true, isFetchingMore: true });

      observers[0].callback(
        [{ isIntersecting: true } as IntersectionObserverEntry],
        observers[0] as unknown as IntersectionObserver,
      );
      expect(onLoadMore).not.toHaveBeenCalled();
    } finally {
      vi.unstubAllGlobals();
    }
  });
});

describe("IssueCommentComposer", () => {
  beforeEach(() => {
    vi.mocked(toast.success).mockClear();
    vi.mocked(toast.error).mockClear();
  });

  it("renders the internal issue title and description", () => {
    render(
      <IssueCommentComposer isInternalIssue onSubmit={vi.fn().mockResolvedValue(true)} />,
    );

    expect(screen.getByText("内部评论")).toBeInTheDocument();
    expect(screen.getByText("评论仅保存在 Fast Ship 内部。")).toBeInTheDocument();
  });

  it("rejects empty submissions without calling onSubmit", async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn();
    render(<IssueCommentComposer isInternalIssue={false} onSubmit={onSubmit} />);

    await user.click(screen.getByRole("button", { name: "发布评论" }));

    expect(toast.error).toHaveBeenCalledWith("请输入评论内容");
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it("clears the draft after a successful submit", async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn().mockResolvedValue(true);
    render(<IssueCommentComposer isInternalIssue={false} onSubmit={onSubmit} />);

    const textarea = screen.getByPlaceholderText("使用 Markdown 输入评论内容");
    await user.type(textarea, "需要补充日志");
    await user.click(screen.getByRole("button", { name: "发布评论" }));

    await waitFor(() => expect(onSubmit).toHaveBeenCalledWith("需要补充日志"));
    expect(toast.success).not.toHaveBeenCalled();
    await waitFor(() => expect(textarea).toHaveValue(""));
  });

  it("keeps the draft when the submit fails", async () => {
    const user = userEvent.setup();
    const onSubmit = vi.fn().mockResolvedValue(false);
    render(<IssueCommentComposer isInternalIssue={false} onSubmit={onSubmit} />);

    const textarea = screen.getByPlaceholderText("使用 Markdown 输入评论内容");
    await user.type(textarea, "发布失败的内容");
    await user.click(screen.getByRole("button", { name: "发布评论" }));

    await waitFor(() => expect(onSubmit).toHaveBeenCalledWith("发布失败的内容"));
    expect(textarea).toHaveValue("发布失败的内容");
  });
});
