import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import NotificationsPage from "@/routes/notifications";
import { NotificationBell } from "@/components/layout/notification-bell";
import { renderWithRoute, wrapWithRouter } from "@/test/render";
import {
  useMarkAllNotificationsRead,
  useMarkNotificationRead,
  useNotifications,
  useUnreadNotificationCount,
} from "@/lib/hooks/use-notifications";

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

vi.mock("@/lib/hooks/use-notifications", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("@/lib/hooks/use-notifications")>();
  return {
    ...actual,
    useNotifications: vi.fn(),
    useUnreadNotificationCount: vi.fn(),
    useMarkNotificationRead: vi.fn(),
    useMarkAllNotificationsRead: vi.fn(),
  };
});

const notification = {
  id: "n-1",
  kind: "review_run_timeout",
  title: "审查运行超时",
  body: "「夜间回报」的审查运行超时",
  environment_id: "env-1",
  thread_id: "th-1",
  run_id: "run-1",
  created_at: "2026-10-14T00:00:00Z",
  updated_at: "2026-10-14T00:10:00Z",
  last_seen_at: "2026-10-14T00:10:00Z",
  read_at: null,
};

describe("NotificationBell", () => {
  const markAllMutate = vi.fn();

  beforeEach(() => {
    vi.mocked(useUnreadNotificationCount).mockReturnValue({
      data: 2,
    } as unknown as ReturnType<typeof useUnreadNotificationCount>);
    vi.mocked(useNotifications).mockReturnValue({
      data: [notification],
    } as unknown as ReturnType<typeof useNotifications>);
    vi.mocked(useMarkAllNotificationsRead).mockReturnValue({
      mutate: markAllMutate,
      mutateAsync: vi.fn(),
    } as unknown as ReturnType<typeof useMarkAllNotificationsRead>);
    vi.mocked(useMarkNotificationRead).mockReturnValue({
      mutate: vi.fn(),
      mutateAsync: vi.fn(),
    } as unknown as ReturnType<typeof useMarkNotificationRead>);
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it("shows the unread badge count", () => {
    render(wrapWithRouter(<NotificationBell />));

    const badge = screen.getByTestId("notification-unread-badge");
    expect(badge).toHaveTextContent("2");
    expect(screen.getByRole("button", { name: /通知（2 条未读）/ })).toBeInTheDocument();
  });

  it("opens recent notifications and offers mark-all-read", async () => {
    const user = userEvent.setup();
    render(wrapWithRouter(<NotificationBell />));

    await user.click(screen.getByRole("button", { name: /通知（2 条未读）/ }));

    expect(await screen.findByText("审查运行超时")).toBeInTheDocument();
    const markAll = screen.getByRole("button", { name: "全部已读" });
    await user.click(markAll);
    expect(markAllMutate).toHaveBeenCalledTimes(1);
  });

  it("hides the badge when there are no unread items", () => {
    vi.mocked(useUnreadNotificationCount).mockReturnValue({
      data: 0,
    } as unknown as ReturnType<typeof useUnreadNotificationCount>);

    render(wrapWithRouter(<NotificationBell />));

    expect(
      screen.queryByTestId("notification-unread-badge"),
    ).not.toBeInTheDocument();
  });
});

describe("NotificationsPage", () => {
  const markMutate = vi.fn();

  beforeEach(() => {
    vi.mocked(useNotifications).mockReturnValue({
      data: [
        notification,
        {
          ...notification,
          id: "n-2",
          kind: "review_cleanup_failed",
          title: "工作区回收失败通知",
          read_at: "2026-10-14T01:00:00Z",
        },
      ],
      isLoading: false,
    } as unknown as ReturnType<typeof useNotifications>);
    vi.mocked(useUnreadNotificationCount).mockReturnValue({
      data: 1,
    } as unknown as ReturnType<typeof useUnreadNotificationCount>);
    vi.mocked(useMarkNotificationRead).mockReturnValue({
      mutate: markMutate,
      mutateAsync: vi.fn(),
    } as unknown as ReturnType<typeof useMarkNotificationRead>);
    vi.mocked(useMarkAllNotificationsRead).mockReturnValue({
      mutate: vi.fn(),
      mutateAsync: vi.fn().mockResolvedValue({}),
    } as unknown as ReturnType<typeof useMarkAllNotificationsRead>);
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it("lists notifications with kind labels and read state", () => {
    renderWithRoute(<NotificationsPage />, {
      path: "/notifications",
      initialEntry: "/notifications",
    });

    expect(screen.getByText("审查运行超时")).toBeInTheDocument();
    expect(screen.getByText("审查超时")).toBeInTheDocument();
    expect(screen.getByText("工作区回收失败通知")).toBeInTheDocument();
    expect(screen.getByText("工作区回收失败")).toBeInTheDocument();
    expect(screen.getByText("标记已读")).toBeInTheDocument();
    expect(screen.getByText("已读")).toBeInTheDocument();
  });

  it("marks a notification read", async () => {
    const user = userEvent.setup();
    renderWithRoute(<NotificationsPage />, {
      path: "/notifications",
      initialEntry: "/notifications",
    });

    await user.click(screen.getByRole("button", { name: "标记已读" }));
    await waitFor(() => expect(markMutate).toHaveBeenCalledWith("n-1", expect.anything()));
  });

  it("filters unread only", async () => {
    const user = userEvent.setup();
    renderWithRoute(<NotificationsPage />, {
      path: "/notifications",
      initialEntry: "/notifications",
    });

    await user.click(screen.getByRole("button", { name: "只看未读" }));

    expect(screen.queryByText("Agent 汇报")).not.toBeInTheDocument();
    expect(screen.getByText("审查运行超时")).toBeInTheDocument();
  });
});
