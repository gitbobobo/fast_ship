import { useNavigate } from "react-router";
import { Bell } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  useMarkAllNotificationsRead,
  useNotifications,
  useUnreadNotificationCount,
} from "@/lib/hooks/use-notifications";
import {
  notificationKindLabel,
  type Notification,
} from "@/lib/api/notifications";
import { cn } from "@/lib/utils";

// 通知铃（INT-90）：未读角标 + 最近通知下拉。未读数 30 秒轮询、聚焦即刷新
// （见 use-notifications）；全量列表在 /notifications 页面。
export function NotificationBell() {
  const navigate = useNavigate();
  const { data: unreadCount = 0 } = useUnreadNotificationCount();
  const { data: notifications = [] } = useNotifications(8);
  const markAllRead = useMarkAllNotificationsRead();

  const recent: Notification[] = notifications.slice(0, 8);

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={
          <Button
            variant="ghost"
            size="icon"
            className="relative h-8 w-8"
            aria-label={`通知${unreadCount > 0 ? `（${unreadCount} 条未读）` : ""}`}
          />
        }
      >
        <Bell className="h-4 w-4" />
        {unreadCount > 0 && (
          <span
            data-testid="notification-unread-badge"
            className={cn(
              "absolute -right-0.5 -top-0.5 flex h-4 min-w-4 items-center justify-center",
              "rounded-full bg-destructive px-1 text-[10px] font-medium leading-none text-white",
            )}
          >
            {unreadCount > 99 ? "99+" : unreadCount}
          </span>
        )}
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-80">
        <div className="flex items-center justify-between px-2 py-1.5">
          <span className="text-sm font-medium">通知</span>
          {unreadCount > 0 && (
            <button
              type="button"
              className="text-xs text-muted-foreground hover:text-foreground"
              onClick={() => markAllRead.mutate()}
            >
              全部已读
            </button>
          )}
        </div>
        <DropdownMenuSeparator />
        {recent.length === 0 ? (
          <div className="px-3 py-6 text-center text-sm text-muted-foreground">
            暂无通知
          </div>
        ) : (
          recent.map((item) => (
            <DropdownMenuItem
              key={item.id}
              className="flex cursor-pointer flex-col items-start gap-0.5 py-2"
              onSelect={() => navigate("/notifications")}
            >
              <span className="flex w-full items-center gap-2">
                <span className="text-xs text-muted-foreground">
                  {notificationKindLabel(item.kind)}
                </span>
                {!item.read_at && (
                  <span
                    className="h-1.5 w-1.5 rounded-full bg-destructive"
                    aria-label="未读"
                  />
                )}
              </span>
              <span className="w-full truncate text-sm font-medium">
                {item.title}
              </span>
              {item.body && (
                <span className="w-full truncate text-xs text-muted-foreground">
                  {item.body}
                </span>
              )}
            </DropdownMenuItem>
          ))
        )}
        <DropdownMenuSeparator />
        <DropdownMenuItem
          className="cursor-pointer justify-center text-sm text-muted-foreground"
          onSelect={() => navigate("/notifications")}
        >
          查看全部
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
