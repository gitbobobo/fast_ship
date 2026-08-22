import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import * as ReactRouter from "react-router";
import { MemoryRouter, Route, Routes } from "react-router";
import { vi } from "vitest";
import EditInternalIssuePage from "@/routes/projects/$id/issues/$iid/edit";
import IssueDetailPage from "@/routes/projects/$id/issues/$iid";
import NewInternalIssuePage from "@/routes/projects/$id/issues/new";
import IssuesPage from "@/routes/issues/index";
import { renderWithRoute } from "@/test/render";
import { useProject, useProjects } from "@/lib/hooks/use-projects";
import {
  useIssues,
  useIssue,
  useIssueFilterOptions,
  useIssueRepoLabels,
  useInfiniteIssueComments,
  useInfiniteIssueTimeline,
  useCreateIssue,
  useCreateIssueComment,
  useUploadDraftIssueAsset,
  useSyncProjectIssues,
  useUpdateIssue,
  useUpdateIssueInternalMeta,
  useReplaceIssueChecklist,
  useUploadIssueAsset,
  useUpsertIssueShipHook,
  useDeleteIssueShipHook,
} from "@/lib/hooks/use-issues";
import { useIssueChecklistSuggestions } from "@/lib/hooks/use-ai";
import { useAuthStore } from "@/lib/store/auth-store";
import { GITHUB_LOCAL_ASSET_NOTICE } from "@/lib/utils/github-media-proxy";
import { toast } from "sonner";

function hasExactTextContent(text: string) {
  return (_: string, element: Element | null) =>
    element?.textContent === text &&
    Array.from(element.children).every((child) => child.textContent !== text);
}

function createDeferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((promiseResolve, promiseReject) => {
    resolve = promiseResolve;
    reject = promiseReject;
  });

  return { promise, resolve, reject };
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
      progress_percent: 0,
      checklist_total: 0,
      checklist_done: 0,
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
}));

vi.mock("@/lib/hooks/use-issues", () => ({
  useIssues: vi.fn(),
  useIssue: vi.fn(),
  useIssueFilterOptions: vi.fn(),
  useIssueRepoLabels: vi.fn(),
  useInfiniteIssueComments: vi.fn(),
  useInfiniteIssueTimeline: vi.fn(),
  useCreateIssue: vi.fn(),
  useCreateIssueComment: vi.fn(),
  useUploadDraftIssueAsset: vi.fn(),
  useSyncProjectIssues: vi.fn(),
  useUpdateIssue: vi.fn(),
  useUpdateIssueInternalMeta: vi.fn(),
  useReplaceIssueChecklist: vi.fn(),
  useUploadIssueAsset: vi.fn(),
  useUpsertIssueShipHook: vi.fn(),
  useDeleteIssueShipHook: vi.fn(),
}));

vi.mock("@/lib/hooks/use-ai", () => ({
  useAISettings: vi.fn(() => ({ data: { configured: false } })),
  useGenerateTitle: vi.fn(() => ({ mutate: vi.fn(), isPending: false })),
  useIssueChecklistSuggestions: vi.fn(),
}));

vi.mock("@/lib/hooks/use-issue-collab", () => ({
  useIssueCollab: vi.fn(() => ({
    data: { suggestions: [], plan: null, review: null, summary: null },
    isLoading: false,
  })),
}));

