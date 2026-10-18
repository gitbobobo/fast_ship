import {
  automationTriggerLabel,
  type Automation,
  type AutomationEventFilter,
  type AutomationInput,
  type AutomationModelSelection,
  type AutomationTrigger,
  type AutomationTriggerInput,
  type AutomationTriggerType,
  type AutomationWorkspace,
  type RuntimeMode,
} from "@/lib/api/automations";
import type { ModelSelection } from "@/lib/api/threads";
import { describeCronExpression, uiLocale } from "@/lib/cron-frequency";
import { uuid } from "@/lib/uuid";

// 自动化编辑页的表单模型与共享助手（INT-118）。
// 触发器在表单里是 TriggerForm 数组（key 是本地 React key，id 是服务端 id），
// 保存时按类型裁剪成 AutomationTriggerInput（非本类型字段清空）。

export const browserTimezone = () =>
  Intl.DateTimeFormat().resolvedOptions().timeZone;

export const isPRTrigger = (t: AutomationTriggerType) =>
  t === "github_pr_opened" || t === "github_pr_synchronize";

export const isIssueTrigger = (t: AutomationTriggerType) =>
  t === "issue_created" ||
  t === "issue_status_changed" ||
  t === "issue_label_added";

export const TRIGGER_TYPE_OPTIONS: {
  value: AutomationTriggerType;
  label: string;
}[] = [
  { value: "cron", label: "定时（cron）" },
  { value: "issue_created", label: "Issue 新建" },
  { value: "issue_status_changed", label: "Issue 状态变化" },
  { value: "issue_label_added", label: "Issue 加标签" },
  { value: "github_pr_opened", label: "GitHub PR 打开" },
  { value: "github_pr_synchronize", label: "GitHub PR 新提交" },
];

export const MAX_TRIGGERS = 20;

export const TIMEOUT_OPTIONS = [
  { value: "300", label: "5 分钟" },
  { value: "900", label: "15 分钟" },
  { value: "3600", label: "1 小时（默认）" },
  { value: "14400", label: "4 小时" },
  { value: "86400", label: "24 小时" },
];

export const RUNTIME_MODE_OPTIONS = [
  { value: "full-access", label: "full-access（默认）" },
  { value: "auto", label: "auto" },
  { value: "auto-accept-edits", label: "auto-accept-edits" },
  { value: "approval-required", label: "approval-required" },
];

export const POST_ACTION_OPTIONS = [
  { value: "none", label: "无（默认）" },
  { value: "settle", label: "settle（收进历史区）" },
  { value: "archive", label: "archive（归档）" },
];

export const POST_ON_TIMEOUT_OPTIONS = [
  { value: "no", label: "否（默认）" },
  { value: "yes", label: "是" },
];

// "" 在 Base UI 里等于未选中，「任意」这种全放行选项要用哨兵值。
export const EVENT_FIELD_ANY = "__any__";
export const ALL_PROJECTS = "__all__";
export const EVENT_FIELD_OPTIONS = [
  { value: EVENT_FIELD_ANY, label: "任意" },
  { value: "state", label: "state（开关状态）" },
  { value: "workflow_status", label: "workflow_status" },
];

export const sameModel = (
  a: AutomationModelSelection,
  b: AutomationModelSelection,
) => a.instance_id === b.instance_id && a.model === b.model;

export function toAutomationModelSelection(
  sel: ModelSelection | undefined,
): AutomationModelSelection | null {
  if (!sel) return null;
  const options = sel.options
    ?.filter((o) => typeof o.value === "string")
    .map((o) => ({ id: o.id, value: String(o.value) }));
  return {
    instance_id: sel.instance_id,
    model: sel.model,
    ...(options?.length ? { options } : {}),
  };
}

// ---- 表单形状 ----

export interface TriggerForm {
  /** 本地 React key（新建用 uuid）；不等于服务端 id。 */
  key: string;
  /** 服务端触发器 id：编辑时原样回传，服务端据此更新而不是重建。 */
  id?: string;
  trigger_type: AutomationTriggerType;
  cron_expression: string;
  timezone: string;
  issue_project_id: string | null;
  event_filter: AutomationEventFilter | null;
  model_selections: AutomationModelSelection[];
  debounce_seconds: number;
}

export interface AutomationForm {
  name: string;
  enabled: boolean;
  environment_id: string;
  t3_project_id: string;
  triggers: TriggerForm[];
  prompt_template: string;
  model_selection: AutomationModelSelection | null;
  runtime_mode: RuntimeMode;
  workspace: AutomationWorkspace | null;
  timeout_seconds: number;
  post_action: AutomationInput["post_action"];
  post_on_timeout: boolean;
}

// 新触发器的类型默认：cron 带「每天 09:00」+ 浏览器时区，PR 防抖 60s。
export function emptyTriggerForm(type: AutomationTriggerType): TriggerForm {
  return {
    key: uuid(),
    trigger_type: type,
    cron_expression: type === "cron" ? "0 9 * * *" : "",
    timezone: type === "cron" ? browserTimezone() : "",
    issue_project_id: null,
    event_filter: null,
    model_selections: [],
    debounce_seconds: 60,
  };
}

