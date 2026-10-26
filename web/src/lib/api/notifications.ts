import type { components } from "@/types/api.gen";
import { api } from "./client";

// 站内通知（INT-135）：审查系统故障四类，kind 直接取契约枚举。

type Schemas = components["schemas"];

export type NotificationKind = Schemas["Notification"]["kind"];

export type Notification = Schemas["Notification"];

export const notificationApi = {
  list: (params?: { limit?: number; unread_only?: boolean }) => {
    const search = new URLSearchParams();
    if (params?.limit) search.set("limit", String(params.limit));
    if (params?.unread_only) search.set("unread_only", "true");
    const query = search.toString();
    return api
      .get(`notifications${query ? `?${query}` : ""}`)
      .json<ApiResponse<{ items: Notification[] }>>();
  },
  unreadCount: () =>
    api.get("notifications/unread-count").json<ApiResponse<{ count: number }>>(),
  markRead: (id: string) =>
    api.post(`notifications/${id}/read`).json<ApiResponse<null>>(),
  markAllRead: () =>
    api.post("notifications/read-all").json<ApiResponse<null>>(),
};

export function notificationKindLabel(kind: NotificationKind): string {
  switch (kind) {
    case "review_dispatch_failed":
      return "审查派发失败";
    case "review_run_timeout":
      return "审查超时";
    case "review_cleanup_failed":
      return "工作区回收失败";
    case "review_redelivery_failed":
      return "投递重投失败";
    case "bot_budget_threshold":
      return "预算告警";
  }
}
