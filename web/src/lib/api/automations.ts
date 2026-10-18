import { api } from "./client";

// 自动化（INT-90）前端类型：与 server/api/openapi.yaml 的 schemas 对应。

export type AutomationTriggerType =
  | "cron"
  | "issue_created"
  | "issue_status_changed"
  | "issue_label_added"
  | "github_pr_opened"
  | "github_pr_synchronize";

export type AutomationPostAction = "none" | "settle" | "archive";

export type RuntimeMode =
  | "approval-required"
  | "auto-accept-edits"
  | "auto"
  | "full-access";

export interface AutomationEventFilter {
  field?: "state" | "workflow_status";
  from?: string;
  to?: string;
  labels?: string[];
}

export interface AutomationModelSelection {
  instance_id: string;
  model: string;
  options?: { id: string; value: string }[];
}

export interface AutomationWorkspace {
  type: "root" | "worktree" | "existing_worktree";
  base_ref?: string;
  branch?: string;
  start_from_origin?: boolean;
  worktree_path?: string;
}

// 自动化触发器（INT-118）：一条自动化可挂多个触发器，数组顺序即 position。
// 各类型只用自己的字段：cron 用 cron_expression/timezone/next_run_at，
// Issue 事件用 event_filter，PR 用 model_selections/debounce_seconds，
// 非 cron 触发都可配 issue_project_id 限定项目范围。
export interface AutomationTrigger {
  id: string;
  trigger_type: AutomationTriggerType;
  cron_expression?: string | null;
  timezone?: string | null;
  // 仅 cron，RFC3339 UTC；服务端每次保存重算。
  next_run_at?: string | null;
  issue_project_id?: string | null;
  event_filter?: AutomationEventFilter | null;
  // GitHub PR 触发的多模型选择（每个模型各开一个运行）。
  model_selections?: AutomationModelSelection[];
  debounce_seconds?: number;
  created_at: string;
}

// 保存时的触发器输入：带 id 表示更新原有触发器，不带表示新建。
export interface AutomationTriggerInput {
  id?: string;
  trigger_type: AutomationTriggerType;
  cron_expression?: string | null;
  timezone?: string | null;
  issue_project_id?: string | null;
  event_filter?: AutomationEventFilter | null;
  model_selections?: AutomationModelSelection[];
  debounce_seconds?: number;
}

export interface Automation {
  id: string;
  name: string;
  enabled: boolean;
  environment_id: string;
  t3_project_id: string;
  // 触发器列表（数组顺序 = position），可为空数组。
  triggers: AutomationTrigger[];
  prompt_template: string;
  // cron / Issue 事件触发的单模型缺省（PR 触发器的模型在各触发器上）。
  model_selection?: AutomationModelSelection | null;
  runtime_mode: string;
  workspace?: AutomationWorkspace | null;
  timeout_seconds: number;
  post_action: AutomationPostAction;
  post_on_timeout: boolean;
  created_at: string;
  updated_at: string;
}

export type AutomationRunStatus =
  | "queued"
  | "dispatched"
  | "running"
  | "completed"
  | "failed"
  | "timed_out"
  | "cancelled";

export type AutomationPostState = "none" | "pending" | "succeeded" | "failed";

export interface AutomationRun {
  id: string;
  automation_id: string;
  // 产生本次运行的触发器 id（INT-118）。
  trigger_id: string;
  trigger_type: AutomationTriggerType;
  trigger_key: string;
  trigger_context?: {
    project_id?: string;
    issue_id?: string;
    issue_sequence_number?: number;
    issue_title?: string;
    field?: string;
    from?: string;
    to?: string;
    labels_added?: string[];
  } | null;
  triggered_at: string;
  status: string | null;
  environment_id: string;
  t3_project_id: string;
  thread_id: string;
  // GitHub PR 触发的运行上下文。
  repo_full_name?: string | null;
  pr_number?: number | null;
  head_sha?: string | null;
  round_number?: number | null;
  model_instance_id?: string | null;
  model_name?: string | null;
  dispatch_started_at?: string | null;
  dispatched_at?: string | null;
  started_at?: string | null;
  finished_at?: string | null;
  deadline_at: string;
  error_code?: string | null;
  error_message?: string | null;
  summary?: string | null;
  post_state: AutomationPostState;
  post_attempt: number;
  post_next_attempt_at?: string | null;
  post_deadline_at?: string | null;
  post_error?: string | null;
}

export interface AutomationInput {
  name: string;
  enabled: boolean;
  environment_id: string;
  t3_project_id: string;
  // 触发器全量替换：0–20 个；启用时服务端要求至少 1 个。
  triggers: AutomationTriggerInput[];
  prompt_template: string;
  model_selection?: AutomationModelSelection | null;
  runtime_mode?: RuntimeMode;
  workspace?: AutomationWorkspace | null;
  timeout_seconds?: number;
  post_action?: AutomationPostAction;
  post_on_timeout?: boolean;
}

export const automationApi = {
  list: () => api.get("automations").json<ApiResponse<{ items: Automation[] }>>(),
  get: (id: string) =>
    api.get(`automations/${id}`).json<ApiResponse<Automation>>(),
  create: (data: AutomationInput) =>
    api.post("automations", { json: data }).json<ApiResponse<Automation>>(),
  update: (id: string, data: AutomationInput) =>
    api.put(`automations/${id}`, { json: data }).json<ApiResponse<Automation>>(),
  remove: (id: string) =>
    api.delete(`automations/${id}`).json<ApiResponse<null>>(),
  listRuns: (id: string) =>
    api
      .get(`automations/${id}/runs`)
      .json<ApiResponse<{ items: AutomationRun[] }>>(),
  getRun: (id: string) =>
    api.get(`automation-runs/${id}`).json<ApiResponse<AutomationRun>>(),
};

export function automationTriggerLabel(type: AutomationTriggerType): string {
  switch (type) {
    case "cron":
      return "定时（cron）";
    case "issue_created":
      return "Issue 新建";
    case "issue_status_changed":
      return "Issue 状态变化";
    case "issue_label_added":
      return "Issue 加标签";
    case "github_pr_opened":
      return "GitHub PR 打开";
    case "github_pr_synchronize":
      return "GitHub PR 新提交";
  }
}

export function automationRunStatusLabel(status: string | null): string {
  switch (status) {
    case null:
    case "":
      return "待派发";
    case "queued":
      return "排队中";
    case "dispatched":
      return "已派发";
    case "running":
      return "运行中";
    case "completed":
      return "已完成";
    case "failed":
      return "已失败";
    case "timed_out":
      return "已超时";
    case "cancelled":
      return "已取消";
    default:
      return status;
  }
}

export function automationPostStateLabel(state: AutomationPostState): string {
  switch (state) {
    case "none":
      return "—";
    case "pending":
      return "待执行";
    case "succeeded":
      return "已执行";
    case "failed":
      return "失败";
  }
}
