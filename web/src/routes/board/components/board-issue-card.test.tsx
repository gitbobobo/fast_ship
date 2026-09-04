import { createEvent, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { BoardSelectionProvider } from "@/routes/board/lib/board-selection-context";
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
  reference: "GH-1",
  state: "open",
  state_reason: "",
  title: "图片上传测试",
  body: "",
  body_html: "",
  author: {
    login: "gitbobobo",
    avatar_url: "",
  },
  unread_comments_count: 0,
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

function renderWithSelection(
  ui: React.ReactElement,
  {
    multiSelectMode = false,
    selectedIssueIds = new Set<string>(),
    selectIssue = vi.fn(),
    selectPreview = false,
  }: {
    multiSelectMode?: boolean;
    selectPreview?: boolean;
    selectedIssueIds?: ReadonlySet<string>;
    selectIssue?: (issue: Issue, modifiers: { shiftKey: boolean }) => void;
  } = {},
) {
  return renderInRouter(
    <BoardSelectionProvider
      value={{
        multiSelectMode,
        selectPreview,
        selectedIssueIds,
        selectIssue,
      }}
    >
      {ui}
    </BoardSelectionProvider>,
  );
}

function getCardElement() {
  const link = screen.getByRole("link", { name: `${issue.reference} ${issue.title}` });
  return link.closest("div.group");
}

function getCardElementByTitle() {
  return screen.getByText(issue.title).closest("div.group");
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

  it("renders issue title and author without status badges", () => {
    renderInRouter(<BoardIssueCard issue={issue} />);

    expect(screen.getByRole("link", { name: `${issue.reference} ${issue.title}` })).toBeInTheDocument();
    expect(screen.getByText("GH-1")).toBeInTheDocument();
    expect(screen.getByText("@gitbobobo")).toBeInTheDocument();
    expect(screen.queryByText("GitHub")).not.toBeInTheDocument();
    expect(screen.queryByText("Open")).not.toBeInTheDocument();
    expect(screen.queryByText("开发中")).not.toBeInTheDocument();
  });

  it("renders INT- reference for internal issues", () => {
    renderInRouter(
      <BoardIssueCard
        issue={{
          ...issue,
          source: "internal",
          reference: "INT-3",
          github: undefined,
        }}
      />,
    );

    expect(screen.getByText("INT-3")).toBeInTheDocument();
  });

  it("shows github icon next to the author for github issues", () => {
    renderInRouter(<BoardIssueCard issue={issue} />);

    const icon = screen.getByRole("img", { name: "GitHub" });
    const author = screen.getByText("@gitbobobo");

    // 图标渲染在作者左侧的同一行
    expect(icon.parentElement).toBe(author.parentElement);
    expect(icon.nextElementSibling).toBe(author);
  });

  it("does not show github icon for internal issues", () => {
    renderInRouter(
      <BoardIssueCard
        issue={{
          ...issue,
          source: "internal",
          github: undefined,
          internal_meta: {
            ...issue.internal_meta!,
            labels: [],
          },
        }}
      />,
    );

    expect(screen.queryByRole("img", { name: "GitHub" })).not.toBeInTheDocument();
  });

  it("renders pending ship hook badge", () => {
    renderInRouter(
      <BoardIssueCard
        issue={{
          ...issue,
          ship_hook: {
            status: "pending",
            comment_enabled: false,
            close_enabled: true,
            workflow_enabled: false,
            workflow_status: "",
          },
        }}
      />,
    );
    expect(screen.getByText("发货后")).toBeInTheDocument();
  });

  it("renders failed ship hook badge", () => {
    renderInRouter(
      <BoardIssueCard
        issue={{
          ...issue,
          ship_hook: {
            status: "fired",
            comment_enabled: false,
            close_enabled: true,
            workflow_enabled: false,
            workflow_status: "",
            version_number: "1.0.0",
            results: {
              close: { ok: false, error: "already closed" },
            },
          },
        }}
      />,
    );
    expect(screen.getByText("钩子失败")).toBeInTheDocument();
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

    fireEvent.pointerDown(screen.getByRole("link", { name: `${issue.reference} ${issue.title}` }));

    expect(onPointerDown).not.toHaveBeenCalled();
  });

  describe("unread comments", () => {
    it("outlines the card in github blue and names the unread state", () => {
      renderInRouter(
        <BoardIssueCard issue={{ ...issue, unread_comments_count: 3 }} />,
      );

      const card = getCardElement();

      expect(card).toHaveClass("outline-github-accent");
      expect(card).toHaveAttribute(
        "aria-label",
        `${issue.reference} ${issue.title}，有未读评论`,
      );
    });

    it("keeps the default border once everything is read", () => {
      renderInRouter(<BoardIssueCard issue={issue} />);

      const card = getCardElement();

      expect(card).not.toHaveClass("outline-github-accent");
      expect(card).not.toHaveAttribute("aria-label");
    });

    it("ignores unread counts on internal issues", () => {
      renderInRouter(
        <BoardIssueCard
          issue={{
            ...issue,
            source: "internal",
            github: undefined,
            unread_comments_count: 5,
          }}
        />,
      );

      const card = getCardElement();

      expect(card).not.toHaveClass("outline-github-accent");
      expect(card).not.toHaveAttribute("aria-label");
    });

    it("keeps the unread outline on the drag overlay", () => {
      renderInRouter(
        <BoardIssueCardOverlay issue={{ ...issue, unread_comments_count: 2 }} />,
      );

      expect(getCardElement()).toHaveClass("outline-github-accent");
    });

    it("shows unread outline and selected border together in multi-select", () => {
      renderWithSelection(
        <BoardIssueCard issue={{ ...issue, unread_comments_count: 2 }} />,
        {
          multiSelectMode: true,
          selectedIssueIds: new Set([issue.id]),
        },
      );

      const card = getCardElementByTitle();

      expect(card).toHaveClass("outline-github-accent");
      expect(card).toHaveClass("border-primary");
      expect(card).toHaveClass("ring-primary/30");
    });
  });

  describe("multi-select mode", () => {
    const selectIssue = vi.fn();

    beforeEach(() => {
      selectIssue.mockClear();
    });

    function renderMultiSelectCard(
      props: Partial<{
        selectedIssueIds: ReadonlySet<string>;
      }> = {},
    ) {
      return renderWithSelection(<BoardIssueCard issue={issue} />, {
        multiSelectMode: true,
        selectedIssueIds: props.selectedIssueIds ?? new Set(),
        selectIssue,
      });
    }

    it("disables dragging through useDraggable while keeping attributes attached", () => {
      const onPointerDown = vi.fn();
      mockUseDraggable.mockReturnValue({
        attributes: {
          tabIndex: 0,
          "aria-roledescription": "draggable",
        },
        listeners: {
          onPointerDown,
        },
        setNodeRef: vi.fn(),
        transform: null,
        isDragging: false,
      });

      renderMultiSelectCard();

      expect(mockUseDraggable).toHaveBeenCalledWith(
        expect.objectContaining({
          id: issue.id,
          disabled: true,
        }),
      );

      const card = getCardElementByTitle() as HTMLElement;
      expect(card).toHaveAttribute("tabindex", "0");
      expect(card).toHaveAttribute("aria-roledescription", "draggable");
    });

    it("renders the title as a non-link element", () => {
      renderMultiSelectCard();

      expect(screen.queryByRole("link")).not.toBeInTheDocument();
      expect(screen.getByText(issue.title)).toBeInTheDocument();
    });

    it("calls selectIssue when clicking the card in the mode", () => {
      renderMultiSelectCard();

      const card = getCardElementByTitle() as HTMLElement;

      fireEvent.click(card);
      expect(selectIssue).toHaveBeenCalledTimes(1);
      expect(selectIssue).toHaveBeenCalledWith(issue, { shiftKey: false });

      fireEvent.click(card, { shiftKey: true });
      expect(selectIssue).toHaveBeenLastCalledWith(issue, { shiftKey: true });
    });

    it("calls selectIssue when clicking the title in the mode", () => {
      renderMultiSelectCard();

      fireEvent.click(screen.getByText(issue.title));

      expect(selectIssue).toHaveBeenCalledTimes(1);
      expect(selectIssue).toHaveBeenCalledWith(issue, { shiftKey: false });
    });

    it("keeps Cmd/Ctrl clicks behaving like plain clicks in the mode", () => {
      renderMultiSelectCard();

      fireEvent.click(getCardElementByTitle() as HTMLElement, {
        metaKey: true,
      });

      expect(selectIssue).toHaveBeenCalledTimes(1);
      expect(selectIssue).toHaveBeenCalledWith(issue, { shiftKey: false });
    });

    it("renders a persistent checkbox reflecting the selected state", () => {
      const { rerender } = renderMultiSelectCard({
        selectedIssueIds: new Set(),
      });

      const checkbox = screen.getByRole("checkbox");
      expect(checkbox).toHaveAttribute("aria-checked", "false");

      rerender(
        <MemoryRouter>
          <BoardSelectionProvider
            value={{
              multiSelectMode: true,
              selectPreview: false,
              selectedIssueIds: new Set([issue.id]),
              selectIssue,
            }}
          >
            <BoardIssueCard issue={issue} />
          </BoardSelectionProvider>
        </MemoryRouter>,
      );

      expect(screen.getByRole("checkbox")).toHaveAttribute("aria-checked", "true");
    });

    it("does not render a checkbox outside multi-select mode", () => {
      renderInRouter(<BoardIssueCard issue={issue} />);

      expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
    });
  });

  describe("select preview while Ctrl/Cmd is held", () => {
    it("shows a checkbox but keeps the title as a link", () => {
      renderWithSelection(<BoardIssueCard issue={issue} />, {
        selectPreview: true,
      });

      expect(screen.getByRole("checkbox")).toHaveAttribute(
        "aria-checked",
        "false",
      );
      expect(
        screen.getByRole("link", {
          name: `${issue.reference} ${issue.title}`,
        }),
      ).toBeInTheDocument();
    });

    it("disables dragging while the preview is active", () => {
      renderWithSelection(<BoardIssueCard issue={issue} />, {
        selectPreview: true,
      });

      expect(mockUseDraggable).toHaveBeenCalledWith(
        expect.objectContaining({
          id: issue.id,
          disabled: true,
        }),
      );
    });
  });

  describe("selectIssue via Cmd/Ctrl outside the mode", () => {
    const selectIssue = vi.fn();
    const dragPointerDownMock = vi.fn();

    beforeEach(() => {
      selectIssue.mockClear();
      dragPointerDownMock.mockClear();
    });

    function renderCardOutsideMode() {
      mockUseDraggable.mockReturnValue({
        attributes: {},
        listeners: {
          onPointerDown: dragPointerDownMock,
        },
        setNodeRef: vi.fn(),
        transform: null,
        isDragging: false,
      });

      return renderWithSelection(<BoardIssueCard issue={issue} />, {
        multiSelectMode: false,
        selectedIssueIds: new Set(),
        selectIssue,
      });
    }

    it("blocks the drag pointerdown and calls selectIssue on Cmd/Ctrl click", () => {
      renderCardOutsideMode();

      const card = getCardElement() as HTMLElement;

      const pointerDownEvent = createEvent.pointerDown(card, {
        metaKey: true,
      });
      fireEvent(card, pointerDownEvent);
      expect(pointerDownEvent.defaultPrevented).toBe(true);
      expect(dragPointerDownMock).not.toHaveBeenCalled();

      const clickEvent = createEvent.click(card, { ctrlKey: true });
      fireEvent(card, clickEvent);
      expect(clickEvent.defaultPrevented).toBe(true);
      expect(selectIssue).toHaveBeenCalledTimes(1);
      expect(selectIssue).toHaveBeenCalledWith(issue, { shiftKey: false });
    });

    it("still forwards plain pointerdown to the drag listeners", () => {
      renderCardOutsideMode();

      const card = getCardElement() as HTMLElement;

      fireEvent.pointerDown(card);

      expect(dragPointerDownMock).toHaveBeenCalledTimes(1);
    });

    it("does nothing special on plain card clicks", () => {
      renderCardOutsideMode();

      fireEvent.click(getCardElement() as HTMLElement);

      expect(selectIssue).not.toHaveBeenCalled();
    });

    it("prevents link navigation and calls selectIssue on Cmd/Ctrl title click", () => {
      renderCardOutsideMode();

      const link = screen.getByRole("link", {
        name: `${issue.reference} ${issue.title}`,
      });

      const pointerDownEvent = createEvent.pointerDown(link, {
        ctrlKey: true,
      });
      fireEvent(link, pointerDownEvent);
      expect(dragPointerDownMock).not.toHaveBeenCalled();

      const clickEvent = createEvent.click(link, { metaKey: true });
      fireEvent(link, clickEvent);
      expect(clickEvent.defaultPrevented).toBe(true);
      expect(selectIssue).toHaveBeenCalledTimes(1);
      expect(selectIssue).toHaveBeenCalledWith(issue, { shiftKey: false });
    });

    it("keeps plain title clicks navigating without calling selectIssue", () => {
      renderCardOutsideMode();

      const link = screen.getByRole("link", {
        name: `${issue.reference} ${issue.title}`,
      });
      fireEvent.click(link);

      // Link 自身会 preventDefault 并走路由导航，这里只断言不进入多选
      expect(selectIssue).not.toHaveBeenCalled();
    });
  });
});