vi.mock("@/lib/hooks/use-issue-prompt", () => ({
  useIssuePrompts: vi.fn(),
  useIssuePromptList: vi.fn(() => [
    { id: "default", name: "默认", content: "请处理此问题" },
  ]),
  useUpdateIssuePrompts: vi.fn(),
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
              progress_percent: 50,
              checklist_total: 2,
              checklist_done: 1,
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
          progress_percent: 50,
          checklist_total: 2,
          checklist_done: 1,
          started_at: "2026-04-12T09:00:00Z",
          checklist_updated_at: "2026-04-12T09:00:00Z",
          updated_at: "2026-04-12T09:00:00Z",
          checklist: [
            { id: "chk-1", title: "复现问题", is_completed: true, sort_order: 0 },
            { id: "chk-2", title: "修复崩溃", is_completed: false, sort_order: 1 },
          ],
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
    vi.mocked(useUploadDraftIssueAsset).mockReturnValue({
      mutateAsync: vi.fn(),
      isPending: false,
    } as unknown as ReturnType<typeof useUploadDraftIssueAsset>);
    vi.mocked(useUpdateIssue).mockReturnValue({
      mutateAsync: vi.fn(),
      isPending: false,
    } as unknown as ReturnType<typeof useUpdateIssue>);
    vi.mocked(useUpdateIssueInternalMeta).mockReturnValue({
      mutateAsync: vi.fn(),
      isPending: false,
    } as unknown as ReturnType<typeof useUpdateIssueInternalMeta>);
    vi.mocked(useUploadIssueAsset).mockReturnValue({
      mutateAsync: vi.fn(),
      isPending: false,
    } as unknown as ReturnType<typeof useUploadIssueAsset>);

    vi.mocked(useReplaceIssueChecklist).mockReturnValue({
      mutateAsync: vi.fn(),
      isPending: false,
    } as unknown as ReturnType<typeof useReplaceIssueChecklist>);
    vi.mocked(useUpsertIssueShipHook).mockReturnValue({
      mutateAsync: vi.fn(),
      isPending: false,
    } as unknown as ReturnType<typeof useUpsertIssueShipHook>);
    vi.mocked(useDeleteIssueShipHook).mockReturnValue({
      mutateAsync: vi.fn(),
      isPending: false,
    } as unknown as ReturnType<typeof useDeleteIssueShipHook>);
    vi.mocked(useIssueChecklistSuggestions).mockReturnValue({
      mutateAsync: vi.fn().mockResolvedValue({
        items: [{ title: "补充复现路径" }, { title: "确认影响版本" }],
      }),
      isPending: false,
    } as unknown as ReturnType<typeof useIssueChecklistSuggestions>);

    vi.mocked(useIssueFilterOptions).mockReturnValue({
      data: {
        labels: ["bug"],
        assignees: ["bob"],
        milestones: ["1.0.1"],
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useIssueFilterOptions>);
    vi.mocked(useIssueRepoLabels).mockReturnValue({
      data: [{ name: "bug", color: "d73a4a", description: "" }],
      isLoading: false,
    } as unknown as ReturnType<typeof useIssueRepoLabels>);

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

  it("renders issue details with unified timeline", async () => {
    mockIssueDetailData();

    renderWithRoute(<IssueDetailPage />, {
      path: "/projects/:id/issues/:iid",
      initialEntry: "/projects/proj-1/issues/issue-1#timeline",
    });

    expect(screen.getByText("GH-42 Crash on launch")).toBeInTheDocument();
    expect(screen.getByText("负责人")).toBeInTheDocument();
    expect(screen.getAllByText("进度 50%").length).toBeGreaterThan(0);
    expect(screen.getByText("1/2 项完成")).toBeInTheDocument();
    expect(screen.getByText("上一条")).toBeInTheDocument();
    expect(screen.getByText("下一条")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "返回" })).toBeInTheDocument();
    expect(screen.getByText("关闭了问题")).toBeInTheDocument();
    expect(screen.getByText(hasExactTextContent("Looks good"))).toBeInTheDocument();
    expect(screen.getByText(hasExactTextContent("Open the app"))).toBeInTheDocument();
    const latestTimelineContent = screen.getByText(hasExactTextContent("Looks good"));
    const commentComposerHeading = screen.getByText("添加评论");
    expect(
      latestTimelineContent.compareDocumentPosition(commentComposerHeading) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();

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

  it("writes GitHub comments from the issue detail page", async () => {
    mockIssueDetailData();
    const user = userEvent.setup();
    const mutateAsync = vi.fn().mockResolvedValue({
      data: {
        id: "comment-2",
      },
    });
    vi.mocked(useCreateIssueComment).mockReturnValue({
      mutateAsync,
      isPending: false,
    } as unknown as ReturnType<typeof useCreateIssueComment>);

    renderWithRoute(<IssueDetailPage />, {
      path: "/projects/:id/issues/:iid",
      initialEntry: "/projects/proj-1/issues/issue-1",
    });

    await user.type(screen.getByPlaceholderText("使用 Markdown 输入评论内容"), "需要补充日志");
    await user.click(screen.getByRole("button", { name: "发布评论" }));

    await waitFor(() => expect(mutateAsync).toHaveBeenCalledWith({ body: "需要补充日志" }));
    expect(toast.success).toHaveBeenCalledWith("评论已发布");
  });

  it("moves internal issue actions into the more menu", async () => {
    const internalIssue = buildInternalIssue();
    vi.mocked(useIssues).mockReturnValue({
      data: {
        items: [internalIssue],
        total: 1,
        page: 1,
        page_size: 20,
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useIssues>);
    vi.mocked(useIssue).mockReturnValue({
      data: internalIssue,
      isLoading: false,
    } as unknown as ReturnType<typeof useIssue>);

    renderWithRoute(<IssueDetailPage />, {
      path: "/projects/:id/issues/:iid",
      initialEntry: "/projects/proj-1/issues/internal-issue-1",
    });

    expect(screen.queryByRole("button", { name: "编辑问题" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "关闭问题" })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "更多" }));
    expect(screen.getByRole("menuitem", { name: "编辑问题" })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "关闭问题" })).toBeInTheDocument();
  });

  it("closes a GitHub issue from the more menu", async () => {
    mockIssueDetailData();
    const mutateAsync = vi.fn().mockResolvedValue({
      data: {
        ...buildGitHubIssue({ state: "closed" }),
      },
    });
    vi.mocked(useUpdateIssue).mockReturnValue({
      mutateAsync,
      isPending: false,
    } as unknown as ReturnType<typeof useUpdateIssue>);

    renderWithRoute(<IssueDetailPage />, {
      path: "/projects/:id/issues/:iid",
      initialEntry: "/projects/proj-1/issues/issue-1",
    });

    fireEvent.click(screen.getByRole("button", { name: "更多" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "关闭问题" }));

    await waitFor(() => expect(mutateAsync).toHaveBeenCalledWith({ state: "closed" }));
    expect(toast.success).toHaveBeenCalledWith("问题已关闭");
  });

  it("updates GitHub labels from the issue detail page", async () => {
    mockIssueDetailData();
    const user = userEvent.setup();
    const mutateAsync = vi.fn().mockResolvedValue({
      data: buildGitHubIssue(),
    });
    vi.mocked(useIssueRepoLabels).mockReturnValue({
      data: [
        { name: "bug", color: "d73a4a", description: "" },
        { name: "ios", color: "0969da", description: "" },
      ],
      isLoading: false,
    } as unknown as ReturnType<typeof useIssueRepoLabels>);
    vi.mocked(useUpdateIssue).mockReturnValue({
      mutateAsync,
      isPending: false,
    } as unknown as ReturnType<typeof useUpdateIssue>);

    renderWithRoute(<IssueDetailPage />, {
      path: "/projects/:id/issues/:iid",
      initialEntry: "/projects/proj-1/issues/issue-1",
    });

    await user.click(screen.getByRole("button", { name: "bug" }));
    await user.click(screen.getByRole("menuitemcheckbox", { name: "ios" }));

    await waitFor(() =>
      expect(mutateAsync).toHaveBeenCalledWith({ labels: ["bug", "ios"] }),
    );
    expect(toast.success).toHaveBeenCalledWith("标签已更新");
  });

  it("queues rapid GitHub label changes against the latest draft", async () => {
    mockIssueDetailData();
    const user = userEvent.setup();
    const firstUpdate = createDeferred<{ data: Issue }>();
    const mutateAsync = vi
      .fn()
      .mockImplementationOnce(() => firstUpdate.promise)
      .mockResolvedValue({ data: buildGitHubIssue() });

    vi.mocked(useIssueRepoLabels).mockReturnValue({
      data: [
        { name: "bug", color: "d73a4a", description: "" },
        { name: "ios", color: "0969da", description: "" },
        { name: "android", color: "2ea44f", description: "" },
      ],
      isLoading: false,
    } as unknown as ReturnType<typeof useIssueRepoLabels>);
    vi.mocked(useUpdateIssue).mockReturnValue({
      mutateAsync,
      isPending: false,
    } as unknown as ReturnType<typeof useUpdateIssue>);

    renderWithRoute(<IssueDetailPage />, {
      path: "/projects/:id/issues/:iid",
      initialEntry: "/projects/proj-1/issues/issue-1",
    });

    await user.click(screen.getByRole("button", { name: "bug" }));
    await user.click(screen.getByRole("menuitemcheckbox", { name: "ios" }));

    await waitFor(() =>
      expect(mutateAsync).toHaveBeenCalledWith({ labels: ["bug", "ios"] }),
    );
    expect(mutateAsync).toHaveBeenCalledTimes(1);

    if (!screen.queryByRole("menuitemcheckbox", { name: "android" })) {
      await user.click(await screen.findByRole("button", { name: "bug, ios" }));
    }
    await user.click(screen.getByRole("menuitemcheckbox", { name: "android" }));

    expect(mutateAsync).toHaveBeenCalledTimes(1);

    firstUpdate.resolve({
      data: buildGitHubIssue(
        {},
        {
          labels: [
            { name: "bug", color: "d73a4a", description: "" },
            { name: "ios", color: "0969da", description: "" },
          ],
        },
      ),
    });

    await waitFor(() =>
      expect(mutateAsync).toHaveBeenLastCalledWith({
        labels: ["bug", "ios", "android"],
      }),
    );
    expect(mutateAsync).toHaveBeenCalledTimes(2);
    expect(toast.success).toHaveBeenCalledWith("标签已更新");
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

  it("saves checklist from the issue detail page", async () => {
    mockIssueDetailData();
    const user = userEvent.setup();
    const mutateAsync = vi.fn().mockResolvedValue({
      data: {
        workflow_status: "in_progress",
        progress_percent: 100,
        checklist_total: 2,
        checklist_done: 2,
        updated_at: "2026-04-12T10:00:00Z",
      },
    });
    vi.mocked(useReplaceIssueChecklist).mockReturnValue({
      mutateAsync,
      isPending: false,
    } as unknown as ReturnType<typeof useReplaceIssueChecklist>);

    renderWithRoute(<IssueDetailPage />, {
      path: "/projects/:id/issues/:iid",
      initialEntry: "/projects/proj-1/issues/issue-1",
    });

    await user.click(screen.getByRole("button", { name: "编辑" }));
    const titleInput = screen.getByDisplayValue("修复崩溃");
    await user.clear(titleInput);
    await user.type(titleInput, "修复崩溃并回归");
    expect(screen.queryByRole("button", { name: "标记完成任务清单 2" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() =>
      expect(mutateAsync).toHaveBeenCalledWith({
        items: [
          { id: "chk-1", title: "复现问题", is_completed: true },
          { id: "chk-2", title: "修复崩溃并回归", is_completed: false },
        ],
      }),
    );
    expect(toast.success).toHaveBeenCalledWith("任务清单已保存");
  });

  it("toggles checklist completion in browse mode", async () => {
    mockIssueDetailData();
    const user = userEvent.setup();
    const mutateAsync = vi.fn().mockResolvedValue({
      data: {
        workflow_status: "done",
        progress_percent: 100,
        checklist_total: 2,
        checklist_done: 2,
        updated_at: "2026-04-12T10:00:00Z",
      },
    });
    vi.mocked(useReplaceIssueChecklist).mockReturnValue({
      mutateAsync,
      isPending: false,
    } as unknown as ReturnType<typeof useReplaceIssueChecklist>);

    renderWithRoute(<IssueDetailPage />, {
      path: "/projects/:id/issues/:iid",
      initialEntry: "/projects/proj-1/issues/issue-1",
    });

    expect(screen.queryByDisplayValue("修复崩溃")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "标记完成任务清单 2" }));

    await waitFor(() =>
      expect(mutateAsync).toHaveBeenCalledWith({
        items: [
          { id: "chk-1", title: "复现问题", is_completed: true },
          { id: "chk-2", title: "修复崩溃", is_completed: true },
        ],
      }),
    );
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
        "/issues?project=proj-1&state=open&q=crash&label=bug&source=github&sort=updated_desc&page=2",
    });

    await waitFor(() =>
      expect(screen.getByRole("link", { name: /GH-42 Crash on launch/i })).toHaveAttribute(
        "href",
        "/projects/proj-1/issues/issue-1?issue_state=open&issue_q=crash&issue_label=bug&issue_source=github&issue_sort=updated_desc&issue_page=2",
      ),
    );
  });

  it("renders the selected workflow status label in the issues filter", async () => {
    vi.mocked(useIssues).mockReturnValue({
      data: {
        items: [
          buildGitHubIssue({
            id: "issue-1",
            body: "App crashes",
          }),
        ],
        total: 1,
        page: 1,
        page_size: 20,
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useIssues>);

    renderWithRoute(<IssuesPage />, {
      path: "/issues",
      initialEntry: "/issues?project=proj-1&workflow_status=in_progress",
    });

    expect(screen.getByText("开发中")).toBeInTheDocument();
  });

  it("preserves unsaved checklist edits when the issue refetches", async () => {
    mockIssueDetailData();
    const user = userEvent.setup();
    const issueState = {
      current: buildGitHubIssue(
        {
          id: "issue-1",
          title: "Crash on launch",
          reference: "GH-42",
          body: "## Steps\n\nOpen the app",
          body_html: "<h2>Steps</h2><p>Open the <strong>app</strong></p>",
          internal_meta: {
            workflow_status: "in_progress",
            progress_percent: 50,
            checklist_total: 2,
            checklist_done: 1,
            started_at: "2026-04-12T09:00:00Z",
            checklist_updated_at: "2026-04-12T09:00:00Z",
            updated_at: "2026-04-12T09:00:00Z",
            checklist: [
              { id: "chk-1", title: "复现问题", is_completed: true, sort_order: 0 },
              { id: "chk-2", title: "修复崩溃", is_completed: false, sort_order: 1 },
            ],
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
    };
    vi.mocked(useIssue).mockImplementation(
      () =>
        ({
          data: issueState.current,
          isLoading: false,
        }) as unknown as ReturnType<typeof useIssue>,
    );

    const view = renderWithRoute(<IssueDetailPage />, {
      path: "/projects/:id/issues/:iid",
      initialEntry: "/projects/proj-1/issues/issue-1",
    });

    await user.click(screen.getByRole("button", { name: "编辑" }));
    const titleInput = screen.getByDisplayValue("修复崩溃");
    await user.clear(titleInput);
    await user.type(titleInput, "修复崩溃并补充测试");

    issueState.current = buildGitHubIssue(
      {
        id: "issue-1",
        title: "Crash on launch",
        reference: "GH-42",
        body: "## Steps\n\nOpen the app",
        body_html: "<h2>Steps</h2><p>Open the <strong>app</strong></p>",
        updated_at: "2026-04-12T10:30:00Z",
        internal_meta: {
          workflow_status: "in_progress",
          progress_percent: 50,
          checklist_total: 2,
          checklist_done: 1,
          started_at: "2026-04-12T09:00:00Z",
          checklist_updated_at: "2026-04-12T10:30:00Z",
          updated_at: "2026-04-12T10:30:00Z",
          checklist: [
            { id: "chk-1", title: "复现问题", is_completed: true, sort_order: 0 },
            { id: "chk-2", title: "修复崩溃", is_completed: false, sort_order: 1 },
          ],
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
    );

    view.rerender(
      <MemoryRouter initialEntries={["/projects/proj-1/issues/issue-1"]}>
        <Routes>
          <Route path="/projects/:id/issues/:iid" element={<IssueDetailPage />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(screen.getByDisplayValue("修复崩溃并补充测试")).toBeInTheDocument();
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

  it("shows a notice when a github issue body references local issue assets", async () => {
    mockIssueDetailData();
    vi.mocked(useIssue).mockReturnValue({
      data: buildGitHubIssue({
        body: "问题描述\n\n![clip](/api/issues/assets/asset-1/content)",
        body_html: '<p><img alt="Rendered image" src="https://github.com/acme/alpha/api/issues/assets/asset-1/content" /></p>',
      }),
      isLoading: false,
    } as unknown as ReturnType<typeof useIssue>);

    renderWithRoute(<IssueDetailPage />, {
      path: "/projects/:id/issues/:iid",
      initialEntry: "/projects/proj-1/issues/issue-1",
    });

    expect(
      screen.getByText(GITHUB_LOCAL_ASSET_NOTICE),
    ).toBeInTheDocument();
    expect(screen.getByRole("img", { name: "clip" })).toHaveAttribute(
      "src",
      "/api/issues/assets/asset-1/content?token=jwt-token",
    );
  });

  it("does not show the notice when only comments reference local issue assets", async () => {
    mockIssueDetailData();
    vi.mocked(useIssue).mockReturnValue({
      data: buildGitHubIssue({
        body: "普通正文",
        body_html: "<p>普通正文</p>",
      }),
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
                body: "![clip](/api/issues/assets/comment-asset-1/content)",
                body_html: "<p>comment html</p>",
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

    renderWithRoute(<IssueDetailPage />, {
      path: "/projects/:id/issues/:iid",
      initialEntry: "/projects/proj-1/issues/issue-1",
    });

    expect(
      screen.queryByText(GITHUB_LOCAL_ASSET_NOTICE),
    ).not.toBeInTheDocument();
  });

  it("appends selected ai suggestions into checklist", async () => {
    const user = userEvent.setup();
    mockIssueDetailData();

    const replaceChecklistMutateAsync = vi.fn().mockResolvedValue({
      data: {
        workflow_status: "in_progress",
        progress_percent: 25,
        checklist_total: 4,
        checklist_done: 1,
        checklist: [],
      },
    });

    vi.mocked(useReplaceIssueChecklist).mockReturnValue({
      mutateAsync: replaceChecklistMutateAsync,
      isPending: false,
    } as unknown as ReturnType<typeof useReplaceIssueChecklist>);

    renderWithRoute(<IssueDetailPage />, {
      path: "/projects/:id/issues/:iid",
      initialEntry: "/projects/proj-1/issues/issue-1",
    });

    await user.click(screen.getByRole("button", { name: "智能识别任务清单建议" }));

    await waitFor(() => {
      expect(screen.getByText("清单建议")).toBeInTheDocument();
      expect(screen.getByText("补充复现路径")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "追加到任务清单" }));

    await waitFor(() =>
      expect(replaceChecklistMutateAsync).toHaveBeenCalledWith({
        items: [
          { id: "chk-1", title: "复现问题", is_completed: true },
          { id: "chk-2", title: "修复崩溃", is_completed: false },
          { id: undefined, title: "补充复现路径", is_completed: false },
          { id: undefined, title: "确认影响版本", is_completed: false },
        ],
      }),
    );
  });

  it("creates an internal issue from a dedicated page", async () => {
    const user = userEvent.setup();
    const navigate = vi.fn();
    const useNavigateSpy = vi.spyOn(ReactRouter, "useNavigate").mockReturnValue(navigate);
    const mutateAsync = vi.fn().mockResolvedValue({
      data: buildInternalIssue({ internal_meta: null }),
    });
    const uploadMutateAsync = vi.fn().mockResolvedValue({
      data: {
        id: "draft-asset-1",
        issue_id: "",
        file_name: "clip.png",
        mime_type: "image/png",
        file_size: 3,
        content_url: "/api/issues/assets/draft-asset-1/content",
        markdown: "![clip](/api/issues/assets/draft-asset-1/content)",
        created_at: "2026-04-12T10:00:00Z",
      },
    });
    vi.mocked(useCreateIssue).mockReturnValue({
      mutateAsync,
      isPending: false,
    } as unknown as ReturnType<typeof useCreateIssue>);
    vi.mocked(useUploadDraftIssueAsset).mockReturnValue({
      mutateAsync: uploadMutateAsync,
      isPending: false,
    } as unknown as ReturnType<typeof useUploadDraftIssueAsset>);

    renderWithRoute(<NewInternalIssuePage />, {
      path: "/projects/:id/issues/new",
      initialEntry:
        "/projects/proj-1/issues/new?project=proj-1&state=closed&q=ship&label=bug&source=github&workflow_status=done&sort=updated_asc&page=2",
    });

    const bodyInput = screen.getByLabelText("描述") as HTMLTextAreaElement;

    await user.type(screen.getByLabelText("标题"), "需要补充交付通知");
    await user.type(bodyInput, "## 验收\n\n完成后通知 QA");
    bodyInput.setSelectionRange(bodyInput.value.length, bodyInput.value.length);

    const imageFile = new File(["img"], "clip.png", { type: "image/png" });
    fireEvent.paste(bodyInput, {
      clipboardData: {
        items: [
          {
            type: imageFile.type,
            getAsFile: () => imageFile,
          },
        ],
      },
    });

    await waitFor(() => expect(uploadMutateAsync).toHaveBeenCalledTimes(1));
    const formData = uploadMutateAsync.mock.calls[0][0] as FormData;
    expect(formData.get("file")).toStrictEqual(imageFile);
    await waitFor(() =>
      expect(bodyInput).toHaveValue("## 验收\n\n完成后通知 QA![clip](/api/issues/assets/draft-asset-1/content)\n"),
    );
    await user.click(await screen.findByRole("button", { name: "创建问题" }));

    await waitFor(() =>
      expect(mutateAsync).toHaveBeenCalledWith({
        title: "需要补充交付通知",
        body: "## 验收\n\n完成后通知 QA![clip](/api/issues/assets/draft-asset-1/content)\n",
        workflow_status: "",
        source: "internal",
      }),
    );
    expect(toast.success).toHaveBeenCalledWith("内部问题已创建");
    expect(navigate).toHaveBeenCalledWith(
      {
        pathname: "/projects/proj-1/issues/internal-issue-1",
        search: "?issue_state=open&issue_source=internal&issue_sort=updated_desc",
      },
      { replace: true },
    );
    useNavigateSpy.mockRestore();
  });

  it("edits an internal issue from a dedicated page", async () => {
    const user = userEvent.setup();
    const navigate = vi.fn();
    const useNavigateSpy = vi.spyOn(ReactRouter, "useNavigate").mockReturnValue(navigate);
    const mutateAsync = vi.fn().mockResolvedValue({
      data: buildInternalIssue({
        title: "补充回归通知",
        body: "更新后的描述",
      }),
    });
    const uploadMutateAsync = vi.fn().mockResolvedValue({
      data: {
        id: "asset-1",
        issue_id: "internal-issue-1",
        file_name: "clip.png",
        mime_type: "image/png",
        file_size: 3,
        content_url: "/api/issues/assets/asset-1/content",
        markdown: "![clip](/api/issues/assets/asset-1/content)",
        created_at: "2026-04-12T10:00:00Z",
      },
    });
    vi.mocked(useIssue).mockReturnValue({
      data: buildInternalIssue(),
      isLoading: false,
    } as unknown as ReturnType<typeof useIssue>);
    vi.mocked(useUpdateIssue).mockReturnValue({
      mutateAsync,
      isPending: false,
    } as unknown as ReturnType<typeof useUpdateIssue>);
    vi.mocked(useUploadIssueAsset).mockReturnValue({
      mutateAsync: uploadMutateAsync,
      isPending: false,
    } as unknown as ReturnType<typeof useUploadIssueAsset>);

    renderWithRoute(<EditInternalIssuePage />, {
      path: "/projects/:id/issues/:iid/edit",
      initialEntry: "/projects/proj-1/issues/internal-issue-1/edit?issue_state=open",
    });

    const titleInput = screen.getByLabelText("标题");
    const bodyInput = screen.getByLabelText("描述") as HTMLTextAreaElement;

    await user.clear(titleInput);
    await user.type(titleInput, "补充回归通知");
    await user.clear(bodyInput);
    await user.type(bodyInput, "更新后的描述");
    bodyInput.setSelectionRange(bodyInput.value.length, bodyInput.value.length);

    const imageFile = new File(["img"], "clip.png", { type: "image/png" });
    fireEvent.paste(bodyInput, {
      clipboardData: {
        items: [
          {
            type: imageFile.type,
            getAsFile: () => imageFile,
          },
        ],
      },
    });

    await waitFor(() => expect(uploadMutateAsync).toHaveBeenCalledTimes(1));
    const formData = uploadMutateAsync.mock.calls[0][0] as FormData;
    expect(formData.get("file")).toStrictEqual(imageFile);

    await waitFor(() =>
      expect(bodyInput).toHaveValue("更新后的描述![clip](/api/issues/assets/asset-1/content)\n"),
    );
    await user.click(await screen.findByRole("button", { name: "保存修改" }));

    await waitFor(() =>
      expect(mutateAsync).toHaveBeenCalledWith({
        title: "补充回归通知",
        body: "更新后的描述![clip](/api/issues/assets/asset-1/content)\n",
      }),
    );
    expect(toast.success).toHaveBeenCalledWith("内部问题已更新");
    expect(navigate).toHaveBeenCalledWith(
      {
        pathname: "/projects/proj-1/issues/internal-issue-1",
        search: "?issue_state=open",
      },
      { replace: true },
    );
    useNavigateSpy.mockRestore();
  });

  it("does not rewrite search params while typing a comment draft", async () => {
    mockIssueDetailData();
    const user = userEvent.setup();
    const setSearchParams = vi.fn();
    const useSearchParamsSpy = vi.spyOn(ReactRouter, "useSearchParams").mockReturnValue([
      new URLSearchParams("issue_state=open"),
      setSearchParams,
    ]);

    renderWithRoute(<IssueDetailPage />, {
      path: "/projects/:id/issues/:iid",
      initialEntry: "/projects/proj-1/issues/issue-1",
    });

    await user.type(screen.getByPlaceholderText("使用 Markdown 输入评论内容"), "12 已修复");

    expect(setSearchParams).not.toHaveBeenCalled();
    useSearchParamsSpy.mockRestore();
  });

  it("shows ship hook section on issue detail page", () => {
    mockIssueDetailData();

    renderWithRoute(<IssueDetailPage />, {
      path: "/projects/:id/issues/:iid",
      initialEntry: "/projects/proj-1/issues/issue-1",
    });

    expect(screen.getByText("下次发货后")).toBeInTheDocument();
    expect(
      screen.getByText("该项目下一次成功发货时执行"),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "设置" })).toBeInTheDocument();

    const metadataTitle = screen.getByText("元数据");
    const shipHookTitle = screen.getByText("下次发货后");
    expect(metadataTitle.closest("[data-slot='card']")).not.toContainElement(
      shipHookTitle,
    );
    expect(
      metadataTitle.compareDocumentPosition(shipHookTitle) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    expect(
      shipHookTitle.compareDocumentPosition(screen.getAllByText("任务清单")[0]) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  it("shows pending ship hook actions and saves configuration", async () => {
    const user = userEvent.setup();
    const upsertMutateAsync = vi.fn().mockResolvedValue({
      data: {
        status: "pending",
        comment_enabled: true,
        comment_body: "已随 {version} 发出。",
        close_enabled: true,
        workflow_enabled: true,
        workflow_status: "done",
      },
    });

    mockIssueDetailData();
    vi.mocked(useIssue).mockReturnValue({
      data: buildGitHubIssue({
        id: "issue-1",
        title: "Crash on launch",
        reference: "GH-42",
        ship_hook: {
          status: "pending",
          comment_enabled: true,
          comment_body: "已随 {version} 发出。",
          close_enabled: true,
          workflow_enabled: true,
          workflow_status: "done",
        },
      }),
      isLoading: false,
    } as unknown as ReturnType<typeof useIssue>);
    vi.mocked(useUpsertIssueShipHook).mockReturnValue({
      mutateAsync: upsertMutateAsync,
      isPending: false,
    } as unknown as ReturnType<typeof useUpsertIssueShipHook>);

    renderWithRoute(<IssueDetailPage />, {
      path: "/projects/:id/issues/:iid",
      initialEntry: "/projects/proj-1/issues/issue-1",
    });

    expect(
      screen.getByText("发评论、关闭、内部状态=已完成"),
    ).toBeInTheDocument();
    expect(screen.getByText("评论预览")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "修改" }));
    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() =>
      expect(upsertMutateAsync).toHaveBeenCalledWith({
        comment_body: "已随 {version} 发出。",
        close: true,
        workflow_status: "done",
      }),
    );
    expect(toast.success).toHaveBeenCalledWith("已保存下次发货后动作");
  });

  it("cancels pending ship hook from issue detail page", async () => {
    const user = userEvent.setup();
    const deleteMutateAsync = vi.fn().mockResolvedValue({ data: null });

    mockIssueDetailData();
    vi.mocked(useIssue).mockReturnValue({
      data: buildGitHubIssue({
        id: "issue-1",
        title: "Crash on launch",
        reference: "GH-42",
        ship_hook: {
          status: "pending",
          comment_enabled: false,
          close_enabled: true,
          workflow_enabled: false,
          workflow_status: "",
        },
      }),
      isLoading: false,
    } as unknown as ReturnType<typeof useIssue>);
    vi.mocked(useDeleteIssueShipHook).mockReturnValue({
      mutateAsync: deleteMutateAsync,
      isPending: false,
    } as unknown as ReturnType<typeof useDeleteIssueShipHook>);

    renderWithRoute(<IssueDetailPage />, {
      path: "/projects/:id/issues/:iid",
      initialEntry: "/projects/proj-1/issues/issue-1",
    });

    await user.click(screen.getByRole("button", { name: "取消" }));

    await waitFor(() => expect(deleteMutateAsync).toHaveBeenCalled());
    expect(toast.success).toHaveBeenCalledWith("已取消");
  });

  it("shows pending and failed ship hook badges on issues list", async () => {
    vi.mocked(useIssues).mockReturnValue({
      data: {
        items: [
          buildGitHubIssue({
            id: "issue-pending",
            title: "Pending hook issue",
            reference: "GH-10",
            ship_hook: {
              status: "pending",
              comment_enabled: false,
              close_enabled: true,
              workflow_enabled: false,
              workflow_status: "",
            },
          }),
          buildGitHubIssue({
            id: "issue-failed",
            title: "Failed hook issue",
            reference: "GH-11",
            ship_hook: {
              status: "fired",
              comment_enabled: true,
              close_enabled: false,
              workflow_enabled: false,
              workflow_status: "",
              version_number: "1.0.0",
              results: {
                comment: { ok: false, error: "failed" },
              },
            },
          }),
        ],
        total: 2,
        page: 1,
        page_size: 20,
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useIssues>);

    renderWithRoute(<IssuesPage />, {
      path: "/issues",
      initialEntry: "/issues?project=proj-1",
    });

    await waitFor(() =>
      expect(screen.getByText("发货后")).toBeInTheDocument(),
    );
    expect(screen.getByText("钩子失败")).toBeInTheDocument();
  });
});
