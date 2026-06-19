import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { CollaborationArea } from "./collaboration-area";

const collabState = vi.hoisted(() => ({
  area: null as unknown as IssueCollabArea,
  answerMock: { mutateAsync: vi.fn(), isPending: false },
  noteMock: { mutateAsync: vi.fn(), isPending: false },
}));

vi.mock("@/lib/hooks/use-issue-collab", () => ({
  useIssueCollab: () => ({ data: collabState.area, isLoading: false }),
  useCreateCollabNote: () => collabState.noteMock,
  useUpdateCollabNote: () => collabState.noteMock,
  useDeleteCollabNote: () => collabState.noteMock,
  useAnswerCollabQuestion: () => collabState.answerMock,
  useUpsertCollabSummary: () => collabState.noteMock,
}));

const EMPTY_AREA: IssueCollabArea = {
  notes: [],
  questions: [],
  summary: null,
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
    collabState.answerMock.mutateAsync.mockResolvedValue(undefined);
    collabState.answerMock.isPending = false;
    collabState.noteMock.mutateAsync.mockResolvedValue(undefined);
    collabState.noteMock.isPending = false;
  });

  it("空状态只渲染背景信息块", () => {
    renderArea();
    expect(screen.getByText("背景信息")).toBeInTheDocument();
    expect(screen.getByText(/暂无背景/)).toBeInTheDocument();
    expect(screen.queryByText("完成总结")).not.toBeInTheDocument();
    // 没有问题时不渲染问题标题
    expect(screen.queryByText(/^问题/)).not.toBeInTheDocument();
  });

  it("渲染问题、回答与完成总结", () => {
    collabState.area = {
      notes: [
        {
          id: "n1",
          issue_id: "issue-1",
          body: "这个按钮主要给运营用",
          author: { kind: "user", login: "alice" },
          created_at: "2026-06-19T10:00:00Z",
          updated_at: "2026-06-19T10:00:00Z",
        },
      ],
      questions: [
        {
          id: "q1",
          issue_id: "issue-1",
          body: "按钮放哪里？",
          options: ["顶部", "侧边"],
          sort_order: 0,
          author: { kind: "agent", login: "代理" },
          answer: null,
          created_at: "2026-06-19T10:00:00Z",
          updated_at: "2026-06-19T10:00:00Z",
        },
        {
          id: "q2",
          issue_id: "issue-1",
          body: "补充说明？",
          options: [],
          sort_order: 1,
          author: { kind: "agent", login: "代理" },
          answer: {
            value: "需要支持暗色",
            author: { kind: "user", login: "alice" },
            answered_at: "2026-06-19T11:00:00Z",
          },
          created_at: "2026-06-19T10:00:00Z",
          updated_at: "2026-06-19T11:00:00Z",
        },
      ],
      summary: {
        issue_id: "issue-1",
        body: "已新增顶部按钮",
        commit_ids: ["abc1234"],
        author: { kind: "agent", login: "代理" },
        created_at: "2026-06-19T12:00:00Z",
        updated_at: "2026-06-19T12:00:00Z",
      },
    };

    renderArea({ ...({} as Project), github_owner: "owner", github_repo: "repo" });

    expect(screen.getByText("这个按钮主要给运营用")).toBeInTheDocument();
    expect(screen.getByText("按钮放哪里？")).toBeInTheDocument();
    // 已作答的自由文本问题显示回答
    expect(screen.getByText("需要支持暗色")).toBeInTheDocument();
    expect(screen.getByText("已新增顶部按钮")).toBeInTheDocument();
    // 提交链接指向 GitHub commit
    const link = screen.getByText("abc1234").closest("a");
    expect(link).toHaveAttribute(
      "href",
      "https://github.com/owner/repo/commit/abc1234",
    );
  });

  it("用选项作答会调用 answer mutation", async () => {
    collabState.area = {
      notes: [],
      questions: [
        {
          id: "q1",
          issue_id: "issue-1",
          body: "按钮放哪里？",
          options: ["顶部", "侧边"],
          sort_order: 0,
          author: { kind: "agent", login: "代理" },
          answer: null,
          created_at: "2026-06-19T10:00:00Z",
          updated_at: "2026-06-19T10:00:00Z",
        },
      ],
      summary: null,
    };

    renderArea();
    fireEvent.click(screen.getByText("顶部"));
    fireEvent.click(screen.getByRole("button", { name: "提交回答" }));

    await waitFor(() => {
      expect(collabState.answerMock.mutateAsync).toHaveBeenCalledWith({
        questionId: "q1",
        answer: "顶部",
      });
    });
  });

  it("自填答案作答", async () => {
    collabState.area = {
      notes: [],
      questions: [
        {
          id: "q2",
          issue_id: "issue-1",
          body: "补充说明？",
          options: [],
          sort_order: 0,
          author: { kind: "agent", login: "代理" },
          answer: null,
          created_at: "2026-06-19T10:00:00Z",
          updated_at: "2026-06-19T10:00:00Z",
        },
      ],
      summary: null,
    };

    renderArea();
    const input = screen.getByPlaceholderText("输入你的答案");
    fireEvent.change(input, { target: { value: "需要支持暗色模式" } });
    fireEvent.click(screen.getByRole("button", { name: "提交回答" }));

    await waitFor(() => {
      expect(collabState.answerMock.mutateAsync).toHaveBeenCalledWith({
        questionId: "q2",
        answer: "需要支持暗色模式",
      });
    });
  });

  it("改答时命中选项的答案回显为选中选项（不显示自填框）", () => {
    collabState.area = {
      notes: [],
      questions: [
        {
          id: "q1",
          issue_id: "issue-1",
          body: "按钮放哪里？",
          options: ["顶部", "侧边"],
          sort_order: 0,
          author: { kind: "agent", login: "代理" },
          answer: {
            value: "顶部",
            author: { kind: "user", login: "alice" },
            answered_at: "2026-06-19T11:00:00Z",
          },
          created_at: "2026-06-19T10:00:00Z",
          updated_at: "2026-06-19T11:00:00Z",
        },
      ],
      summary: null,
    };

    renderArea();
    // 已作答时显示回答值
    expect(screen.getByText("顶部")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "改" }));

    // 命中选项 → 选中该选项，不应出现自填输入框
    expect(screen.queryByPlaceholderText("输入你的答案")).not.toBeInTheDocument();
    // 提交按钮可用（已选中"顶部"）
    expect(screen.getByRole("button", { name: "提交回答" })).not.toBeDisabled();
  });

  it("改答时自定义答案回显到自填框并选中'其他'", () => {
    collabState.area = {
      notes: [],
      questions: [
        {
          id: "q1",
          issue_id: "issue-1",
          body: "按钮放哪里？",
          options: ["顶部", "侧边"],
          sort_order: 0,
          author: { kind: "agent", login: "代理" },
          answer: {
            value: "需要暗色模式",
            author: { kind: "user", login: "alice" },
            answered_at: "2026-06-19T11:00:00Z",
          },
          created_at: "2026-06-19T10:00:00Z",
          updated_at: "2026-06-19T11:00:00Z",
        },
      ],
      summary: null,
    };

    renderArea();
    fireEvent.click(screen.getByRole("button", { name: "改" }));

    // 自定义答案 → 显示自填框并回填
    const input = screen.getByPlaceholderText("输入你的答案") as HTMLInputElement;
    expect(input.value).toBe("需要暗色模式");
  });
});
