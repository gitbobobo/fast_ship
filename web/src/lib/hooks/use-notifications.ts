import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { notificationApi } from "@/lib/api/notifications";
import { useAuthStore } from "@/lib/store/auth-store";

// 通知查询（INT-90）。未读数 30 秒轮询 + 窗口聚焦刷新；缓存按用户隔离。

const ROOT = "notifications";
export const NOTIFICATION_UNREAD_POLL_MS = 30_000;

const userId = () => useAuthStore.getState().user?.id ?? null;

const keys = {
  list: () => [ROOT, userId(), "list"],
  unreadCount: () => [ROOT, userId(), "unread-count"],
};

export function useNotifications(limit = 50) {
  return useQuery({
    queryKey: [...keys.list(), limit],
    queryFn: async () => (await notificationApi.list({ limit })).data.items,
    enabled: Boolean(userId()),
  });
}

export function useUnreadNotificationCount() {
  return useQuery({
    queryKey: keys.unreadCount(),
    queryFn: async () => (await notificationApi.unreadCount()).data.count,
    enabled: Boolean(userId()),
    refetchInterval: NOTIFICATION_UNREAD_POLL_MS,
    // 全局默认关闭聚焦刷新；未读数是铃铛角标，聚焦回来必须立刻对齐。
    refetchOnWindowFocus: true,
  });
}

function useInvalidateNotifications() {
  const queryClient = useQueryClient();
  return () => {
    queryClient.invalidateQueries({ queryKey: [ROOT, userId()] });
  };
}

export function useMarkNotificationRead() {
  const invalidate = useInvalidateNotifications();
  return useMutation({
    mutationFn: (id: string) => notificationApi.markRead(id),
    onSuccess: invalidate,
  });
}

export function useMarkAllNotificationsRead() {
  const invalidate = useInvalidateNotifications();
  return useMutation({
    mutationFn: () => notificationApi.markAllRead(),
    onSuccess: invalidate,
  });
}
