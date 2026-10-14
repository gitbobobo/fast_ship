import { api } from "./client";

// 站内通知（INT-90）：自动化运行结果与 Agent 发布。

export type NotificationKind =
  | "agent"
  | "automation_dispatch_failed"
  | "automation_run_timeout"
  | "automation_post_failed"
  | "automation_busy";

export interface Notification {
  id: string;
  kind: NotificationKind;
  title: string;
  body: string;
  environment_id?: string | null;
  thread_id?: string | null;
  run_id?: string | null;
  created_at: string;
  updated_at: string;
  last_seen_at: string;
  read_at?: string | null;
}

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
    case "automation_dispatch_failed":
      return "启动失败";
    case "automation_run_timeout":
      return "运行超时";
    case "automation_post_failed":
      return "后置失败";
    case "automation_busy":
      return "触发跳过";
  }
}
