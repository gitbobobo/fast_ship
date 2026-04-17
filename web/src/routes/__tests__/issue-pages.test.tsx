import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import ProjectDetailPage from "@/routes/projects/$id/index";
import EditInternalIssuePage from "@/routes/projects/$id/issues/$iid/edit";
import IssueDetailPage from "@/routes/projects/$id/issues/$iid";
import NewInternalIssuePage from "@/routes/projects/$id/issues/new";
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
  useCreateIssue,
  useCreateIssueComment,
  useSyncProjectIssues,
  useUpdateIssue,
  useUpdateIssueInternalMeta,
} from "@/lib/hooks/use-issues";
import { useAuthStore } from "@/lib/store/auth-store";
import { toast } from "sonner";

function hasExactTextContent(text: string) {
  return (_: string, element: Element | null) =>
    element?.textContent === text &&
    Array.from(element.children).every((child) => child.textContent !== text);
}

function buildGitHubIssue(
  overrides: Partial<Issue> = {},
  githubOverrides: Partial<IssueGitHubMeta> = {},
): Issue {
  return {
    id: "issue-1",
    project_id: "proj-1",
    source: "github",
    sequence_number: 1,
    reference: `GH-${githubOverrides.number ?? 42}`,
    state: "open",
    state_reason: "",
    title: "Crash on launch",
    body: "",
    body_html: "",
    author: { login: "alice", avatar_url: "" },
    created_at: "2026-04-10T10:00:00Z",
    updated_at: "2026-04-12T10:00:00Z",
    internal_meta: null,
    github: {
      github_issue_id: 1001,
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
      synced_at: "2026-04-12T10:05:00Z",
      ...githubOverrides,
    },
    ...overrides,
  };
}

function buildInternalIssue(overrides: Partial<Issue> = {}): Issue {
  return {
    id: "internal-issue-1",
    project_id: "proj-1",
    source: "internal",
    sequence_number: 7,
    reference: "INT-7",
    state: "open",
    state_reason: "",
    title: "补充站内发布提醒",
    body: "## 背景\n\n需要在版本发布后提醒测试同学。",
    body_html: "",
    author: { login: "alice", avatar_url: "" },
    created_at: "2026-04-10T10:00:00Z",
    updated_at: "2026-04-12T10:00:00Z",
    internal_meta: {
      workflow_status: "todo",
      updated_at: "2026-04-12T10:00:00Z",
    },
    github: null,
    ...overrides,
  };
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
  useCreateIssue: vi.fn(),
  useCreateIssueComment: vi.fn(),
  useSyncProjectIssues: vi.fn(),
  useUpdateIssue: vi.fn(),
  useUpdateIssueInternalMeta: vi.fn(),
}));

vi.mock("@/lib/store/auth-store", () => ({
  useAuthStore: vi.fn(),
}));