export function emptyAutomationForm(): AutomationForm {
  return {
    name: "",
    enabled: false,
    environment_id: "",
    t3_project_id: "",
    triggers: [],
    prompt_template: "",
    model_selection: null,
    runtime_mode: "full-access",
    workspace: null,
    timeout_seconds: 3600,
    post_action: "none",
    post_on_timeout: false,
  };
}

export function triggerToForm(t: AutomationTrigger): TriggerForm {
  return {
    key: t.id,
    id: t.id,
    trigger_type: t.trigger_type,
    cron_expression: t.cron_expression ?? "",
    timezone: t.timezone ?? "",
    issue_project_id: t.issue_project_id ?? null,
    event_filter: t.event_filter ?? null,
    model_selections: t.model_selections ?? [],
    debounce_seconds: t.debounce_seconds ?? 60,
  };
}

export function automationToForm(a: Automation): AutomationForm {
  return {
    name: a.name,
    enabled: a.enabled,
    environment_id: a.environment_id,
    t3_project_id: a.t3_project_id,
    triggers: (a.triggers ?? []).map(triggerToForm),
    prompt_template: a.prompt_template,
    model_selection: a.model_selection ?? null,
    runtime_mode: (a.runtime_mode as RuntimeMode) || "full-access",
    workspace: a.workspace ?? null,
    timeout_seconds: a.timeout_seconds,
    post_action: a.post_action,
    post_on_timeout: a.post_on_timeout,
  };
}

// 表单 → 请求体：只携带该类型自己的字段，其余清空（与服务端全量替换语义对齐）。
export function triggerToInput(t: TriggerForm): AutomationTriggerInput {
  const input: AutomationTriggerInput = {
    trigger_type: t.trigger_type,
    cron_expression: null,
    timezone: null,
    issue_project_id: null,
    event_filter: null,
    model_selections: [],
    debounce_seconds: 0,
  };
  if (t.id) input.id = t.id;
  if (t.trigger_type === "cron") {
    input.cron_expression = t.cron_expression;
    input.timezone = t.timezone;
  } else {
    input.issue_project_id = t.issue_project_id;
    if (isPRTrigger(t.trigger_type)) {
      input.model_selections = t.model_selections;
      input.debounce_seconds = t.debounce_seconds;
    } else {
      input.event_filter = t.event_filter;
    }
  }
  return input;
}

export function automationFormToInput(form: AutomationForm): AutomationInput {
  return {
    name: form.name,
    enabled: form.enabled,
    environment_id: form.environment_id,
    t3_project_id: form.t3_project_id,
    triggers: form.triggers.map(triggerToInput),
    prompt_template: form.prompt_template,
    model_selection: form.model_selection,
    runtime_mode: form.runtime_mode,
    workspace: form.workspace,
    timeout_seconds: form.timeout_seconds,
    post_action: form.post_action,
    post_on_timeout: form.post_on_timeout,
  };
}

// 记录 → 全量 PUT 请求体：由响应记录反向构造（触发器 id 原样回传）。
// 启停（useToggleAutomation）就是先 GET 最新详情、再套这套裁剪后 PUT。
export function automationRecordToInput(a: Automation): AutomationInput {
  return automationFormToInput(automationToForm(a));
}

// ---- 展示摘要 ----

// 单个触发器的摘要（列表「触发」列与触发器卡片收起态共用）。
export function triggerSummary(t: {
  trigger_type: AutomationTriggerType;
  cron_expression?: string | null;
  timezone?: string | null;
  model_selections?: AutomationModelSelection[];
  debounce_seconds?: number;
}): string {
  const label = automationTriggerLabel(t.trigger_type);
  if (t.trigger_type === "cron") {
    const desc = describeCronExpression(
      t.cron_expression ?? "",
      t.timezone ?? "",
      uiLocale(),
    );
    // 描述不出来（表达式损坏等）时至少展示原表达式。
    return desc
      ? `${label} · ${desc}`
      : `${label} · ${t.cron_expression ?? ""}`;
  }
  if (isPRTrigger(t.trigger_type)) {
    const models = (t.model_selections ?? []).map((m) => m.model).join("、");
    const debounce = t.debounce_seconds ?? 60;
    return `${label}${models ? ` · ${models}` : ""} · 防抖 ${debounce}s`;
  }
  return label;
}

// 列表「触发」列：无触发器显示「未配置」，多个显示首个 + 「等 N 个」。
export function automationTriggersSummary(a: Automation): string {
  const triggers = a.triggers ?? [];
  if (!triggers.length) return "未配置";
  const first = triggerSummary(triggers[0]);
  return triggers.length > 1 ? `${first} 等 ${triggers.length} 个` : first;
}

// 「下次运行」取所有 cron 触发器里最早的非空 next_run_at。
export function earliestNextRunAt(a: Automation): string | null {
  const times = (a.triggers ?? [])
    .filter((t) => t.trigger_type === "cron" && t.next_run_at)
    .map((t) => t.next_run_at as string);
  return times.sort()[0] ?? null;
}
