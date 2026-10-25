import { Bell } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { HeaderActions } from "@/components/layout/header-actions";
import { Header } from "@/components/layout/header";
import { formatDate } from "@/lib/utils/format";
import {
  notificationKindLabel,
  type Notification,
} from "@/lib/api/notifications";
import {
  useMarkAllNotificationsRead,
  useMarkNotificationRead,
  useNotifications,
} from "@/lib/hooks/use-notifications";
import { useState } from "react";
import { toast } from "sonner";

// 通知全量列表（INT-90）：铃铛下拉只放最近几条，这里看完整历史并逐条/全部已读。
export default function NotificationsPage() {
  const [unreadOnly, setUnreadOnly] = useState(false);
  const { data: notifications, isLoading } = useNotifications(100);
  const markRead = useMarkNotificationRead();
  const markAllRead = useMarkAllNotificationsRead();

  const items = (notifications ?? []).filter((n) =>
    unreadOnly ? !n.read_at : true,
  );

  const handleMarkAll = async () => {
    try {
      await markAllRead.mutateAsync();
      toast.success("已全部标记为已读");
    } catch {
      toast.error("操作失败");
    }
  };

  return (
    <div className="flex flex-col">
      <Header
        title="通知"
        actions={
          <HeaderActions
            primary={
              <Button size="sm" variant="outline" onClick={handleMarkAll}>
                全部已读
              </Button>
            }
          />
        }
      />
      <div className="mx-auto w-full max-w-4xl space-y-4 p-4 md:p-6">
        <div className="flex items-center justify-between">
          <p className="text-sm text-muted-foreground">
            审查系统故障与 Agent 发布的站内通知。
          </p>
          <Button
            size="sm"
            variant={unreadOnly ? "default" : "outline"}
            onClick={() => setUnreadOnly((v) => !v)}
          >
            {unreadOnly ? "只看未读中" : "只看未读"}
          </Button>
        </div>

        {isLoading ? (
          <div className="rounded-lg border bg-muted/40 py-12 text-center text-sm text-muted-foreground">
            加载中...
          </div>
        ) : items.length === 0 ? (
          <div className="rounded-lg border bg-muted/40 py-12 text-center">
            <Bell className="mx-auto mb-2 h-6 w-6 text-muted-foreground" />
            <p className="text-sm text-muted-foreground">
              {unreadOnly ? "没有未读通知" : "暂无通知"}
            </p>
          </div>
        ) : (
          <div className="overflow-x-auto rounded-lg border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>类型</TableHead>
                  <TableHead className="min-w-[200px]">内容</TableHead>
                  <TableHead className="w-[140px] whitespace-nowrap">时间</TableHead>
                  <TableHead className="text-right">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {items.map((n) => (
                  <NotificationRow
                    key={n.id}
                    notification={n}
                    onMarkRead={() =>
                      markRead.mutate(n.id, {
                        onSuccess: () => toast.success("已标记为已读"),
                      })
                    }
                  />
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </div>
    </div>
  );
}

function NotificationRow({
  notification,
  onMarkRead,
}: {
  notification: Notification;
  onMarkRead: () => void;
}) {
  const unread = !notification.read_at;
  return (
    <TableRow className={unread ? "bg-muted/30" : undefined}>
      <TableCell>
        <Badge variant="secondary">{notificationKindLabel(notification.kind)}</Badge>
      </TableCell>
      <TableCell className="min-w-0">
        <p className="break-words text-sm font-medium">
          {unread && (
            <span
              className="mr-1.5 inline-block h-1.5 w-1.5 rounded-full bg-destructive align-middle"
              aria-label="未读"
            />
          )}
          {notification.title}
        </p>
        {notification.body && (
          <p className="mt-0.5 break-words text-xs text-muted-foreground">{notification.body}</p>
        )}
      </TableCell>
      <TableCell className="w-[140px] whitespace-nowrap text-sm text-muted-foreground">
        {formatDate(notification.updated_at)}
      </TableCell>
      <TableCell className="text-right">
        {unread ? (
          <Button variant="ghost" size="sm" onClick={onMarkRead}>
            标记已读
          </Button>
        ) : (
          <span className="text-xs text-muted-foreground">已读</span>
        )}
      </TableCell>
    </TableRow>
  );
}
