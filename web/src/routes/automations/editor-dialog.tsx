import { useEffect, useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import {
  OptionSelect,
  withMissingOption,
} from "@/components/ui/option-select";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { environmentApi, threadApi } from "@/lib/api/threads";
import { useProjects } from "@/lib/hooks/use-projects";
import {
  type Automation,
  type AutomationInput,
  type AutomationModelSelection,
  type AutomationTriggerType,
  type RuntimeMode,
} from "@/lib/api/automations";
import {
  buildModelSelection,
  parseProviders,
  resolveModel,
} from "@/lib/threads-compose";
import { CronScheduleFields } from "./cron-editor";

// 自动化新建/编辑弹窗（INT-90）：选项常量与表单控件集中在此文件。

export const browserTimezone = () => Intl.DateTimeFormat().resolvedOptions().timeZone;

const TRIGGER_TYPE_OPTIONS = [
  { value: "cron", label: "定时（cron）" },
  { value: "issue_created", label: "Issue 新建" },
  { value: "issue_status_changed", label: "Issue 状态变化" },
  { value: "issue_label_added", label: "Issue 加标签" },
  { value: "github_pr_opened", label: "GitHub PR 打开" },
  { value: "github_pr_synchronize", label: "GitHub PR 新提交" },
];

export const isPRTrigger = (t: AutomationTriggerType) =>
  t === "github_pr_opened" || t === "github_pr_synchronize";

const sameModel = (a: AutomationModelSelection, b: AutomationModelSelection) =>
  a.instance_id === b.instance_id && a.model === b.model;

const TIMEOUT_OPTIONS = [
  { value: "300", label: "5 分钟" },
  { value: "900", label: "15 分钟" },
  { value: "3600", label: "1 小时（默认）" },
  { value: "14400", label: "4 小时" },
  { value: "86400", label: "24 小时" },
];

const RUNTIME_MODE_OPTIONS = [
  { value: "full-access", label: "full-access（默认）" },
  { value: "auto", label: "auto" },
  { value: "auto-accept-edits", label: "auto-accept-edits" },
  { value: "approval-required", label: "approval-required" },
];

const POST_ACTION_OPTIONS = [
  { value: "none", label: "无（默认）" },
  { value: "settle", label: "settle（收进历史区）" },
  { value: "archive", label: "archive（归档）" },
];

const POST_ON_TIMEOUT_OPTIONS = [
  { value: "no", label: "否（默认）" },
  { value: "yes", label: "是" },
];

const ENABLED_OPTIONS = [
  { value: "off", label: "停用（默认）" },
  { value: "on", label: "启用" },
];

// "" 在 Base UI 里等于未选中，「任意」这种全放行选项要用哨兵值。
const EVENT_FIELD_ANY = "__any__";
const ALL_PROJECTS = "__all__";
const EVENT_FIELD_OPTIONS = [
  { value: EVENT_FIELD_ANY, label: "任意" },
  { value: "state", label: "state（开关状态）" },
  { value: "workflow_status", label: "workflow_status" },
];

function toAutomationModelSelection(
  sel: ReturnType<typeof buildModelSelection>,
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

// ---- 新建/编辑 ----

export function AutomationEditorDialog({
  open,
  editing,
  form,
  onFormChange,
  onClose,
  onSubmit,
  submitting,
}: {
  open: boolean;
  editing: Automation | null;
  form: AutomationInput;
  onFormChange: (form: AutomationInput) => void;
  onClose: () => void;
  onSubmit: () => void;
  submitting: boolean;
}) {
  const set = <K extends keyof AutomationInput>(key: K, value: AutomationInput[K]) =>
    onFormChange({ ...form, [key]: value });

  // toggleModelSelection PR 触发的多模型勾选：(instance_id, model) 不可重复。
  const toggleModelSelection = (sel: AutomationModelSelection) => {
    const list = form.model_selections ?? [];
    if (list.some((s) => sameModel(s, sel))) {
      set("model_selections", list.filter((s) => !sameModel(s, sel)));
      return;
    }
    if (list.length >= 10) {
      toast.error("最多选择 10 个模型");
      return;
    }
    set("model_selections", [...list, sel]);
  };

  const {
    data: environments = [],
    isLoading: environmentsLoading,
    isError: environmentsError,
  } = useQuery({
    queryKey: ["automations", "env-options"],
    queryFn: async () => (await environmentApi.list()).data.items,
    enabled: open,
  });

  const {
    data: composer,
    isLoading: composerLoading,
    isError: composerError,
  } = useQuery({
    queryKey: ["automations", "composer", form.environment_id],
    queryFn: async () =>
      (
        await threadApi.composerOptions(form.environment_id)
      ).data,
    enabled: open && Boolean(form.environment_id),
  });

  const { data: workspaceOpts } = useQuery({
    queryKey: ["automations", "workspace", form.environment_id, form.t3_project_id],
    queryFn: async () =>
      (await threadApi.workspaceOptions(form.environment_id, form.t3_project_id, "")).data,
    enabled: open && Boolean(form.environment_id && form.t3_project_id),
  });

  const projects = composer?.projects ?? [];
  const providers = useMemo(
    () => parseProviders(composer?.config),
    [composer?.config],
  );
  const t3Project = projects.find((p) => p.id === form.t3_project_id);
  const modelKey = form.model_selection
    ? `${form.model_selection.instance_id}::${form.model_selection.model}`
    : "default";
  const workspaceKind = form.workspace?.type ?? "root";
  const canWorktree = workspaceOpts?.is_repo !== false && Boolean(workspaceOpts?.ref_name);

  useEffect(() => {
    if (form.workspace?.type !== "worktree") return;
    const ref = workspaceOpts?.ref_name?.trim();
    if (!ref || form.workspace.base_ref === ref) return;
    if (form.workspace.base_ref) return;
    onFormChange({
      ...form,
      workspace: { ...form.workspace, base_ref: ref },
    });
  }, [form, onFormChange, workspaceOpts?.ref_name]);

  const filter = form.event_filter ?? {};

  return (
    <Dialog open={open} onOpenChange={(next) => !next && onClose()}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{editing ? "编辑自动化" : "新建自动化"}</DialogTitle>
          <DialogDescription>
            {editing
              ? "更新只影响后续触发，进行中的运行按原配置执行。"
              : "新建默认停用；启用前需在服务端配置 public_url。"}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-2">
          <div className="space-y-2">
            <Label htmlFor="automation-name">名称</Label>
            <Input
              id="automation-name"
              value={form.name}
              placeholder="例如 每日令牌回报"
              onChange={(e) => set("name", e.target.value)}
            />
          </div>

          <div className="space-y-2">
            <Label>触发方式</Label>
            <OptionSelect
              value={form.trigger_type}
              onValueChange={(next) => {
                const trigger = (next ?? form.trigger_type) as AutomationTriggerType;
                onFormChange({
                  ...form,
                  trigger_type: trigger,
                  // 触发通道切换：清理互不兼容的模型选择形态。
                  model_selection: isPRTrigger(trigger)
                    ? null
                    : form.model_selection,
                  model_selections: isPRTrigger(trigger)
                    ? (form.model_selections ?? [])
                    : [],
                  debounce_seconds: isPRTrigger(trigger)
                    ? (form.debounce_seconds ?? 60)
                    : 0,
                  event_filter: isPRTrigger(trigger) ? null : form.event_filter,
                });
              }}
              options={TRIGGER_TYPE_OPTIONS}
              className="w-full"
            />
          </div>

          {isPRTrigger(form.trigger_type) && (
            <div className="space-y-2">
              <Label htmlFor="automation-debounce">防抖窗口（秒）</Label>
              <Input
                id="automation-debounce"
                type="number"
                min={0}
                max={86400}
                value={form.debounce_seconds ?? 60}
                onChange={(e) =>
                  set("debounce_seconds", Number(e.target.value))
                }
              />
              <p className="text-xs text-muted-foreground">
                窗口内新推送会重置计时，到点只审查最新提交；0 表示立即开审。默认 60。
              </p>
            </div>
          )}

          {form.trigger_type === "cron" ? (
            <CronScheduleFields
              expression={form.cron_expression ?? ""}
              timezone={form.timezone ?? ""}
              onChange={(expr, tz) =>
                onFormChange({ ...form, cron_expression: expr, timezone: tz })
              }
            />
          ) : isPRTrigger(form.trigger_type) ? (
            <p className="text-xs text-muted-foreground">
              GitHub PR 触发不做事件字段过滤：仓库范围由 GitHub App 安装与项目关联决定，
              draft 与 fork 不审查。
            </p>
          ) : (
            <EventFilterFields
              triggerType={form.trigger_type}
              filter={filter}
              onChange={(next) => set("event_filter", next)}
            />
          )}

          {form.trigger_type !== "cron" && (
            <div className="space-y-2">
              <Label>限定 Fast Ship 项目（可选）</Label>
              <IssueProjectSelect
                value={form.issue_project_id ?? ""}
                onChange={(next) =>
                  set("issue_project_id", next === "" ? null : next)
                }
              />
              <p className="text-xs text-muted-foreground">
                留空表示当前用户全部项目的事件都触发。
              </p>
            </div>
          )}

          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-2">
              <Label>T3 环境</Label>
              <OptionSelect
                value={form.environment_id}
                onValueChange={(next) =>
                  onFormChange({
                    ...form,
                    environment_id: next ?? "",
                    t3_project_id: "",
                    model_selection: null,
                    model_selections: [],
                    workspace: null,
                  })
                }
                options={withMissingOption(
                  environments.map((env) => ({
                    value: env.id,
                    label: env.name,
                  })),
                  form.environment_id,
                  environmentsLoading
                    ? "加载中…"
                    : environmentsError
                      ? "加载失败，请重试"
                      : `${form.environment_id}（已失效）`,
                )}
                placeholder="选择环境"
                className="w-full"
              />
            </div>
            <div className="space-y-2">
              <Label>T3 项目</Label>
              <OptionSelect
                value={form.t3_project_id}
                onValueChange={(next) =>
                  onFormChange({
                    ...form,
                    t3_project_id: next ?? "",
                    model_selection: null,
                    model_selections: [],
                    workspace: null,
                  })
                }
                options={withMissingOption(
                  projects.map((project) => ({
                    value: project.id,
                    label: project.title,
                  })),
                  form.t3_project_id,
                  composerLoading
                    ? "加载中…"
                    : composerError
                      ? "加载失败，请重试"
                      : `${form.t3_project_id}（已失效）`,
                )}
                placeholder="选择项目"
                className="w-full"
              />
            </div>
          </div>

          <div className="grid grid-cols-2 gap-3">
            {isPRTrigger(form.trigger_type) ? (
              <div className="space-y-2">
                <Label>模型（可多选，每个模型各开一个运行）</Label>
                <div className="max-h-40 space-y-1 overflow-y-auto rounded-md border p-2">
                  {providers.flatMap((provider) =>
                    provider.models.map((model) => {
                      const resolved = resolveModel(providers, t3Project, {
                        instanceId: provider.instanceId,
                        model: model.slug,
                        efforts: {},
                      });
                      const sel =
                        toAutomationModelSelection(buildModelSelection(resolved)) ?? {
                          instance_id: provider.instanceId,
                          model: model.slug,
                        };
                      const checked = (form.model_selections ?? []).some(
                        (s) => sameModel(s, sel),
                      );
                      return (
                        <label
                          key={`${provider.instanceId}::${model.slug}`}
                          className="flex cursor-pointer items-center gap-2 rounded px-1 py-0.5 text-sm hover:bg-muted/60"
                        >
                          <input
                            type="checkbox"
                            checked={checked}
                            onChange={() => toggleModelSelection(sel)}
                          />
                          <span>
                            {provider.name} / {model.name}
                          </span>
                        </label>
                      );
                    }),
                  )}
                  {!providers.length && (
                    <p className="px-1 py-0.5 text-xs text-muted-foreground">
                      选择 T3 环境与项目后展示可用模型。
                    </p>
                  )}
                </div>
                <p className="text-xs text-muted-foreground">
                  必须显式选择（不能「项目默认」）；一次触发每个模型并行开一个运行。
                </p>
              </div>
            ) : (
            <div className="space-y-2">
              <Label>模型</Label>
              <OptionSelect
                value={modelKey}
                onValueChange={(next) => {
                  if (!next || next === "default") {
                    set("model_selection", null);
                    return;
                  }
                  const sep = next.indexOf("::");
                  const instanceId = sep >= 0 ? next.slice(0, sep) : next;
                  const model = sep >= 0 ? next.slice(sep + 2) : "";
                  const resolved = resolveModel(providers, t3Project, {
                    instanceId,
                    model,
                    efforts: {},
                  });
                  set(
                    "model_selection",
                    toAutomationModelSelection(buildModelSelection(resolved)),
                  );
                }}
                options={withMissingOption(
                  [
                    { value: "default", label: "项目默认" },
                    ...providers.flatMap((provider) =>
                      provider.models.map((model) => ({
                        value: `${provider.instanceId}::${model.slug}`,
                        label: `${provider.name} / ${model.name}`,
                      })),
                    ),
                  ],
                  modelKey,
                  composerLoading
                    ? "加载中…"
                    : composerError
                      ? "加载失败，请重试"
                      : `${modelKey}（已失效）`,
                )}
                placeholder="项目默认"
                className="w-full"
              />
              <p className="text-xs text-muted-foreground">
                选项来自该环境 composer-options；留空用 T3 项目默认模型。
              </p>
            </div>
            )}
            <div className="space-y-2">
              <Label>工作区</Label>
              <OptionSelect
                value={workspaceKind}
                onValueChange={(next) => {
                  if (next === "worktree") {
                    set("workspace", {
                      type: "worktree",
                      base_ref: workspaceOpts?.ref_name ?? "",
                    });
                    return;
                  }
                  if (next === "existing_worktree") {
                    return;
                  }
                  set("workspace", null);
                }}
                options={[
                  { value: "root", label: "当前检出（默认）" },
                  {
                    value: "worktree",
                    label: "新建 worktree",
                    disabled: !canWorktree,
                  },
                  ...(workspaceKind === "existing_worktree"
                    ? [{ value: "existing_worktree", label: "已有 worktree" }]
                    : []),
                ]}
                className="w-full"
              />
              <p className="text-xs text-muted-foreground">
                {workspaceKind === "worktree"
                  ? `基线分支 ${form.workspace?.base_ref || "（等待仓库信息）"}`
                  : "留空按项目当前检出启动线程。"}
              </p>
            </div>
          </div>

          <div className="space-y-2">
            <Label htmlFor="automation-prompt">提示词模板</Label>
            <Textarea
              id="automation-prompt"
              rows={5}
              value={form.prompt_template}
              placeholder="支持占位符 {{issue_id}}、{{issue_title}}、{{issue_body}}；事件触发时替换。"
              onChange={(e) => set("prompt_template", e.target.value)}
            />
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-2">
              <Label>超时时长</Label>
              <OptionSelect
                value={String(form.timeout_seconds ?? 3600)}
                onValueChange={(next) => set("timeout_seconds", Number(next))}
                options={withMissingOption(
                  TIMEOUT_OPTIONS,
                  String(form.timeout_seconds ?? 3600),
                  `${form.timeout_seconds} 秒`,
                )}
                className="w-full"
              />
            </div>
            <div className="space-y-2">
              <Label>运行模式</Label>
              <OptionSelect
                value={form.runtime_mode ?? "full-access"}
                onValueChange={(next) => set("runtime_mode", next as RuntimeMode)}
                options={withMissingOption(
                  RUNTIME_MODE_OPTIONS,
                  form.runtime_mode ?? "full-access",
                  `${form.runtime_mode}（未知）`,
                )}
                className="w-full"
              />
            </div>
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-2">
              <Label>结束后置操作</Label>
              <OptionSelect
                value={form.post_action ?? "none"}
                onValueChange={(next) =>
                  set("post_action", next as AutomationInput["post_action"])
                }
                options={POST_ACTION_OPTIONS}
                className="w-full"
              />
              {form.post_action === "archive" && (
                <p className="text-xs text-muted-foreground">
                  archive 会终止仍在活动的线程。
                </p>
              )}
            </div>
            <div className="space-y-2">
              <Label>超时后仍执行后置</Label>
              <OptionSelect
                value={form.post_on_timeout ? "yes" : "no"}
                onValueChange={(next) => set("post_on_timeout", next === "yes")}
                options={POST_ON_TIMEOUT_OPTIONS}
                className="w-full"
              />
            </div>
          </div>

          <div className="space-y-2">
            <Label>启用状态</Label>
            <OptionSelect
              value={form.enabled ? "on" : "off"}
              onValueChange={(next) => set("enabled", next === "on")}
              options={ENABLED_OPTIONS}
              className="w-full"
            />
          </div>
        </div>

        <DialogFooter>
          <Button type="button" variant="outline" onClick={onClose}>
            取消
          </Button>
          <Button type="button" onClick={onSubmit} disabled={submitting}>
            {submitting ? "保存中..." : "保存"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// EventFilterFields 事件过滤的可选输入：无过滤 = 该类型全部真实事件。
function EventFilterFields({
  triggerType,
  filter,
  onChange,
}: {
  triggerType: AutomationTriggerType;
  filter: NonNullable<AutomationInput["event_filter"]>;
  onChange: (filter: AutomationInput["event_filter"]) => void;
}) {
  if (triggerType === "issue_created") {
    return (
      <p className="text-xs text-muted-foreground">
        Issue 新建事件不做事件字段过滤；项目范围用下方「限定 Fast Ship 项目」控制。
      </p>
    );
  }
  if (triggerType === "issue_status_changed") {
    return (
      <div className="grid grid-cols-3 gap-3">
        <div className="space-y-2">
          <Label>字段</Label>
          <OptionSelect
            value={filter.field ?? EVENT_FIELD_ANY}
            onValueChange={(next) =>
              onChange({
                ...filter,
                field:
                  !next || next === EVENT_FIELD_ANY
                    ? undefined
                    : (next as "state" | "workflow_status"),
              })
            }
            options={withMissingOption(
              EVENT_FIELD_OPTIONS,
              filter.field ?? EVENT_FIELD_ANY,
              `${filter.field}（未知）`,
            )}
            className="w-full"
          />
        </div>
        <div className="space-y-2">
          <Label>从（可选）</Label>
          <Input
            value={filter.from ?? ""}
            placeholder="open / todo"
            onChange={(e) => onChange({ ...filter, from: e.target.value })}
          />
        </div>
        <div className="space-y-2">
          <Label>到（可选）</Label>
          <Input
            value={filter.to ?? ""}
            placeholder="closed / done"
            onChange={(e) => onChange({ ...filter, to: e.target.value })}
          />
        </div>
      </div>
    );
  }
  return (
    <div className="space-y-2">
      <Label htmlFor="automation-labels">匹配标签（可选，逗号分隔）</Label>
      <Input
        id="automation-labels"
        value={(filter.labels ?? []).join(", ")}
        placeholder="留空匹配全部加标签事件"
        onChange={(e) =>
          onChange({
            ...filter,
            labels: e.target.value
              .split(",")
              .map((s) => s.trim())
              .filter(Boolean),
          })
        }
      />
    </div>
  );
}

// IssueProjectSelect 事件触发的可选项目范围（不依赖 INT-89 项目绑定）。
function IssueProjectSelect({
  value,
  onChange,
}: {
  value: string;
  onChange: (value: string) => void;
}) {
  const { data: projects, isLoading, isError } = useProjects();
  const items = projects?.items ?? [];
  return (
    <OptionSelect
      value={value || ALL_PROJECTS}
      onValueChange={(next) => onChange(!next || next === ALL_PROJECTS ? "" : next)}
      options={withMissingOption(
        [
          { value: ALL_PROJECTS, label: "全部项目" },
          ...items.map((project) => ({ value: project.id, label: project.name })),
        ],
        value,
        isLoading
          ? "加载中…"
          : isError
            ? "加载失败，请重试"
            : `${value}（已失效）`,
      )}
      className="w-full"
    />
  );
}

