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
  consensus: null,
  summary: null,
};

const FULL_AREA: IssueCollabArea = {
  consensus: {
    issue_id: "issue-1",
    body: "双方同意优先处理登录页",
    author: { kind: "agent", login: "代理" },
    created_at: "2026-06-19T10:00:00Z",
    updated_at: "2026-06-19T10:30:00Z",
  },
  summary: {
    issue_id: "issue-1",
    body: "已新增顶部按钮",
    author: { kind: "agent", login: "代理" },
    created_at: "2026-06-19T13:00:00Z",
    updated_at: "2026-06-19T13:00:00Z",
  },
};

const CONSENSUS_ONLY_AREA: IssueCollabArea = {
  consensus: FULL_AREA.consensus,
  summary: null,
};

const SUMMARY_ONLY_AREA: IssueCollabArea = {
  consensus: null,
  summary: FULL_AREA.summary,
};

function renderArea() {
  return render(
    <MemoryRouter>
      <CollaborationArea issueId="issue-1" />
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

  it("全空只渲染标题，不渲染 Tab 与清空按钮", () => {
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

  it("仅共识有内容时渲染两个 Tab，默认选中共识", () => {
    collabState.area = CONSENSUS_ONLY_AREA;
    renderArea();

    expect(screen.getAllByRole("tab")).toHaveLength(2);
    expect(screen.getByRole("tab", { name: /共识，有内容/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.getByRole("tab", { name: /完成总结，暂无内容/ })).toBeInTheDocument();
    expect(screen.getByLabelText("清空协作区")).toBeInTheDocument();
    expect(screen.getByText("双方同意优先处理登录页")).toBeInTheDocument();
  });

  it("点完成总结 Tab 显示「暂无内容」", async () => {
    const user = userEvent.setup();
    collabState.area = CONSENSUS_ONLY_AREA;
    renderArea();

    await user.click(screen.getByRole("tab", { name: /完成总结，暂无内容/ }));
    expect(screen.getByRole("tab", { name: /完成总结，暂无内容/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.getByText("暂无内容")).toBeInTheDocument();
  });

  it("仅完成总结有内容时渲染两个 Tab，默认选中完成总结且无 SHA 药丸", () => {
    collabState.area = SUMMARY_ONLY_AREA;
    renderArea();

    expect(screen.getAllByRole("tab")).toHaveLength(2);
    expect(screen.getByRole("tab", { name: /完成总结，有内容/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.getByLabelText("清空协作区")).toBeInTheDocument();
    expect(screen.getByText("已新增顶部按钮")).toBeInTheDocument();
    expect(screen.queryByText(/abc1234/)).not.toBeInTheDocument();
    expect(screen.queryByText(/commit/i)).not.toBeInTheDocument();
  });

  it("两块都有内容时可切换，默认选共识", async () => {
    const user = userEvent.setup();
    collabState.area = FULL_AREA;
    renderArea();

    expect(screen.getByRole("tab", { name: /共识，有内容/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.getByText("双方同意优先处理登录页")).toBeInTheDocument();

    await user.click(screen.getByRole("tab", { name: /完成总结，有内容/ }));
    expect(screen.getByRole("tab", { name: /完成总结，有内容/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.getByText("已新增顶部按钮")).toBeInTheDocument();
    expect(screen.queryByText("双方同意优先处理登录页")).not.toBeInTheDocument();
  });

  it("点击清空协作区弹出确认并调用 mutation，toast「协作区已清空」", async () => {
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

  it("删除共识调用对应 mutation 且 toast「共识已删除」", async () => {
    const user = userEvent.setup();
    collabState.area = FULL_AREA;
    renderArea();

    await user.click(screen.getByLabelText("删除共识"));
    expect(screen.getByText("删除共识？")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "确认删除" }));

    await waitFor(() => {
      expect(collabState.mutateAsync).toHaveBeenCalledTimes(1);
      expect(toastMock.success).toHaveBeenCalledWith("共识已删除");
    });
  });

  it("删除完成总结调用对应 mutation 且 toast「完成总结已删除」", async () => {
    const user = userEvent.setup();
    collabState.area = FULL_AREA;
    renderArea();

    await user.click(screen.getByRole("tab", { name: /完成总结，有内容/ }));
    await user.click(screen.getByLabelText("删除完成总结"));
    expect(screen.getByText("删除完成总结？")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "确认删除" }));

    await waitFor(() => {
      expect(collabState.mutateAsync).toHaveBeenCalledTimes(1);
      expect(toastMock.success).toHaveBeenCalledWith("完成总结已删除");
    });
  });

  it("删除失败时显示错误 toast 且对话框保持打开", async () => {
    const user = userEvent.setup();
    collabState.mutateAsync.mockRejectedValueOnce(new Error("network"));
    collabState.area = FULL_AREA;
    renderArea();

    await user.click(screen.getByLabelText("删除共识"));
    await user.click(screen.getByRole("button", { name: "确认删除" }));

    await waitFor(() => {
      expect(toastMock.error).toHaveBeenCalledWith("删除失败，请稍后重试");
    });
    expect(screen.getByText("删除共识？")).toBeInTheDocument();
  });

  it("删除当前激活 tab 内容后切换到仍有内容的 tab", async () => {
    const user = userEvent.setup();
    const { rerender } = renderArea();

    collabState.area = FULL_AREA;
    rerender(
      <MemoryRouter>
        <CollaborationArea issueId="issue-1" />
      </MemoryRouter>,
    );

    await user.click(screen.getByRole("tab", { name: /完成总结，有内容/ }));
    expect(screen.getByRole("tab", { name: /完成总结，有内容/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );

    collabState.area = CONSENSUS_ONLY_AREA;
    rerender(
      <MemoryRouter>
        <CollaborationArea issueId="issue-1" />
      </MemoryRouter>,
    );

    await waitFor(() => {
      expect(screen.getByRole("tab", { name: /共识，有内容/ })).toHaveAttribute(
        "aria-selected",
        "true",
      );
    });
  });
});
