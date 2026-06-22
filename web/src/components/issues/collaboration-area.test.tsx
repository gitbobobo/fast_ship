import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { CollaborationArea } from "./collaboration-area";

const collabState = vi.hoisted(() => ({
  area: undefined as IssueCollabArea | undefined,
  isLoading: false,
  mutateAsync: vi.fn().mockResolvedValue(undefined),
  isPending: false,
}));

vi.mock("@/lib/hooks/use-issue-collab", () => ({
  useIssueCollab: () => ({ data: collabState.area, isLoading: collabState.isLoading }),
  useDeleteCollabSection: () => ({
    mutateAsync: collabState.mutateAsync,
    isPending: collabState.isPending,
  }),
}));

const toastMock = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
}));

vi.mock("sonner", () => ({
  toast: toastMock,
}));

const EMPTY_AREA: IssueCollabArea = {
  suggestions: [],
  plan: null,
  review: null,
  summary: null,
};

const FULL_AREA: IssueCollabArea = {
  suggestions: [
    {
      id: "s1",
      issue_id: "issue-1",
      body: "建议新增顶部按钮",
      sort_order: 0,
      author: { kind: "agent", login: "代理" },
      created_at: "2026-06-19T10:00:00Z",
      updated_at: "2026-06-19T10:00:00Z",
    },
    {
      id: "s2",
      issue_id: "issue-1",
      body: "支持暗色模式",
      sort_order: 1,
      author: { kind: "agent", login: "代理" },
      created_at: "2026-06-19T10:00:00Z",
      updated_at: "2026-06-19T10:00:00Z",
    },
  ],
  plan: {
    issue_id: "issue-1",
    body: "分两步实施",
    author: { kind: "agent", login: "代理" },
    created_at: "2026-06-19T11:00:00Z",
    updated_at: "2026-06-19T11:00:00Z",
  },
  review: {
    issue_id: "issue-1",
    body: "审查通过",
    author: { kind: "agent", login: "代理" },
    created_at: "2026-06-19T12:00:00Z",
    updated_at: "2026-06-19T12:00:00Z",
  },
  summary: {
    issue_id: "issue-1",
    body: "已新增顶部按钮",
    commit_ids: ["abc1234"],
    author: { kind: "agent", login: "代理" },
    created_at: "2026-06-19T13:00:00Z",
    updated_at: "2026-06-19T13:00:00Z",
  },
};

function renderArea(project: Project | null = null) {
  return render(
    <MemoryRouter>
      <CollaborationArea issueId="issue-1" project={project} />
    </MemoryRouter>,
  );
}

