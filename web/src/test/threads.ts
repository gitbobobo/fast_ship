import type {
  Environment,
  ThreadActivity,
  ThreadQueue,
} from "@/lib/api/threads";

export function makeEnvironment(over: Partial<Environment> = {}): Environment {
  return {
    id: "e1",
    name: "Mac",
    address: "http://mac:3773",
    status: "ok",
    scopes: ["orchestration:read", "orchestration:operate"],
    access: "operate",
    updated_at: "2026-10-13T10:00:00Z",
    ...over,
  };
}

export function makeQueue(over: Partial<ThreadQueue> = {}): ThreadQueue {
  return { held: false, can_reorder: false, can_steer: false, items: [], ...over };
}

export function makeActivity(over: Partial<ThreadActivity> = {}): ThreadActivity {
  return {
    stopping: false,
    has_pending_requests: false,
    pending_background_task_count: 0,
    ...over,
  };
}
