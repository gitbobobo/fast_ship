import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { IssueSourceList } from "./issue-source-list";
import { toGitHubMediaProxyUrl } from "@/lib/utils/github-media-proxy";

// spy 模式：保留真实实现，只录调用参数——Base UI Avatar 在 jsdom 不触发图片
// 加载、不渲染真实 <img>，只能断言转换函数收到 token（见下方头像回归测试）
vi.mock("@/lib/utils/github-media-proxy", { spy: true });

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

function baseGithubMeta() {
  return {
    github_issue_id: 1,
    github_node_id: "I_kw",
    number: 42,
    html_url: "https://github.com/acme/alpha/issues/42",
    author_association: "MEMBER",
    assignees: [],
    labels: [],
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
    comments_count: 0,
    locked: false,
    active_lock_reason: "",
    synced_at: "2026-04-12T10:00:00Z",
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

  it("offers a retry after a background refresh fails with all pages loaded", () => {
    const onRetry = vi.fn();
    renderList({ isError: true, hasNextPage: false, onRetry });

    expect(screen.getByTestId("issue-source-list")).toBeInTheDocument();
    expect(screen.getByText("GH-1")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "刷新失败，点击重试" }));
    expect(onRetry).toHaveBeenCalledTimes(1);
  });

  it("replaces the list with the error panel only when nothing is loaded", () => {
    renderList({ isError: true, entries: [] });

    expect(screen.queryByTestId("issue-source-list")).not.toBeInTheDocument();
    expect(screen.getByText("来源列表加载失败")).toBeInTheDocument();
  });

  it("renders reference, title, state and highlights the current issue", () => {
    renderList({ showState: true });

    const list = screen.getByTestId("issue-source-list");
    expect(list).toBeInTheDocument();
    const current = screen.getByRole("button", { current: true });
    expect(current).toHaveAttribute("data-issue-id", "issue-1");
    expect(current).toHaveTextContent("GH-1");
    expect(current).toHaveTextContent("崩溃");
    expect(screen.getByText("CLOSED")).toBeInTheDocument();
    expect(screen.getAllByText("OPEN").length).toBeGreaterThan(0);
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

  it("keeps the full title available via the title attribute while truncating", () => {
    renderList({
      entries: [{ issue: makeIssue("issue-1", "GH-1", "崩溃：打开主界面时白屏并且控制台报错") }],
    });

    expect(screen.getByRole("button", { name: /GH-1/ })).toHaveAttribute(
      "title",
      "崩溃：打开主界面时白屏并且控制台报错",
    );
  });

  it("shows the state markers only when the source context allows mixed states", () => {
    // 判定由来源上下文稳定决定（页面传 showState），不随已加载页的动态混态变化：
    // 看板列固定 open、带状态筛选的列表来源不传 —— 不显示，避免整列重复
    const single = renderList({
      entries: [
        { issue: makeIssue("issue-1", "GH-1", "崩溃") },
        { issue: makeIssue("issue-2", "GH-2", "卡顿") },
      ],
    });
    expect(within(single.container).queryByText("OPEN")).not.toBeInTheDocument();
    // 首页全 open 的可混态来源（列表 · 全部）也逐行恒显示：翻页后出现 closed 行时
    // 先前行不会补显文字回流
    const allOpen = renderList({
      showState: true,
      entries: [
        { issue: makeIssue("issue-1", "GH-1", "崩溃") },
        { issue: makeIssue("issue-2", "GH-2", "卡顿") },
      ],
    });
    expect(within(allOpen.container).getAllByText("OPEN").length).toBe(2);
    // 可混态来源里 closed 行显示 Closed
    const mixed = renderList({ showState: true });
    expect(within(mixed.container).getByText("OPEN")).toBeInTheDocument();
    expect(within(mixed.container).getByText("CLOSED")).toBeInTheDocument();
  });

  it("hides the internal status for board column sources", () => {
    renderList({
      showInternalStatus: false,
      entries: [
        {
          issue: makeIssue("issue-1", "INT-1", "崩溃", {
            source: "internal",
            internal_meta: {
              workflow_status: "in_progress",
              progress_percent: null,
              checklist_total: 0,
              checklist_done: 0,
            },
          }),
        },
      ],
    });

    expect(screen.queryByText("开发中")).not.toBeInTheDocument();
  });

  it("shows the internal status dot with its label by default", () => {
    renderList({
      entries: [
        {
          issue: makeIssue("issue-1", "INT-1", "崩溃", {
            source: "internal",
            internal_meta: {
              workflow_status: "in_progress",
              progress_percent: null,
              checklist_total: 0,
              checklist_done: 0,
            },
          }),
        },
      ],
    });

    expect(screen.getByText("开发中")).toBeInTheDocument();
  });

  it("caps labels at two with an overflow count", () => {
    renderList({
      entries: [
        {
          issue: makeIssue("issue-1", "GH-1", "崩溃", {
            github: {
              ...baseGithubMeta(),
              labels: [
                { name: "bug", color: "d73a4a", description: "" },
                { name: "ui", color: "1d76db", description: "" },
                { name: "perf", color: "fbca04", description: "" },
              ],
            },
          }),
        },
      ],
    });

    expect(screen.getByText("bug")).toBeInTheDocument();
    expect(screen.getByText("ui")).toBeInTheDocument();
    expect(screen.queryByText("perf")).not.toBeInTheDocument();
    expect(screen.getByText("+1")).toBeInTheDocument();
  });

  it("shows the pull request aggregate only when there are linked prs", () => {
    const summary = { total: 3, open: 1, merged: 2 };
    renderList({
      entries: [
        { issue: makeIssue("issue-1", "GH-1", "带 PR 的问题", { pull_request_summary: summary }) },
        { issue: makeIssue("issue-2", "GH-2", "无 PR 的问题") },
      ],
    });

    expect(screen.getByText("PR 3 · 合 2")).toBeInTheDocument();
    expect(screen.queryByText(/PR 0/)).not.toBeInTheDocument();
  });

  it("shows the relative updated time", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-04-13T10:00:00Z"));
    try {
      renderList();
      // updated_at 2026-04-12T10:00:00Z → 恰好 1 天前
      expect(screen.getAllByText("1 天前").length).toBe(2);
    } finally {
      vi.useRealTimers();
    }
  });

  it("shows up to three assignee avatars for github issues only", () => {
    const assignees = [
      { login: "alice", avatar_url: "" },
      { login: "bob", avatar_url: "" },
      { login: "carol", avatar_url: "" },
      { login: "dave", avatar_url: "" },
    ];
    renderList({
      entries: [
        {
          issue: makeIssue("issue-1", "GH-1", "多人协作", {
            github: { ...baseGithubMeta(), assignees },
          }),
        },
        {
          issue: makeIssue("issue-2", "INT-2", "内部问题", {
            source: "internal",
          }),
        },
      ],
    });

    const list = screen.getByTestId("issue-source-list");
    expect(within(list).getByTitle("alice、bob、carol、dave")).toBeInTheDocument();
    expect(within(list).getByText("+1")).toBeInTheDocument();
  });

  it("keeps the stale row weakened with the marker instead of snapshot metadata", () => {
    renderList({
      entries: [
        { issue: makeIssue("issue-1", "GH-1", "崩溃") },
        {
          issue: makeIssue("issue-x", "GH-9", "已移出", {
            state: "closed",
            updated_at: "2020-01-01T00:00:00Z",
          }),
          stale: true,
        },
      ],
    });

    const stale = screen.getByRole("button", { name: /GH-9/ });
    expect(within(stale).getByText("已不符合来源条件")).toBeInTheDocument();
    // 快照状态/时间可能已过期：stale 行不再展示 Open/Closed 与相对时间，
    // 也不得回退成绝对日期——整行 textContent 不含任何日期形状
    expect(within(stale).queryByText("CLOSED")).not.toBeInTheDocument();
    expect(within(stale).queryByText(/天前|分钟前|小时前/)).not.toBeInTheDocument();
    expect(stale.textContent).not.toMatch(/\d{4}-\d{2}-\d{2}/);
  });

  it("routes assignee avatars through the media proxy with the auth token", () => {
    // 回归：负责人头像是服务端改写的媒体代理 URL，代理要求认证——
    // 裸 <img src=avatar_url> 会 401 只剩首字母；必须带 token 过 toGitHubMediaProxyUrl。
    // jsdom 里 Avatar 不渲染真实 <img>，改为断言转换函数收到非空 URL 与 token
    vi.mocked(toGitHubMediaProxyUrl).mockClear();
    renderList({
      token: "jwt-token",
      entries: [
        {
          issue: makeIssue("issue-1", "GH-1", "崩溃", {
            github: {
              ...baseGithubMeta(),
              assignees: [
                { login: "alice", avatar_url: "https://avatars.githubusercontent.com/u/9?v=4" },
              ],
            },
          }),
        },
      ],
    });

    expect(vi.mocked(toGitHubMediaProxyUrl)).toHaveBeenCalledWith(
      "https://avatars.githubusercontent.com/u/9?v=4",
      "jwt-token",
    );
  });
});
