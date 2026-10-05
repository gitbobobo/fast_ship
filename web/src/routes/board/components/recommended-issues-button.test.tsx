import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { RecommendedIssuesButton } from "./recommended-issues-button";

const {
  useRecommendationsMock,
  useIssueMock,
  useIssueCollabMock,
  mutateAsyncMock,
  toastSuccessMock,
  copyWithToastMock,
  promptList,
} = vi.hoisted(() => ({
  useRecommendationsMock: vi.fn(),
  useIssueMock: vi.fn(),
  useIssueCollabMock: vi.fn(),
  mutateAsyncMock: vi.fn(),
  toastSuccessMock: vi.fn(),
  copyWithToastMock: vi.fn(),
  promptList: { current: [] as IssuePrompt[] },
}));

vi.mock("@/lib/copy", () => ({ copyWithToast: copyWithToastMock }));

vi.mock("@/lib/hooks/use-issue-prompt", () => ({
  useIssuePromptList: () => promptList.current,
}));

vi.mock("@/lib/hooks/use-recommendations", () => ({
  useRecommendations: (projectId?: string) =>
    useRecommendationsMock(projectId),
  useRemoveRecommendation: () => ({
    mutateAsync: mutateAsyncMock,
    isPending: false,
  }),
}));

vi.mock("@/lib/hooks/use-issues", () => ({
  useIssue: (issueId: string) => useIssueMock(issueId),
}));

vi.mock("@/lib/hooks/use-issue-collab", () => ({
  useIssueCollab: (issueId: string) => useIssueCollabMock(issueId),
  useDeleteCollabSection: () => ({ mutateAsync: vi.fn(), isPending: false }),
}));

vi.mock("sonner", () => ({
  toast: {
    success: toastSuccessMock,
    error: vi.fn(),
    info: vi.fn(),
    warning: vi.fn(),
  },
}));

const DEP: RecommendationDependency = {
  issue_id: "dep-1",
  title: "前置任务",
  state: "closed",
  workflow_status: "done",
  project_id: "project-1",
  sequence_number: 1,
  reference: "INT-0",
};

function makeItem(
  id: string,
  reference: string,
  title: string,
  priority: IssueRecommendation["priority"],
  extra: Partial<IssueRecommendation> = {},
): IssueRecommendation {
  return {
    issue: {
      id,
      project_id: "project-1",
      project_name: "musiver",
      source: "internal",
      sequence_number: 1,
      reference,
      title,
      state: "open",
      workflow_status: "todo",
    },
    reason: `推荐 ${reference}`,
    priority,
    created_by: "ci-bot",
    created_at: "2026-08-22T00:00:00Z",
    updated_at: "2026-08-22T00:00:00Z",
    dependencies: [],
    ...extra,
  };
}

function makeIssue(
  id: string,
  reference: string,
  title: string,
  extra: Partial<Issue> = {},
): Issue {
  return {
    id,
    project_id: "project-1",
    source: "internal",
    sequence_number: 1,
    reference,
    state: "open",
    state_reason: "",
    title,
    body: `${title}的正文`,
    body_html: "",
    author: { login: "alice", avatar_url: "" },
    unread_comments_count: 0,
    created_at: "2026-08-22T00:00:00Z",
    updated_at: "2026-08-22T00:00:00Z",
    internal_meta: {
      workflow_status: "todo",
      checklist_total: 0,
      checklist_done: 0,
      checklist: [],
    },
    github: null,
    ...extra,
  };
}

const HIGH_ITEM = makeItem("issue-1", "INT-1", "高优任务", "high", {
  reason: "先做这个能解锁后续工作",
  dependencies: [DEP],
});
const MED_ITEM = makeItem("issue-2", "INT-2", "中优任务", "medium", {
  reason: "中优理由",
});
const MED_ITEM_2 = makeItem("issue-3", "INT-3", "另一个中优", "medium");

const ISSUES: Record<string, Issue> = {
  "issue-1": makeIssue("issue-1", "INT-1", "高优任务", {
    internal_meta: {
      workflow_status: "todo",
      checklist_total: 2,
      checklist_done: 1,
      checklist: [
        { id: "chk-1", title: "调研方案", is_completed: true, sort_order: 0 },
        { id: "chk-2", title: "落地实现", is_completed: false, sort_order: 1 },
      ],
    },
  }),
  "issue-2": makeIssue("issue-2", "INT-2", "中优任务"),
  "issue-3": makeIssue("issue-3", "INT-3", "另一个中优"),
  "dep-1": makeIssue("dep-1", "INT-0", "前置任务", {
    state: "closed",
    internal_meta: {
      workflow_status: "done",
      checklist_total: 0,
      checklist_done: 0,
      checklist: [],
    },
  }),
};

