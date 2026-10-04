import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { RecommendedIssuesButton } from "./recommended-issues-button";

const {
  useRecommendationsMock,
  mutateAsyncMock,
  toastSuccessMock,
  copyWithToastMock,
} = vi.hoisted(() => ({
  useRecommendationsMock: vi.fn(),
  mutateAsyncMock: vi.fn(),
  toastSuccessMock: vi.fn(),
  copyWithToastMock: vi.fn(),
}));

vi.mock("@/lib/copy", () => ({ copyWithToast: copyWithToastMock }));

vi.mock("@/lib/hooks/use-issue-prompt", () => ({
  useIssuePromptList: () => [
    { id: "default", name: "默认", content: "请处理此问题", supports_batch: true },
  ],
}));

vi.mock("@/lib/hooks/use-recommendations", () => ({
  useRecommendations: (projectId?: string) =>
    useRecommendationsMock(projectId),
  useRemoveRecommendation: () => ({
    mutateAsync: mutateAsyncMock,
    isPending: false,
  }),
}));

vi.mock("sonner", () => ({
  toast: {
    success: toastSuccessMock,
    error: vi.fn(),
    info: vi.fn(),
    warning: vi.fn(),
  },
}));

const item: IssueRecommendation = {
  issue: {
    id: "issue-1",
    project_id: "project-1",
    project_name: "musiver",
    source: "internal",
    sequence_number: 3,
    reference: "INT-3",
    title: "推荐这个任务",
    state: "open",
    workflow_status: "todo",
  },
  reason: "先做这个能解锁后续工作",
  priority: "high",
  created_by: "ci-bot",
  created_at: "2026-08-22T00:00:00Z",
  updated_at: "2026-08-22T00:00:00Z",
  dependencies: [
    {
      issue_id: "dep-1",
      title: "前置任务",
      state: "closed",
      workflow_status: "done",
      project_id: "project-1",
      sequence_number: 1,
      reference: "INT-1",
    },
  ],
};

function renderButton() {
  return render(
    <MemoryRouter>
      <RecommendedIssuesButton projectId="project-1" />
    </MemoryRouter>,
  );
}

describe("RecommendedIssuesButton", () => {
  beforeEach(() => {
    useRecommendationsMock.mockReset();
    mutateAsyncMock.mockReset();
    mutateAsyncMock.mockResolvedValue(undefined);
    copyWithToastMock.mockReset();
    copyWithToastMock.mockResolvedValue(true);
  });

  it("renders nothing when there are no recommendations", () => {
    useRecommendationsMock.mockReturnValue({
      data: { items: [] },
      isLoading: false,
      isError: false,
    });
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

  it("shows the dialog with recommendation cards on click", async () => {
    useRecommendationsMock.mockReturnValue({
      data: { items: [item] },
      isLoading: false,
      isError: false,
    });
    renderButton();

    fireEvent.click(screen.getByRole("button", { name: /推荐/ }));

    expect(await screen.findByText("推荐任务")).toBeInTheDocument();
    expect(screen.getByText("推荐这个任务")).toBeInTheDocument();
    expect(screen.getByText("INT-3")).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "高优先级" })).toBeInTheDocument();
    expect(screen.getByText("先做这个能解锁后续工作")).toBeInTheDocument();
    expect(screen.getByText("前置任务")).toBeInTheDocument();
    expect(screen.getByText("已关闭")).toBeInTheDocument();
    expect(screen.getByText(/ci-bot/)).toBeInTheDocument();
    expect(screen.queryByText(/musiver/)).not.toBeInTheDocument();
  });

  it("shows the project name when listing across projects", async () => {
    useRecommendationsMock.mockReturnValue({
      data: { items: [item] },
      isLoading: false,
      isError: false,
    });
    render(
      <MemoryRouter>
        <RecommendedIssuesButton />
      </MemoryRouter>,
    );
    fireEvent.click(screen.getByRole("button", { name: /推荐/ }));

    expect(await screen.findByText(/musiver/)).toBeInTheDocument();
  });

  it("copies a prompt that includes the recommendation reason", async () => {
    useRecommendationsMock.mockReturnValue({
      data: { items: [item] },
      isLoading: false,
      isError: false,
    });
    renderButton();
    fireEvent.click(screen.getByRole("button", { name: /推荐/ }));
    await screen.findByText("推荐任务");

    fireEvent.click(screen.getByRole("button", { name: /复制提示词/ }));

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

  it("deletes a recommendation after confirming", async () => {
    useRecommendationsMock.mockReturnValue({
      data: { items: [item] },
      isLoading: false,
      isError: false,
    });
    renderButton();
    fireEvent.click(screen.getByRole("button", { name: /推荐/ }));
    await screen.findByText("推荐任务");

    fireEvent.click(
      screen.getByRole("button", { name: "移除推荐" }),
    );
    fireEvent.click(
      await screen.findByRole("button", { name: "确认移除" }),
    );

    await waitFor(() =>
      expect(mutateAsyncMock).toHaveBeenCalledWith("issue-1"),
    );
    await waitFor(() => expect(toastSuccessMock).toHaveBeenCalled());
  });
});
