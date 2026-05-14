import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  BoardIssueCard,
  BoardIssueCardOverlay,
} from "./board-issue-card";

const { mockUseDraggable } = vi.hoisted(() => ({
  mockUseDraggable: vi.fn(),
}));

vi.mock("@dnd-kit/core", () => ({
  useDraggable: mockUseDraggable,
}));

const issue: Issue = {
  id: "issue-1",
  project_id: "project-1",
  source: "github",
  sequence_number: 1,
  reference: "#1",
  state: "open",
  state_reason: "",
  title: "图片上传测试",
  body: "",
  body_html: "",
  author: {
    login: "gitbobobo",
    avatar_url: "",
  },
  created_at: "2026-05-14T12:00:00Z",
  updated_at: "2026-05-14T12:00:00Z",
  internal_meta: {
    workflow_status: "in_progress",
    checklist_total: 0,
    checklist_done: 0,
    labels: [],
  },
  github: {
    github_issue_id: 1,
    github_node_id: "node-1",
    number: 1,
    html_url: "https://github.com/gitbobobo/fast_ship/issues/1",
    author_association: "OWNER",
    assignees: [],
    labels: [],
    milestone: null,
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
    synced_at: "2026-05-14T12:00:00Z",
  },
};

function renderInRouter(ui: React.ReactElement) {
  return render(<MemoryRouter>{ui}</MemoryRouter>);
}

function getCardElement() {
  const link = screen.getByRole("link", { name: issue.title });
  return link.closest("div.group");
}

describe("BoardIssueCard", () => {
  beforeEach(() => {
    mockUseDraggable.mockReset();
    mockUseDraggable.mockReturnValue({
      attributes: {},
      listeners: {},
      setNodeRef: vi.fn(),
      transform: null,
      isDragging: false,
    });
  });

  it("does not move the source card while dragging", () => {
    mockUseDraggable.mockReturnValue({
      attributes: {},
      listeners: {},
      setNodeRef: vi.fn(),
      transform: { x: 140, y: 12, scaleX: 1, scaleY: 1 },
      isDragging: true,
    });

    renderInRouter(<BoardIssueCard issue={issue} />);

    const card = getCardElement();

    expect(card).toHaveClass("invisible");
    expect(card).not.toHaveAttribute("style");
  });

  it("keeps the drag transform on the live draggable card before pickup", () => {
    mockUseDraggable.mockReturnValue({
      attributes: {},
      listeners: {},
      setNodeRef: vi.fn(),
      transform: { x: 140, y: 12, scaleX: 1, scaleY: 1 },
      isDragging: false,
    });

    renderInRouter(<BoardIssueCard issue={issue} />);

    const card = getCardElement();

    expect(card).toHaveStyle({
      transform: "translate3d(140px, 12px, 0)",
    });
  });

  it("renders the overlay without attaching draggable state", () => {
    renderInRouter(<BoardIssueCardOverlay issue={issue} />);

    const card = getCardElement();

    expect(mockUseDraggable).not.toHaveBeenCalled();
    expect(card).toHaveClass("pointer-events-none");
    expect(card).not.toHaveClass("invisible");
  });

  it("renders issue title, source badge, state badge and workflow status", () => {
    renderInRouter(<BoardIssueCard issue={issue} />);

    expect(screen.getByRole("link", { name: issue.title })).toBeInTheDocument();
    expect(screen.getByText("GitHub")).toBeInTheDocument();
    expect(screen.getByText("Open")).toBeInTheDocument();
    expect(screen.getByText("开发中")).toBeInTheDocument();
    expect(screen.getByText("@gitbobobo")).toBeInTheDocument();
  });

  it("attaches draggable attributes to the whole card", () => {
    mockUseDraggable.mockReturnValue({
      attributes: {
        tabIndex: 0,
        "aria-roledescription": "draggable",
      },
      listeners: {},
      setNodeRef: vi.fn(),
      transform: null,
      isDragging: false,
    });

    renderInRouter(<BoardIssueCard issue={issue} />);

    const card = getCardElement();

    expect(card).toHaveAttribute("tabindex", "0");
    expect(card).toHaveAttribute("aria-roledescription", "draggable");
  });

  it("does not start dragging when pressing the issue link", () => {
    const onPointerDown = vi.fn();
    mockUseDraggable.mockReturnValue({
      attributes: {},
      listeners: {
        onPointerDown,
      },
      setNodeRef: vi.fn(),
      transform: null,
      isDragging: false,
    });

    renderInRouter(<BoardIssueCard issue={issue} />);

    fireEvent.pointerDown(screen.getByRole("link", { name: issue.title }));

    expect(onPointerDown).not.toHaveBeenCalled();
  });
});
