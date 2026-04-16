import { fireEvent, screen, waitFor } from "@testing-library/react";
import { vi } from "vitest";
import ProjectDetailPage from "@/routes/projects/$id/index";
import IssueDetailPage from "@/routes/projects/$id/issues/$iid";
import IssuesPage from "@/routes/issues/index";
import { renderWithRoute } from "@/test/render";
import { useDeleteProject, useProject, useProjects } from "@/lib/hooks/use-projects";
import { useVersions } from "@/lib/hooks/use-versions";
import {
  useIssues,
  useIssue,
  useIssueFilterOptions,
  useInfiniteIssueComments,
  useInfiniteIssueTimeline,
  useSyncProjectIssues,
} from "@/lib/hooks/use-issues";
import { toast } from "sonner";

function hasExactTextContent(text: string) {
  return (_: string, element: Element | null) =>
    element?.textContent === text &&
    Array.from(element.children).every((child) => child.textContent !== text);
}

vi.mock("sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));

vi.mock("@/lib/hooks/use-projects", () => ({
  useProjects: vi.fn(),
  useProject: vi.fn(),
  useDeleteProject: vi.fn(),
}));

vi.mock("@/lib/hooks/use-versions", () => ({
  useVersions: vi.fn(),
}));

vi.mock("@/lib/hooks/use-issues", () => ({
  useIssues: vi.fn(),
  useIssue: vi.fn(),
  useIssueFilterOptions: vi.fn(),
  useInfiniteIssueComments: vi.fn(),
  useInfiniteIssueTimeline: vi.fn(),
  useSyncProjectIssues: vi.fn(),
}));

function mockIssueDetailData() {
  vi.mocked(useIssues).mockReturnValue({
    data: {
      items: [
        {
          id: "issue-0",
          project_id: "proj-1",
          github_issue_id: 1000,
          github_node_id: "I_kw0",
          number: 41,
          state: "open",
          state_reason: "",
          title: "Previous issue",
          body: "",
          body_html: "",
          html_url: "https://github.com/acme/alpha/issues/41",
          author: { login: "alice", avatar_url: "" },
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
          created_at: "2026-04-09T10:00:00Z",
          updated_at: "2026-04-09T10:00:00Z",
          synced_at: "2026-04-12T10:05:00Z",
        },
        {
          id: "issue-1",
          project_id: "proj-1",
          github_issue_id: 1001,
          github_node_id: "I_kw",
          number: 42,
          state: "open",
          state_reason: "",
          title: "Crash on launch",
          body: "",
          body_html: "",
          html_url: "https://github.com/acme/alpha/issues/42",
          author: { login: "alice", avatar_url: "" },
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
          comments_count: 1,
          locked: false,
          active_lock_reason: "",
          created_at: "2026-04-10T10:00:00Z",
          updated_at: "2026-04-12T10:00:00Z",
          synced_at: "2026-04-12T10:05:00Z",
        },
        {
          id: "issue-2",
          project_id: "proj-1",
          github_issue_id: 1002,
          github_node_id: "I_kw2",
          number: 43,
          state: "open",
          state_reason: "",
          title: "Next issue",
          body: "",
          body_html: "",
          html_url: "https://github.com/acme/alpha/issues/43",
          author: { login: "alice", avatar_url: "" },
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
          created_at: "2026-04-13T10:00:00Z",
          updated_at: "2026-04-13T10:00:00Z",
          synced_at: "2026-04-13T10:05:00Z",
        },
      ],
      total: 3,
      page: 1,
      page_size: 20,
    },
    isLoading: false,
  } as unknown as ReturnType<typeof useIssues>);

  vi.mocked(useIssue).mockReturnValue({
    data: {
      id: "issue-1",
      project_id: "proj-1",
      github_issue_id: 1001,
      github_node_id: "I_kw",
      number: 42,
      state: "open",
      state_reason: "",
      title: "Crash on launch",
      body: "## Steps\n\nOpen the app",
      body_html: "<h2>Steps</h2><p>Open the <strong>app</strong></p>",
      html_url: "https://github.com/acme/alpha/issues/42",
      author: { login: "alice", avatar_url: "" },
      author_association: "MEMBER",
      assignees: [{ login: "bob", avatar_url: "" }],
      labels: [{ name: "bug", color: "d73a4a", description: "" }],
      milestone: { number: 1, title: "1.0.1", state: "open", description: "" },
      reactions: {
        total_count: 1,
        "+1": 1,
        "-1": 0,
        laugh: 0,
        hooray: 0,
        confused: 0,
        heart: 0,
        rocket: 0,
        eyes: 0,
      },
      comments_count: 1,
      locked: false,
      active_lock_reason: "",
      created_at: "2026-04-10T10:00:00Z",
      updated_at: "2026-04-12T10:00:00Z",
      synced_at: "2026-04-12T10:05:00Z",
    },
    isLoading: false,
  } as unknown as ReturnType<typeof useIssue>);

  vi.mocked(useInfiniteIssueComments).mockReturnValue({
    data: {
      pages: [
        {
          items: [
            {
              id: "comment-1",
              issue_id: "issue-1",
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
            },
          ],
          total: 1,
          page: 1,
          page_size: 20,
        },
      ],
    },
    fetchNextPage: vi.fn(),
    hasNextPage: false,
    isFetchingNextPage: false,
    isLoading: false,
  } as unknown as ReturnType<typeof useInfiniteIssueComments>);

  vi.mocked(useInfiniteIssueTimeline).mockReturnValue({
    data: {
      pages: [
        {
          items: [
            {
              id: "event-1",
              issue_id: "issue-1",
              event_key: "gh:1",
              event_type: "closed",
              github_event_id: 1,
              actor: { login: "alice", avatar_url: "" },
              body: "",
              summary: "关闭了问题",
              payload: {},
              created_at: "2026-04-12T10:00:00Z",
            },
          ],
          total: 1,
          page: 1,
          page_size: 20,
        },
      ],
    },
    fetchNextPage: vi.fn(),
    hasNextPage: false,
    isFetchingNextPage: false,
    isLoading: false,
  } as unknown as ReturnType<typeof useInfiniteIssueTimeline>);
}

describe("Issue pages", () => {
  const writeText = vi.fn();
  const openWindow = vi.fn();

  beforeEach(() => {
    window.HTMLElement.prototype.scrollIntoView = vi.fn();
    writeText.mockResolvedValue(undefined);
    openWindow.mockReset();

    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });

    Object.defineProperty(window, "open", {
      configurable: true,
      value: openWindow,
    });

    vi.mocked(useDeleteProject).mockReturnValue({
      mutateAsync: vi.fn(),
    } as unknown as ReturnType<typeof useDeleteProject>);

    vi.mocked(useProjects).mockReturnValue({
      data: {
        items: [
          {
            id: "proj-1",
            user_id: "user-1",
            name: "Alpha App",
            description: "Release automation",
            github_owner: "acme",
            github_repo: "alpha",
            issue_sync: {
              status: "completed",
              last_synced_at: "2026-04-12T10:05:00Z",
              last_successful_sync_at: "2026-04-12T10:05:00Z",
              last_issue_updated_at: "2026-04-12T10:00:00Z",
              last_error: "",
            },
            created_at: "2026-04-10T10:00:00Z",
            updated_at: "2026-04-10T10:00:00Z",
          },
        ],
        total: 1,
        page: 1,
        page_size: 100,
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useProjects>);

    vi.mocked(useSyncProjectIssues).mockReturnValue({
      mutateAsync: vi.fn(),
      isPending: false,
    } as unknown as ReturnType<typeof useSyncProjectIssues>);

    vi.mocked(useIssueFilterOptions).mockReturnValue({
      data: {
        labels: ["bug"],
        assignees: ["bob"],
        milestones: ["1.0.1"],
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useIssueFilterOptions>);

    vi.mocked(useInfiniteIssueComments).mockReturnValue({
      data: {
        pages: [
          {
            items: [],
            total: 0,
            page: 1,
            page_size: 20,
          },
        ],
      },
      fetchNextPage: vi.fn(),
      hasNextPage: false,
      isFetchingNextPage: false,
      isLoading: false,
    } as unknown as ReturnType<typeof useInfiniteIssueComments>);

    vi.mocked(useInfiniteIssueTimeline).mockReturnValue({
      data: {
        pages: [
          {
            items: [],
            total: 0,
            page: 1,
            page_size: 20,
          },
        ],
      },
      fetchNextPage: vi.fn(),
      hasNextPage: false,
      isFetchingNextPage: false,
      isLoading: false,
    } as unknown as ReturnType<typeof useInfiniteIssueTimeline>);
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it("renders issues in the project detail page", async () => {
    vi.mocked(useProject).mockReturnValue({
      data: {
        id: "proj-1",
        user_id: "user-1",
        name: "Alpha App",
        description: "Release automation",
        github_owner: "acme",
        github_repo: "alpha",
        issue_sync: {
          status: "completed",
          last_synced_at: "2026-04-12T10:05:00Z",
          last_successful_sync_at: "2026-04-12T10:05:00Z",
          last_issue_updated_at: "2026-04-12T10:00:00Z",
          last_error: "",
        },
        created_at: "2026-04-10T10:00:00Z",
        updated_at: "2026-04-10T10:00:00Z",
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useProject>);

    vi.mocked(useVersions).mockReturnValue({
      data: { items: [], total: 0, page: 1, page_size: 20 },
      isLoading: false,
    } as unknown as ReturnType<typeof useVersions>);

    vi.mocked(useIssues).mockReturnValue({
      data: {
        items: [
          {
            id: "issue-1",
            project_id: "proj-1",
            github_issue_id: 1001,
            github_node_id: "I_kw",
            number: 42,
            state: "open",
            state_reason: "",
            title: "Crash on launch",
            body: "App crashes",
            body_html: "",
            html_url: "https://github.com/acme/alpha/issues/42",
            author: { login: "alice", avatar_url: "" },
            author_association: "MEMBER",
            assignees: [],
            labels: [{ name: "bug", color: "d73a4a", description: "" }],
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
            comments_count: 3,
            locked: false,
            active_lock_reason: "",
            created_at: "2026-04-10T10:00:00Z",
            updated_at: "2026-04-12T10:00:00Z",
            synced_at: "2026-04-12T10:05:00Z",
          },
        ],
        total: 1,
        page: 1,
        page_size: 20,
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useIssues>);

    renderWithRoute(<ProjectDetailPage />, {
      path: "/projects/:id",
      initialEntry: "/projects/proj-1",
    });

    expect(screen.getByText("问题")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "查看全部问题" })).toHaveAttribute(
      "href",
      "/issues?project=proj-1",
    );
    fireEvent.click(screen.getByRole("button", { name: "同步 GitHub Issues" }));
    expect(screen.getByText("同步完成")).toBeInTheDocument();
    await waitFor(() =>
      expect(writeText).not.toHaveBeenCalled(),
    );
  });

  it("renders issue details with anchor-driven tabs and remembers current tab", async () => {
    mockIssueDetailData();

    renderWithRoute(<IssueDetailPage />, {
      path: "/projects/:id/issues/:iid",
      initialEntry: "/projects/proj-1/issues/issue-1?issue_tab=timeline#timeline",
    });

    expect(screen.getByText("#42 Crash on launch")).toBeInTheDocument();
    expect(screen.getByText("负责人")).toBeInTheDocument();
    expect(screen.getByText("上一条")).toBeInTheDocument();
    expect(screen.getByText("下一条")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "返回" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "动态" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByText("关闭了问题")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "更多" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "复制链接" }));
    await waitFor(() =>
      expect(writeText).toHaveBeenCalledWith(
        `${window.location.origin}/projects/proj-1/issues/issue-1?issue_tab=timeline#timeline`,
      ),
    );
    expect(toast.success).toHaveBeenCalledWith("已复制当前问题视图链接");

    fireEvent.click(screen.getByRole("button", { name: "更多" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "复制 GitHub 深链接" }));
    await waitFor(() =>
      expect(writeText).toHaveBeenCalledWith("https://github.com/acme/alpha/issues/42"),
    );
    expect(toast.success).toHaveBeenCalledWith("已复制 GitHub 深链接");

    fireEvent.click(screen.getByRole("tab", { name: "评论" }));

    expect(screen.getByRole("tab", { name: "评论" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByText(hasExactTextContent("Looks good"))).toBeInTheDocument();
    expect(screen.getByText(hasExactTextContent("Open the app"))).toBeInTheDocument();
  });

  it("supports direct comment anchors and lets comments override timeline tab state", async () => {
    mockIssueDetailData();

    renderWithRoute(<IssueDetailPage />, {
      path: "/projects/:id/issues/:iid",
      initialEntry: "/projects/proj-1/issues/issue-1?issue_tab=timeline#issuecomment-5001",
    });

    expect(screen.getByRole("tab", { name: "评论" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByText(hasExactTextContent("Looks good"))).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "定位到评论 5001" })).toBeInTheDocument();
    expect(document.getElementById("issuecomment-5001")).toHaveClass("ring-2");

    fireEvent.click(screen.getByRole("button", { name: "更多" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "复制 GitHub 深链接" }));
    await waitFor(() =>
      expect(writeText).toHaveBeenCalledWith(
        "https://github.com/acme/alpha/issues/42#issuecomment-1",
      ),
    );
    expect(toast.success).toHaveBeenCalledWith("已复制 GitHub 深链接");

    fireEvent.click(screen.getByRole("button", { name: "复制评论 5001 的 GitHub 深链接" }));
    await waitFor(() =>
      expect(writeText).toHaveBeenCalledWith(
        "https://github.com/acme/alpha/issues/42#issuecomment-1",
      ),
    );
  });

  it("supports direct timeline anchors and lets timeline override comment tab state", async () => {
    mockIssueDetailData();

    renderWithRoute(<IssueDetailPage />, {
      path: "/projects/:id/issues/:iid",
      initialEntry: "/projects/proj-1/issues/issue-1#issueevent-1",
    });

    expect(screen.getByRole("tab", { name: "动态" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByText("关闭了问题")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "定位到动态 1" })).toBeInTheDocument();
    expect(document.getElementById("issueevent-1")).toHaveClass("ring-2");

    fireEvent.click(screen.getByRole("button", { name: "更多" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "复制 GitHub 深链接" }));
    await waitFor(() =>
      expect(writeText).toHaveBeenCalledWith("https://github.com/acme/alpha/issues/42"),
    );
    expect(toast.success).toHaveBeenCalledWith("当前动态没有精确 GitHub 链接，已复制问题链接");

    fireEvent.click(screen.getByRole("button", { name: "复制动态 1 的 GitHub 深链接" }));
    await waitFor(() =>
      expect(writeText).toHaveBeenCalledWith("https://github.com/acme/alpha/issues/42"),
    );
  });

  it("preserves list filters when linking from the issues page to detail", async () => {
    vi.mocked(useIssues).mockReturnValue({
      data: {
        items: [
          {
            id: "issue-1",
            project_id: "proj-1",
            github_issue_id: 1001,
            github_node_id: "I_kw",
            number: 42,
            state: "open",
            state_reason: "",
            title: "Crash on launch",
            body: "App crashes",
            body_html: "",
            html_url: "https://github.com/acme/alpha/issues/42",
            author: { login: "alice", avatar_url: "" },
            author_association: "MEMBER",
            assignees: [],
            labels: [{ name: "bug", color: "d73a4a", description: "" }],
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
            comments_count: 3,
            locked: false,
            active_lock_reason: "",
            created_at: "2026-04-10T10:00:00Z",
            updated_at: "2026-04-12T10:00:00Z",
            synced_at: "2026-04-12T10:05:00Z",
          },
        ],
        total: 30,
        page: 2,
        page_size: 20,
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useIssues>);

    renderWithRoute(<IssuesPage />, {
      path: "/issues",
      initialEntry:
        "/issues?project=proj-1&state=open&q=crash&label=bug&assignee=bob&milestone=1.0.1&sort=updated_desc&page=2",
    });

    await waitFor(() =>
      expect(screen.getByRole("link", { name: /#42 Crash on launch/i })).toHaveAttribute(
        "href",
        "/projects/proj-1/issues/issue-1?issue_state=open&issue_q=crash&issue_label=bug&issue_assignee=bob&issue_milestone=1.0.1&issue_sort=updated_desc&issue_page=2",
      ),
    );
  });

  it("renders sanitized raw html fallback when body_html is missing", async () => {
    mockIssueDetailData();

    vi.mocked(useIssue).mockReturnValue({
      data: {
        id: "issue-1",
        project_id: "proj-1",
        github_issue_id: 1001,
        github_node_id: "I_kw",
        number: 42,
        state: "open",
        state_reason: "",
        title: "Crash on launch",
        body: '<img width="1000" height="700" alt="Image" src="https://github.com/user-attachments/assets/demo" /><script>alert(1)</script>',
        body_html: "",
        html_url: "https://github.com/acme/alpha/issues/42",
        author: { login: "alice", avatar_url: "" },
        author_association: "MEMBER",
        assignees: [{ login: "bob", avatar_url: "" }],
        labels: [{ name: "bug", color: "d73a4a", description: "" }],
        milestone: { number: 1, title: "1.0.1", state: "open", description: "" },
        reactions: {
          total_count: 1,
          "+1": 1,
          "-1": 0,
          laugh: 0,
          hooray: 0,
          confused: 0,
          heart: 0,
          rocket: 0,
          eyes: 0,
        },
        comments_count: 1,
        locked: false,
        active_lock_reason: "",
        created_at: "2026-04-10T10:00:00Z",
        updated_at: "2026-04-12T10:00:00Z",
        synced_at: "2026-04-12T10:05:00Z",
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useIssue>);

    renderWithRoute(<IssueDetailPage />, {
      path: "/projects/:id/issues/:iid",
      initialEntry: "/projects/proj-1/issues/issue-1",
    });

    expect(screen.getByRole("img", { name: "Image" })).toHaveAttribute(
      "src",
      "https://github.com/user-attachments/assets/demo",
    );
    expect(document.querySelector("script")).toBeNull();
  });
});
