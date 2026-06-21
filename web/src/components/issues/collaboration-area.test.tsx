import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { CollaborationArea } from "./collaboration-area";

const collabState = vi.hoisted(() => ({
  area: null as unknown as IssueCollabArea,
}));

vi.mock("@/lib/hooks/use-issue-collab", () => ({
  useIssueCollab: () => ({ data: collabState.area, isLoading: false }),
}));

const EMPTY_AREA: IssueCollabArea = {
  suggestions: [],
  plan: null,
  review: null,
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
  });

  it("空状态只渲染标题，不渲染任何区块", () => {
    renderArea();
    expect(screen.getByText("人机协作区")).toBeInTheDocument();
    expect(screen.queryByText(/^实施建议/)).not.toBeInTheDocument();
    expect(screen.queryByText("计划")).not.toBeInTheDocument();
    expect(screen.queryByText("审查结果")).not.toBeInTheDocument();
    expect(screen.queryByText("完成总结")).not.toBeInTheDocument();
  });

  it("渲染四块只读内容", () => {
    collabState.area = {
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

    renderArea({ ...({} as Project), github_owner: "owner", github_repo: "repo" });

    expect(screen.getByText("建议新增顶部按钮")).toBeInTheDocument();
    expect(screen.getByText("支持暗色模式")).toBeInTheDocument();
    expect(screen.getByText("分两步实施")).toBeInTheDocument();
    expect(screen.getByText("审查通过")).toBeInTheDocument();
    expect(screen.getByText("已新增顶部按钮")).toBeInTheDocument();

    // 提交链接指向 GitHub commit
    const link = screen.getByText("abc1234").closest("a");
    expect(link).toHaveAttribute("href", "https://github.com/owner/repo/commit/abc1234");
  });

  it("不渲染任何编辑/作答交互元素（纯只读）", () => {
    collabState.area = {
      suggestions: [
        {
          id: "s1",
          issue_id: "issue-1",
          body: "建议一",
          sort_order: 0,
          author: { kind: "agent", login: "代理" },
          created_at: "2026-06-19T10:00:00Z",
          updated_at: "2026-06-19T10:00:00Z",
        },
      ],
      plan: null,
      review: null,
      summary: null,
    };

    renderArea();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
    expect(screen.queryByText("补充背景")).not.toBeInTheDocument();
    expect(screen.queryByText("提交回答")).not.toBeInTheDocument();
  });
});
