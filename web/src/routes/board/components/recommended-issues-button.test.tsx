import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { RecommendedIssuesButton } from "./recommended-issues-button";

const {
  useRecommendationsMock,
  useIssueMock,
  useIssueCollabMock,
  deferMutateAsyncMock,
  restoreMutateAsyncMock,
  removeMutateAsyncMock,
  toastSuccessMock,
  toastErrorMock,
  copyWithToastMock,
  promptList,
} = vi.hoisted(() => ({
  useRecommendationsMock: vi.fn(),
  useIssueMock: vi.fn(),
  useIssueCollabMock: vi.fn(),
  deferMutateAsyncMock: vi.fn(),
  restoreMutateAsyncMock: vi.fn(),
  removeMutateAsyncMock: vi.fn(),
  toastSuccessMock: vi.fn(),
  toastErrorMock: vi.fn(),
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
  useDeferRecommendation: () => ({
    mutateAsync: deferMutateAsyncMock,
    isPending: false,
  }),
  useRestoreRecommendation: () => ({
    mutateAsync: restoreMutateAsyncMock,
    isPending: false,
  }),
  useRemoveRecommendation: () => ({
    mutateAsync: removeMutateAsyncMock,
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
    error: toastErrorMock,
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
    status: "active",
    deferred_at: null,
    defer_note: null,
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
const DEFERRED_ITEM = makeItem("issue-4", "INT-4", "刚延后的任务", "medium", {
  status: "deferred",
  deferred_at: "2026-08-25T00:00:00Z",
  defer_note: "等依赖就绪",
});
const DEFERRED_ITEM_2 = makeItem("issue-5", "INT-5", "更早延后的任务", "medium", {
  status: "deferred",
  deferred_at: "2026-08-20T00:00:00Z",
});

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
  "issue-4": makeIssue("issue-4", "INT-4", "刚延后的任务"),
  "issue-5": makeIssue("issue-5", "INT-5", "更早延后的任务"),
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
    deferMutateAsyncMock.mockReset();
    deferMutateAsyncMock.mockResolvedValue(undefined);
    restoreMutateAsyncMock.mockReset();
    restoreMutateAsyncMock.mockResolvedValue(undefined);
    removeMutateAsyncMock.mockReset();
    removeMutateAsyncMock.mockResolvedValue(undefined);
    copyWithToastMock.mockReset();
    copyWithToastMock.mockResolvedValue(true);
    toastSuccessMock.mockReset();
    toastErrorMock.mockReset();
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

    // 双 tab 各带计数，默认落在推荐中
    expect(
      screen.getByRole("tab", { name: /推荐中 3/ }),
    ).toHaveAttribute("aria-selected", "true");
    expect(
      screen.getByRole("tab", { name: /已延后 0/ }),
    ).toBeInTheDocument();

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
    // peek 视图隐藏推荐理由与操作入口
    expect(screen.queryByText("推荐理由")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /延后处理/ }),
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

  it("defers a recommendation with an optional note", async () => {
    mockRecommendations(ALL_ITEMS);
    renderButton();
    await openDialog();
    await screen.findByRole("heading", { name: "高优任务" });

    fireEvent.click(screen.getByRole("button", { name: /延后处理/ }));

    const noteInput = await screen.findByLabelText("延后备注");
    fireEvent.change(noteInput, { target: { value: "等依赖就绪" } });
    fireEvent.click(screen.getByRole("button", { name: "确认延后" }));

    await waitFor(() =>
      expect(deferMutateAsyncMock).toHaveBeenCalledWith({
        issueId: "issue-1",
        note: "等依赖就绪",
      }),
    );
    await waitFor(() =>
      expect(toastSuccessMock).toHaveBeenCalledWith("已延后"),
    );
  });

  it("ignores navigation and Enter keys while the defer dialog is open", async () => {
    mockRecommendations(ALL_ITEMS);
    renderButton();
    await openDialog();
    await screen.findByRole("heading", { name: "高优任务" });

    fireEvent.click(screen.getByRole("button", { name: /延后处理/ }));
    const noteInput = await screen.findByLabelText("延后备注");

    // j/k、↑/↓ 不移动选中；Enter 不触发复制（底层弹框被 inert，按 DOM 属性断言）
    fireEvent.keyDown(noteInput, { key: "j" });
    fireEvent.keyDown(noteInput, { key: "k" });
    fireEvent.keyDown(noteInput, { key: "ArrowDown" });
    fireEvent.keyDown(noteInput, { key: "ArrowUp" });
    fireEvent.keyDown(noteInput, { key: "Enter" });
    fireEvent.keyDown(window, { key: "Enter" });

    expect(document.querySelector('[data-issue-id="issue-1"]')).toHaveAttribute(
      "aria-current",
      "true",
    );
    expect(copyWithToastMock).not.toHaveBeenCalled();
  });

  it("keeps the note in the defer dialog when deferring fails", async () => {
    deferMutateAsyncMock.mockRejectedValue(new Error("network"));
    mockRecommendations(ALL_ITEMS);
    renderButton();
    await openDialog();
    await screen.findByRole("heading", { name: "高优任务" });

    fireEvent.click(screen.getByRole("button", { name: /延后处理/ }));
    const noteInput = await screen.findByLabelText("延后备注");
    fireEvent.change(noteInput, { target: { value: "等依赖就绪" } });
    fireEvent.click(screen.getByRole("button", { name: "确认延后" }));

    await waitFor(() =>
      expect(toastErrorMock).toHaveBeenCalledWith("延后处理失败"),
    );
    // 对话框保持打开且已输入内容不丢，可重试
    expect(noteInput).toHaveValue("等依赖就绪");
  });

  it("rejects an over-limit defer note without closing the dialog", async () => {
    mockRecommendations(ALL_ITEMS);
    renderButton();
    await openDialog();
    await screen.findByRole("heading", { name: "高优任务" });

    fireEvent.click(screen.getByRole("button", { name: /延后处理/ }));
    const noteInput = await screen.findByLabelText("延后备注");
    // fireEvent.change 不受 maxLength 约束，模拟粘贴等越过属性限制的路径
    fireEvent.change(noteInput, { target: { value: "长".repeat(501) } });
    fireEvent.click(screen.getByRole("button", { name: "确认延后" }));

    await waitFor(() =>
      expect(toastErrorMock).toHaveBeenCalledWith("备注不能超过 500 个字符"),
    );
    expect(deferMutateAsyncMock).not.toHaveBeenCalled();
    expect(noteInput).toHaveValue("长".repeat(501));
  });

  it("defers a recommendation without a note", async () => {
    mockRecommendations(ALL_ITEMS);
    renderButton();
    await openDialog();
    await screen.findByRole("heading", { name: "高优任务" });

    fireEvent.click(screen.getByRole("button", { name: /延后处理/ }));
    fireEvent.click(
      await screen.findByRole("button", { name: "确认延后" }),
    );

    await waitFor(() =>
      expect(deferMutateAsyncMock).toHaveBeenCalledWith({
        issueId: "issue-1",
        note: undefined,
      }),
    );
    await waitFor(() =>
      expect(toastSuccessMock).toHaveBeenCalledWith("已延后"),
    );
  });

  it("stays on the active tab with an empty state after the last active item leaves", async () => {
    mockRecommendations([HIGH_ITEM, DEFERRED_ITEM]);
    const { rerender } = renderButton();
    await openDialog();
    await screen.findByRole("heading", { name: "高优任务" });

    // 延后后：条目从推荐中消失、进入已延后，弹框留在推荐中 tab
    mockRecommendations([
      {
        ...HIGH_ITEM,
        status: "deferred" as const,
        deferred_at: "2026-08-26T00:00:00Z",
      },
      DEFERRED_ITEM,
    ]);
    rerenderButton(rerender);

    expect(
      await screen.findByText("暂无推荐中的任务"),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("tab", { name: /推荐中 0/ }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("tab", { name: /已延后 2/ }),
    ).toBeInTheDocument();
  });

  it("lists deferred items on the deferred tab ordered by deferred_at desc within a group", async () => {
    mockRecommendations([...ALL_ITEMS, DEFERRED_ITEM_2, DEFERRED_ITEM]);
    renderButton();
    await openDialog();

    fireEvent.click(screen.getByRole("tab", { name: /已延后/ }));

    // 同组内 deferred_at 降序：INT-4（8-25）在 INT-5（8-20）之前，默认选中第一条
    const later = await screen.findByRole("button", {
      name: /INT-4.*刚延后的任务/,
    });
    const earlier = screen.getByRole("button", {
      name: /INT-5.*更早延后的任务/,
    });
    expect(later).toHaveAttribute("aria-current", "true");
    expect(
      later.compareDocumentPosition(earlier) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();

    // 右栏展示延后信息块与延后 tab 的操作栏
    expect(
      await screen.findByRole("heading", { name: "刚延后的任务" }),
    ).toBeInTheDocument();
    expect(screen.getByText("等依赖就绪")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /移回推荐/ }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /彻底移除/ }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /延后处理/ }),
    ).not.toBeInTheDocument();
  });

  it("restores a deferred recommendation back to the active tab", async () => {
    mockRecommendations([...ALL_ITEMS, DEFERRED_ITEM]);
    renderButton();
    await openDialog();

    fireEvent.click(screen.getByRole("tab", { name: /已延后/ }));
    await screen.findByRole("heading", { name: "刚延后的任务" });

    fireEvent.click(screen.getByRole("button", { name: /移回推荐/ }));

    await waitFor(() =>
      expect(restoreMutateAsyncMock).toHaveBeenCalledWith("issue-4"),
    );
    await waitFor(() =>
      expect(toastSuccessMock).toHaveBeenCalledWith("已移回推荐"),
    );
  });

  it("removes a deferred recommendation entirely after confirming", async () => {
    mockRecommendations([...ALL_ITEMS, DEFERRED_ITEM]);
    renderButton();
    await openDialog();

    fireEvent.click(screen.getByRole("tab", { name: /已延后/ }));
    await screen.findByRole("heading", { name: "刚延后的任务" });

    fireEvent.click(screen.getByRole("button", { name: /彻底移除/ }));
    fireEvent.click(
      await screen.findByRole("button", { name: "确认移除" }),
    );

    await waitFor(() =>
      expect(removeMutateAsyncMock).toHaveBeenCalledWith("issue-4"),
    );
    await waitFor(() =>
      expect(toastSuccessMock).toHaveBeenCalledWith("已彻底移除"),
    );
  });

  it("moves selection within the deferred tab with arrow keys", async () => {
    mockRecommendations([...ALL_ITEMS, DEFERRED_ITEM, DEFERRED_ITEM_2]);
    renderButton();
    await openDialog();

    fireEvent.click(screen.getByRole("tab", { name: /已延后/ }));
    await screen.findByRole("heading", { name: "刚延后的任务" });

    fireEvent.keyDown(window, { key: "ArrowDown" });
    expect(
      await screen.findByRole("heading", { name: "更早延后的任务" }),
    ).toBeInTheDocument();
  });

  it("shows the button without a badge when only deferred items exist", async () => {
    mockRecommendations([DEFERRED_ITEM]);
    renderButton();

    // 徽标只计推荐中条数：全为已延后时按钮可见但无数字
    const button = screen.getByRole("button", { name: "推荐" });
    expect(button).toBeInTheDocument();
    expect(button).toHaveTextContent(/^推荐$/);
  });

  it("opens on the deferred tab when no active items exist", async () => {
    mockRecommendations([DEFERRED_ITEM]);
    renderButton();

    await openDialog();

    expect(screen.getByRole("tab", { name: /已延后/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(
      await screen.findByRole("heading", { name: "刚延后的任务" }),
    ).toBeInTheDocument();
    expect(screen.getByText("等依赖就绪")).toBeInTheDocument();
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