function mockIssueDetailData() {
  vi.mocked(useIssues).mockReturnValue({
    data: {
      items: [
        buildGitHubIssue(
          {
            id: "issue-0",
            title: "Previous issue",
            reference: "GH-41",
            created_at: "2026-04-09T10:00:00Z",
            updated_at: "2026-04-09T10:00:00Z",
          },
          {
            github_issue_id: 1000,
            github_node_id: "I_kw0",
            number: 41,
            html_url: "https://github.com/acme/alpha/issues/41",
          },
        ),
        buildGitHubIssue(
          {
            id: "issue-1",
            title: "Crash on launch",
            reference: "GH-42",
            internal_meta: {
              workflow_status: "in_progress",
              started_at: "2026-04-12T09:00:00Z",
              updated_at: "2026-04-12T09:00:00Z",
            },
          },
          {
            comments_count: 1,
          },
        ),
        buildGitHubIssue(
          {
            id: "issue-2",
            title: "Next issue",
            reference: "GH-43",
            created_at: "2026-04-13T10:00:00Z",
            updated_at: "2026-04-13T10:00:00Z",
          },
          {
            github_issue_id: 1002,
            github_node_id: "I_kw2",
            number: 43,
            html_url: "https://github.com/acme/alpha/issues/43",
            synced_at: "2026-04-13T10:05:00Z",
          },
        ),
      ],
      total: 3,
      page: 1,
      page_size: 20,
    },
    isLoading: false,
  } as unknown as ReturnType<typeof useIssues>);

  vi.mocked(useIssue).mockReturnValue({
    data: buildGitHubIssue(
      {
        id: "issue-1",
        title: "Crash on launch",
        reference: "GH-42",
        body: "## Steps\n\nOpen the app",
        body_html: "<h2>Steps</h2><p>Open the <strong>app</strong></p>",
        internal_meta: {
          workflow_status: "in_progress",
          started_at: "2026-04-12T09:00:00Z",
          updated_at: "2026-04-12T09:00:00Z",
        },
      },
      {
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
      },
    ),
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
  const authState = {
    token: "jwt-token" as string | null,
    user: null as User | null,
    setAuth: vi.fn(),
    setUser: vi.fn(),
    logout: vi.fn(),
  };

  beforeEach(() => {
    authState.token = "jwt-token";
    vi.mocked(useAuthStore).mockImplementation(((selector?: (state: typeof authState) => unknown) =>
      selector ? selector(authState) : authState) as typeof useAuthStore);
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
    vi.mocked(useCreateIssue).mockReturnValue({
      mutateAsync: vi.fn(),
      isPending: false,
    } as unknown as ReturnType<typeof useCreateIssue>);
    vi.mocked(useCreateIssueComment).mockReturnValue({
      mutateAsync: vi.fn(),
      isPending: false,
    } as unknown as ReturnType<typeof useCreateIssueComment>);
    vi.mocked(useUpdateIssue).mockReturnValue({
      mutateAsync: vi.fn(),
      isPending: false,
    } as unknown as ReturnType<typeof useUpdateIssue>);

    vi.mocked(useUpdateIssueInternalMeta).mockReturnValue({
      mutateAsync: vi.fn(),
      isPending: false,
    } as unknown as ReturnType<typeof useUpdateIssueInternalMeta>);

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
          buildGitHubIssue(
            {
              id: "issue-1",
              body: "App crashes",
              internal_meta: {
                workflow_status: "done",
                started_at: "2026-04-11T10:00:00Z",
                completed_at: "2026-04-12T09:30:00Z",
                updated_at: "2026-04-12T09:30:00Z",
              },
            },
            {
              labels: [{ name: "bug", color: "d73a4a", description: "" }],
              comments_count: 3,
            },
          ),
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

  it("renders issue details with unified timeline", async () => {
    mockIssueDetailData();

    renderWithRoute(<IssueDetailPage />, {
      path: "/projects/:id/issues/:iid",
      initialEntry: "/projects/proj-1/issues/issue-1#timeline",
    });

    expect(screen.getByText("GH-42 Crash on launch")).toBeInTheDocument();
    expect(screen.getByText("负责人")).toBeInTheDocument();
    expect(screen.getAllByText("开发中").length).toBeGreaterThan(0);
    expect(screen.getByText("上一条")).toBeInTheDocument();
    expect(screen.getByText("下一条")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "返回" })).toBeInTheDocument();
    expect(screen.getByText("关闭了问题")).toBeInTheDocument();
    expect(screen.getByText(hasExactTextContent("Looks good"))).toBeInTheDocument();
    expect(screen.getByText(hasExactTextContent("Open the app"))).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "更多" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "复制链接" }));
    await waitFor(() =>
      expect(writeText).toHaveBeenCalledWith(
        `${window.location.origin}/projects/proj-1/issues/issue-1#timeline`,
      ),
    );
    expect(toast.success).toHaveBeenCalledWith("已复制当前问题视图链接");

    fireEvent.click(screen.getByRole("button", { name: "更多" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "复制 GitHub 深链接" }));
    await waitFor(() =>
      expect(writeText).toHaveBeenCalledWith("https://github.com/acme/alpha/issues/42"),
    );
    expect(toast.success).toHaveBeenCalledWith("已复制 GitHub 深链接");
  });

  it("supports direct comment anchors in unified timeline", async () => {
    mockIssueDetailData();

    renderWithRoute(<IssueDetailPage />, {
      path: "/projects/:id/issues/:iid",
      initialEntry: "/projects/proj-1/issues/issue-1#issuecomment-5001",
    });

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

  it("supports direct timeline anchors in unified timeline", async () => {
    mockIssueDetailData();

    renderWithRoute(<IssueDetailPage />, {
      path: "/projects/:id/issues/:iid",
      initialEntry: "/projects/proj-1/issues/issue-1#issueevent-1",
    });

    expect(screen.getByText("关闭了问题")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "定位到动态 1" })).toBeInTheDocument();
    expect(document.getElementById("issueevent-1")).toHaveClass("bg-primary/5");

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

  it("updates internal workflow status from the issue detail page", async () => {
    mockIssueDetailData();
    const user = userEvent.setup();
    const mutateAsync = vi.fn().mockResolvedValue({
      data: {
        workflow_status: "done",
        started_at: "2026-04-12T09:00:00Z",
        completed_at: "2026-04-12T10:00:00Z",
        updated_at: "2026-04-12T10:00:00Z",
      },
    });
    vi.mocked(useUpdateIssueInternalMeta).mockReturnValue({
      mutateAsync,
      isPending: false,
    } as unknown as ReturnType<typeof useUpdateIssueInternalMeta>);

    renderWithRoute(<IssueDetailPage />, {
      path: "/projects/:id/issues/:iid",
      initialEntry: "/projects/proj-1/issues/issue-1",
    });

    await user.click(screen.getByRole("combobox"));
    await user.click(await screen.findByRole("option", { name: "已完成" }));

    await waitFor(() =>
      expect(mutateAsync).toHaveBeenCalledWith({ workflow_status: "done" }),
    );
    expect(toast.success).toHaveBeenCalledWith("已更新内部状态");
  });

  it("preserves list filters when linking from the issues page to detail", async () => {
    vi.mocked(useIssues).mockReturnValue({
      data: {
        items: [
          buildGitHubIssue(
            {
              id: "issue-1",
              body: "App crashes",
            },
            {
              labels: [{ name: "bug", color: "d73a4a", description: "" }],
              comments_count: 3,
            },
          ),
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
      expect(screen.getByRole("link", { name: /GH-42 Crash on launch/i })).toHaveAttribute(
        "href",
        "/projects/proj-1/issues/issue-1?issue_state=open&issue_q=crash&issue_label=bug&issue_assignee=bob&issue_milestone=1.0.1&issue_sort=updated_desc&issue_page=2",
      ),
    );
  });

  it("renders sanitized raw html fallback when body_html is missing", async () => {
    mockIssueDetailData();

    vi.mocked(useIssue).mockReturnValue({
      data: buildGitHubIssue(
        {
          body: '<img width="1000" height="700" alt="Image" src="https://github.com/user-attachments/assets/demo" /><script>alert(1)</script>',
          body_html: "",
        },
        {
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
        },
      ),
      isLoading: false,
    } as unknown as ReturnType<typeof useIssue>);

    renderWithRoute(<IssueDetailPage />, {
      path: "/projects/:id/issues/:iid",
      initialEntry: "/projects/proj-1/issues/issue-1",
    });

    expect(screen.getByRole("img", { name: "Image" })).toHaveAttribute(
      "src",
      "/api/github/media-proxy?url=https%3A%2F%2Fgithub.com%2Fuser-attachments%2Fassets%2Fdemo&token=jwt-token",
    );
    expect(document.querySelector("script")).toBeNull();
  });

  it("appends the auth token to proxy urls in rendered html", async () => {
    mockIssueDetailData();
    vi.mocked(useIssue).mockReturnValue({
      data: buildGitHubIssue({
        body: "fallback",
        body_html:
          '<p><img alt="Rendered image" src="/api/github/media-proxy?url=https%3A%2F%2Fgithub.com%2Fuser-attachments%2Fassets%2Fdemo" /></p>',
      }),
      isLoading: false,
    } as unknown as ReturnType<typeof useIssue>);

    renderWithRoute(<IssueDetailPage />, {
      path: "/projects/:id/issues/:iid",
      initialEntry: "/projects/proj-1/issues/issue-1",
    });

    expect(screen.getByRole("img", { name: "Rendered image" })).toHaveAttribute(
      "src",
      "/api/github/media-proxy?url=https%3A%2F%2Fgithub.com%2Fuser-attachments%2Fassets%2Fdemo&token=jwt-token",
    );
  });

  it("creates an internal issue from a dedicated page", async () => {
    const user = userEvent.setup();
    const mutateAsync = vi.fn().mockResolvedValue({
      data: buildInternalIssue(),
    });
    vi.mocked(useCreateIssue).mockReturnValue({
      mutateAsync,
      isPending: false,
    } as unknown as ReturnType<typeof useCreateIssue>);

    renderWithRoute(<NewInternalIssuePage />, {
      path: "/projects/:id/issues/new",
      initialEntry: "/projects/proj-1/issues/new?project=proj-1&state=open&q=ship",
    });

    await user.type(screen.getByLabelText("标题"), "需要补充交付通知");
    await user.type(screen.getByLabelText("描述"), "## 验收\n\n完成后通知 QA");
    await user.click(screen.getByRole("combobox"));
    await user.click(await screen.findByRole("option", { name: "开发中" }));
    await user.click(screen.getByRole("button", { name: "创建问题" }));

    await waitFor(() =>
      expect(mutateAsync).toHaveBeenCalledWith({
        title: "需要补充交付通知",
        body: "## 验收\n\n完成后通知 QA",
        workflow_status: "in_progress",
      }),
    );
    expect(toast.success).toHaveBeenCalledWith("内部问题已创建");
  });

  it("edits an internal issue from a dedicated page", async () => {
    const user = userEvent.setup();
    const mutateAsync = vi.fn().mockResolvedValue({
      data: buildInternalIssue({
        title: "补充回归通知",
        body: "更新后的描述",
      }),
    });
    vi.mocked(useIssue).mockReturnValue({
      data: buildInternalIssue(),
      isLoading: false,
    } as unknown as ReturnType<typeof useIssue>);
    vi.mocked(useUpdateIssue).mockReturnValue({
      mutateAsync,
      isPending: false,
    } as unknown as ReturnType<typeof useUpdateIssue>);

    renderWithRoute(<EditInternalIssuePage />, {
      path: "/projects/:id/issues/:iid/edit",
      initialEntry: "/projects/proj-1/issues/internal-issue-1/edit?issue_state=open",
    });

    const titleInput = screen.getByLabelText("标题");
    const bodyInput = screen.getByLabelText("描述");

    await user.clear(titleInput);
    await user.type(titleInput, "补充回归通知");
    await user.clear(bodyInput);
    await user.type(bodyInput, "更新后的描述");
    await user.click(screen.getByRole("button", { name: "保存修改" }));

    await waitFor(() =>
      expect(mutateAsync).toHaveBeenCalledWith({
        title: "补充回归通知",
        body: "更新后的描述",
      }),
    );
    expect(toast.success).toHaveBeenCalledWith("内部问题已更新");
  });
});