describe("CollaborationArea", () => {
  beforeEach(() => {
    collabState.area = EMPTY_AREA;
    collabState.isLoading = false;
    collabState.mutateAsync.mockClear().mockResolvedValue(undefined);
    collabState.isPending = false;
    toastMock.success.mockClear();
    toastMock.error.mockClear();
  });

  it("空状态只渲染标题，不渲染任何区块", () => {
    renderArea();
    expect(screen.getByText("人机协作区")).toBeInTheDocument();
    expect(screen.queryByRole("tablist")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("清空协作区")).not.toBeInTheDocument();
  });

  it("data 为 undefined 时仅渲染标题", () => {
    collabState.area = undefined;
    collabState.isLoading = false;
    renderArea();
    expect(screen.getByText("人机协作区")).toBeInTheDocument();
    expect(screen.queryByRole("tablist")).not.toBeInTheDocument();
  });

  it("加载态不渲染 Tab", () => {
    collabState.isLoading = true;
    renderArea();
    expect(screen.getByText("正在加载人机协作区")).toBeInTheDocument();
    expect(screen.queryByRole("tablist")).not.toBeInTheDocument();
  });

  it("四块均有内容时渲染四个 Tab，默认显示实施建议", () => {
    collabState.area = FULL_AREA;
    renderArea();

    expect(screen.getAllByRole("tab")).toHaveLength(4);
    expect(screen.getByRole("tab", { name: /实施建议，2 条/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.getByLabelText("清空协作区")).toBeInTheDocument();
    expect(screen.getByText("建议新增顶部按钮")).toBeInTheDocument();
  });

  it("仅有 suggestions 时默认选中实施建议 Tab", () => {
    collabState.area = {
      ...EMPTY_AREA,
      suggestions: FULL_AREA.suggestions,
    };
    renderArea();

    expect(screen.getByRole("tab", { name: /实施建议，2 条/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.getByLabelText("清空全部建议")).toBeInTheDocument();
  });

  it("点击 Tab 切换显示对应只读内容", async () => {
    const user = userEvent.setup();
    collabState.area = FULL_AREA;
    renderArea({ ...({} as Project), github_owner: "owner", github_repo: "repo" });

    await user.click(screen.getByRole("tab", { name: /计划，有内容/ }));
    expect(screen.getByRole("tab", { name: /计划，有内容/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.getByText("分两步实施")).toBeInTheDocument();
    expect(screen.getByLabelText("删除计划")).toBeInTheDocument();

    await user.click(screen.getByRole("tab", { name: /审查结果，有内容/ }));
    expect(screen.getByText("审查通过")).toBeInTheDocument();

    await user.click(screen.getByRole("tab", { name: /完成总结，有内容/ }));
    expect(screen.getByText("已新增顶部按钮")).toBeInTheDocument();
    const link = screen.getByText("abc1234").closest("a");
    expect(link).toHaveAttribute("href", "https://github.com/owner/repo/commit/abc1234");
  });

  it("project 缺 owner/repo 时 commit 显示为纯文本而非链接", async () => {
    const user = userEvent.setup();
    collabState.area = FULL_AREA;
    renderArea(null);

    await user.click(screen.getByRole("tab", { name: /完成总结，有内容/ }));
    expect(screen.getByText("abc1234")).toBeInTheDocument();
    expect(screen.getByText("abc1234").closest("a")).toBeNull();
  });

  it("仅有 plan 有内容时，切换到空 Tab 显示占位", async () => {
    const user = userEvent.setup();
    collabState.area = {
      ...EMPTY_AREA,
      plan: FULL_AREA.plan,
    };
    renderArea();

    expect(screen.getByRole("tab", { name: /计划，有内容/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.getByText("分两步实施")).toBeInTheDocument();
    await user.click(screen.getByRole("tab", { name: /审查结果，暂无内容/ }));
    expect(screen.getByText("暂无内容")).toBeInTheDocument();
  });

  it("仅有 review 有内容时默认选中审查结果 Tab", () => {
    collabState.area = {
      ...EMPTY_AREA,
      review: FULL_AREA.review,
    };
    renderArea();

    expect(screen.getByRole("tab", { name: /审查结果，有内容/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.getByText("审查通过")).toBeInTheDocument();
  });

  it("点击删除按钮弹出确认对话框并调用 mutation", async () => {
    const user = userEvent.setup();
    collabState.area = FULL_AREA;
    renderArea();

    await user.click(screen.getByLabelText("清空协作区"));
    expect(screen.getByText("清空协作区？")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "确认删除" }));

    await waitFor(() => {
      expect(collabState.mutateAsync).toHaveBeenCalledTimes(1);
      expect(toastMock.success).toHaveBeenCalledWith("协作区已清空");
    });
  });

  it("删除失败时显示错误 toast 且对话框保持打开", async () => {
    const user = userEvent.setup();
    collabState.mutateAsync.mockRejectedValueOnce(new Error("network"));
    collabState.area = FULL_AREA;
    renderArea();

    await user.click(screen.getByRole("tab", { name: /计划，有内容/ }));
    await user.click(screen.getByLabelText("删除计划"));
    await user.click(screen.getByRole("button", { name: "确认删除" }));

    await waitFor(() => {
      expect(toastMock.error).toHaveBeenCalledWith("删除失败，请稍后重试");
    });
    expect(screen.getByText("删除计划？")).toBeInTheDocument();
  });

  it("删除当前激活 tab 内容后切换到仍有内容的 tab", async () => {
    const user = userEvent.setup();
    collabState.area = {
      ...EMPTY_AREA,
      plan: FULL_AREA.plan,
      review: FULL_AREA.review,
    };
    const { rerender } = renderArea();

    await user.click(screen.getByRole("tab", { name: /计划，有内容/ }));
    expect(screen.getByRole("tab", { name: /计划，有内容/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );

    collabState.area = {
      ...EMPTY_AREA,
      review: FULL_AREA.review,
    };
    rerender(
      <MemoryRouter>
        <CollaborationArea issueId="issue-1" project={null} />
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.getByRole("tab", { name: /审查结果，有内容/ })).toHaveAttribute(
        "aria-selected",
        "true",
      );
    });
  });
});