const ALL_ITEMS = [HIGH_ITEM, MED_ITEM, MED_ITEM_2];

function mockRecommendations(items: IssueRecommendation[]) {
  useRecommendationsMock.mockReturnValue({
    data: { items },
    isLoading: false,
    isError: false,
  });
}

function renderButton() {
  return render(
    <MemoryRouter>
      <RecommendedIssuesButton projectId="project-1" />
    </MemoryRouter>,
  );
}

async function openDialog() {
  fireEvent.click(screen.getByRole("button", { name: /^推荐/ }));
  await screen.findByText("推荐任务");
}

function rerenderButton(rerender: (ui: React.ReactNode) => void) {
  rerender(
    <MemoryRouter>
      <RecommendedIssuesButton projectId="project-1" />
    </MemoryRouter>,
  );
}

describe("RecommendedIssuesButton", () => {
  beforeEach(() => {
    useRecommendationsMock.mockReset();
    useIssueMock.mockReset();
    useIssueMock.mockImplementation((issueId: string) => ({
      data: ISSUES[issueId],
      isLoading: false,
    }));
    useIssueCollabMock.mockReset();
    useIssueCollabMock.mockReturnValue({
      data: { consensus: null, summary: null },
      isLoading: false,
    });
    mutateAsyncMock.mockReset();
    mutateAsyncMock.mockResolvedValue(undefined);
    copyWithToastMock.mockReset();
    copyWithToastMock.mockResolvedValue(true);
    toastSuccessMock.mockReset();
    promptList.current = [
      {
        id: "default",
        name: "默认",
        content: "请处理此问题",
        supports_batch: true,
      },
    ];
    window.HTMLElement.prototype.scrollIntoView = vi.fn();
  });

  it("renders nothing when there are no recommendations", () => {
    mockRecommendations([]);
    const { container } = renderButton();
    expect(container).toBeEmptyDOMElement();
  });

  it("renders nothing when the list fails to load", () => {
    useRecommendationsMock.mockReturnValue({
      data: undefined,
      isLoading: false,
      isError: true,
    });
    const { container } = renderButton();
    expect(container).toBeEmptyDOMElement();
  });

  it("opens a two-column dialog with the first item selected", async () => {
    mockRecommendations(ALL_ITEMS);
    renderButton();

    await openDialog();

    // 左栏：分组标题 + 单行列表
    expect(
      screen.getByRole("region", { name: "高优先级" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("region", { name: "中优先级" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /INT-2.*中优任务/ }),
    ).toBeInTheDocument();

    // 默认选中第一条（最高优先级），右栏为只读详情
    expect(
      screen.getByRole("button", { name: /INT-1.*高优任务/ }),
    ).toHaveAttribute("aria-current", "true");
    expect(
      await screen.findByRole("heading", { name: "高优任务" }),
    ).toBeInTheDocument();
    expect(screen.getByText("先做这个能解锁后续工作")).toBeInTheDocument();
    expect(screen.getByText("高优任务的正文")).toBeInTheDocument();
    expect(screen.getByText("1/2 项完成")).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: /打开完整详情页/ }),
    ).toHaveAttribute("href", "/projects/project-1/issues/issue-1");
  });

  it("selects a recommendation on click", async () => {
    mockRecommendations(ALL_ITEMS);
    renderButton();
    await openDialog();

    fireEvent.click(
      screen.getByRole("button", { name: /INT-2.*中优任务/ }),
    );

    expect(
      screen.getByRole("button", { name: /INT-2.*中优任务/ }),
    ).toHaveAttribute("aria-current", "true");
    expect(
      await screen.findByRole("heading", { name: "中优任务" }),
    ).toBeInTheDocument();
    expect(screen.getByText("中优理由")).toBeInTheDocument();
  });

  it("moves selection across priority groups with arrow and j/k keys", async () => {
    mockRecommendations(ALL_ITEMS);
    renderButton();
    await openDialog();
    await screen.findByRole("heading", { name: "高优任务" });

    // 跨分组向下
    fireEvent.keyDown(window, { key: "ArrowDown" });
    expect(
      await screen.findByRole("heading", { name: "中优任务" }),
    ).toBeInTheDocument();

    fireEvent.keyDown(window, { key: "j" });
    expect(
      await screen.findByRole("heading", { name: "另一个中优" }),
    ).toBeInTheDocument();

    // 到尾部后不再前进
    fireEvent.keyDown(window, { key: "ArrowDown" });
    expect(
      screen.getByRole("heading", { name: "另一个中优" }),
    ).toBeInTheDocument();

    fireEvent.keyDown(window, { key: "ArrowUp" });
    expect(
      await screen.findByRole("heading", { name: "中优任务" }),
    ).toBeInTheDocument();

    fireEvent.keyDown(window, { key: "k" });
    fireEvent.keyDown(window, { key: "ArrowUp" });
    expect(
      await screen.findByRole("heading", { name: "高优任务" }),
    ).toBeInTheDocument();

    // 到头部后不再后退
    fireEvent.keyDown(window, { key: "ArrowUp" });
    expect(
      screen.getByRole("heading", { name: "高优任务" }),
    ).toBeInTheDocument();
  });

  it("copies the prompt with the recommendation reason on Enter", async () => {
    mockRecommendations(ALL_ITEMS);
    renderButton();
    await openDialog();
    await screen.findByRole("heading", { name: "高优任务" });

    fireEvent.keyDown(window, { key: "Enter" });

    await waitFor(() =>
      expect(copyWithToastMock).toHaveBeenCalledWith(
        `/fast-ship 请处理此问题
---
项目ID：project-1
问题ID：issue-1
推荐理由：先做这个能解锁后续工作`,
        "已复制提示词",
      ),
    );
  });

  it("copies the prompt on Enter even when a list row holds focus", async () => {
    // 回归：Dialog 打开时 base-ui 自动聚焦首行；行被排除在 Enter 让位名单外，
    // 焦点落在行上按 Enter 仍是复制，不会变成激活该行
    mockRecommendations(ALL_ITEMS);
    renderButton();
    await openDialog();
    const firstRow = await screen.findByRole("button", { name: /INT-1.*高优任务/ });
    firstRow.focus();

    fireEvent.keyDown(firstRow, { key: "Enter" });

    await waitFor(() =>
      expect(copyWithToastMock).toHaveBeenCalledWith(
        expect.stringContaining("推荐理由：先做这个能解锁后续工作"),
        "已复制提示词",
      ),
    );
    // 焦点行未被 Enter 激活成「重选」，选中项维持
    expect(firstRow).toHaveAttribute("aria-current", "true");
  });

  it("lets Enter activate the dependency chip instead of copying", async () => {
    mockRecommendations(ALL_ITEMS);
    renderButton();
    await openDialog();
    await screen.findByRole("heading", { name: "高优任务" });
    const depChip = screen.getByRole("button", { name: /INT-0.*前置任务/ });
    depChip.focus();

    fireEvent.keyDown(depChip, { key: "Enter" });

    // Enter 让位原生激活，不触发复制（happy-dom 不会合成 click，验证的是不复制）
    await waitFor(() => expect(copyWithToastMock).not.toHaveBeenCalled());
  });

  it("ignores navigation keys combined with modifier keys", async () => {
    mockRecommendations(ALL_ITEMS);
    renderButton();
    await openDialog();
    await screen.findByRole("heading", { name: "高优任务" });

    fireEvent.keyDown(window, { key: "j", metaKey: true });
    fireEvent.keyDown(window, { key: "ArrowDown", ctrlKey: true });

    // 选中项仍是第一条
    expect(
      screen.getByRole("button", { name: /INT-1.*高优任务/ }),
    ).toHaveAttribute("aria-current", "true");
  });

  it("opens the prompt picker on Enter when multiple prompts exist", async () => {
    promptList.current = [
      { id: "a", name: "默认", content: "请处理此问题", supports_batch: true },
      { id: "b", name: "补充测试", content: "请补充测试", supports_batch: false },
    ];
    mockRecommendations(ALL_ITEMS);
    renderButton();
    await openDialog();
    await screen.findByRole("heading", { name: "高优任务" });

    fireEvent.keyDown(window, { key: "Enter" });

    const menuItem = await screen.findByRole("menuitem", {
      name: "补充测试",
    });
    fireEvent.click(menuItem);

    await waitFor(() =>
      expect(copyWithToastMock).toHaveBeenCalledWith(
        `/fast-ship 请补充测试
---
项目ID：project-1
问题ID：issue-1
推荐理由：先做这个能解锁后续工作`,
        "已复制提示词",
      ),
    );
  });

  it("peeks a dependency from its chip and returns via the back link", async () => {
    mockRecommendations(ALL_ITEMS);
    renderButton();
    await openDialog();
    await screen.findByRole("heading", { name: "高优任务" });

    // 前置依赖 chip → 右栏切到该依赖（推荐列表之外的 issue）
    fireEvent.click(screen.getByRole("button", { name: /INT-0.*前置任务/ }));

    expect(
      await screen.findByRole("button", { name: /返回 INT-1/ }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("heading", { name: "前置任务" }),
    ).toBeInTheDocument();
    // peek 视图隐藏推荐理由与移除入口
    expect(screen.queryByText("推荐理由")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /移除推荐/ }),
    ).not.toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: /打开完整详情页/ }),
    ).toHaveAttribute("href", "/projects/project-1/issues/dep-1");

    // 回链回到选中条目
    fireEvent.click(screen.getByRole("button", { name: /返回 INT-1/ }));
    expect(
      await screen.findByRole("heading", { name: "高优任务" }),
    ).toBeInTheDocument();
    expect(screen.getByText("先做这个能解锁后续工作")).toBeInTheDocument();
  });

  it("removes a recommendation after confirming", async () => {
    mockRecommendations(ALL_ITEMS);
    renderButton();
    await openDialog();
    await screen.findByRole("heading", { name: "高优任务" });

    fireEvent.click(screen.getByRole("button", { name: /移除推荐/ }));
    fireEvent.click(
      await screen.findByRole("button", { name: "确认移除" }),
    );

    await waitFor(() =>
      expect(mutateAsyncMock).toHaveBeenCalledWith("issue-1"),
    );
    await waitFor(() => expect(toastSuccessMock).toHaveBeenCalled());
  });

  it("falls through to the next item when the selected one disappears", async () => {
    mockRecommendations(ALL_ITEMS);
    const { rerender } = renderButton();
    await openDialog();
    await screen.findByRole("heading", { name: "高优任务" });

    mockRecommendations([MED_ITEM, MED_ITEM_2]);
    rerenderButton(rerender);

    expect(
      await screen.findByRole("heading", { name: "中优任务" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /INT-2.*中优任务/ }),
    ).toHaveAttribute("aria-current", "true");
  });

  it("falls back to the previous item when the selected last one disappears", async () => {
    mockRecommendations(ALL_ITEMS);
    const { rerender } = renderButton();
    await openDialog();

    fireEvent.keyDown(window, { key: "ArrowDown" });
    fireEvent.keyDown(window, { key: "ArrowDown" });
    await screen.findByRole("heading", { name: "另一个中优" });

    mockRecommendations([HIGH_ITEM, MED_ITEM]);
    rerenderButton(rerender);

    expect(
      await screen.findByRole("heading", { name: "中优任务" }),
    ).toBeInTheDocument();
  });

  it("closes the dialog when the list becomes empty", async () => {
    mockRecommendations(ALL_ITEMS);
    const { rerender } = renderButton();
    await openDialog();

    mockRecommendations([]);
    rerenderButton(rerender);

    await waitFor(() =>
      expect(screen.queryByText("推荐任务")).not.toBeInTheDocument(),
    );
    expect(
      screen.queryByRole("button", { name: /^推荐/ }),
    ).not.toBeInTheDocument();
  });

  it("renders the collab area read-only inside the dialog", async () => {
    useIssueCollabMock.mockReturnValue({
      data: {
        consensus: {
          issue_id: "issue-1",
          body: "双方同意优先处理登录页",
          author: { kind: "agent", login: "代理" },
          created_at: "2026-08-20T00:00:00Z",
          updated_at: "2026-08-21T00:00:00Z",
        },
        summary: null,
      },
      isLoading: false,
    });
    mockRecommendations(ALL_ITEMS);
    renderButton();
    await openDialog();

    expect(await screen.findByText("人机协作区")).toBeInTheDocument();
    expect(
      screen.getByText("双方同意优先处理登录页"),
    ).toBeInTheDocument();
    // 弹框内不允许出现删除入口（内部含 AlertDialog）
    expect(screen.queryByLabelText("清空协作区")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("删除共识")).not.toBeInTheDocument();
  });
});
