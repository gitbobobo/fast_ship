import type { components } from "@/types/api.gen";
import { api } from "./client";

// 站内通知（INT-135）：审查系统故障与 Agent 发布。
// Notification 引用契约；kind 的字面量联合保留手写——契约枚举只有四个
// review_* 值，前端另含 "agent"（Agent 发布文案），穷尽 switch 的标签映射依赖它。

type Schemas = components["schemas"];

export type NotificationKind =
  | "agent"
  | "review_dispatch_failed"
  | "review_run_timeout"
  | "review_cleanup_failed"
  | "review_redelivery_failed";

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
    case "agent":
      return "Agent";
    case "review_dispatch_failed":
      return "审查派发失败";
    case "review_run_timeout":
      return "审查超时";
    case "review_cleanup_failed":
      return "工作区回收失败";
    case "review_redelivery_failed":
      return "投递重投失败";
  }
}
