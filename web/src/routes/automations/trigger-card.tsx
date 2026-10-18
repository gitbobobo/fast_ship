import { useState } from "react";
import { ChevronRight, Trash2 } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  OptionSelect,
  withMissingOption,
} from "@/components/ui/option-select";
import { cn } from "@/lib/utils";
import {
  automationTriggerLabel,
  type AutomationEventFilter,
  type AutomationModelSelection,
  type AutomationTriggerType,
} from "@/lib/api/automations";
import type { ComposerProject } from "@/lib/api/threads";
import {
  resolveModel,
  buildModelSelection,
  type ProviderChoice,
} from "@/lib/threads-compose";
import { ModelMultiPicker } from "@/components/threads/model-picker";
import { useProjects } from "@/lib/hooks/use-projects";
import { CronScheduleFields } from "./cron-editor";
import {
  ALL_PROJECTS,
  EVENT_FIELD_ANY,
  EVENT_FIELD_OPTIONS,
  isIssueTrigger,
  isPRTrigger,
  sameModel,
  toAutomationModelSelection,
  triggerSummary,
  type TriggerForm,
} from "@/lib/automations/form-model";

// 单个触发器的编辑卡片（INT-118）：头部 = 类型名 + 摘要 + 删除，
// 展开后是该类型专属字段；新建的触发器默认展开。

export function TriggerCard({
  trigger,
  providers,
  t3Project,
  onChange,
  onRemove,
}: {
  trigger: TriggerForm;
  providers: ProviderChoice[];
  t3Project: ComposerProject | undefined;
  onChange: (next: TriggerForm) => void;
  onRemove: () => void;
}) {
  // 已有触发器（带服务端 id）默认收起，新添加的默认展开。
  const [expanded, setExpanded] = useState(() => !trigger.id);
  const label = automationTriggerLabel(trigger.trigger_type);
  const pr = isPRTrigger(trigger.trigger_type);

  const toggleModelSelection = (sel: AutomationModelSelection) => {
    const list = trigger.model_selections;
    if (list.some((s) => sameModel(s, sel))) {
      onChange({
        ...trigger,
        model_selections: list.filter((s) => !sameModel(s, sel)),
      });
      return;
    }
    if (list.length >= 10) {
      toast.error("最多选择 10 个模型");
      return;
    }
    onChange({ ...trigger, model_selections: [...list, sel] });
  };

  return (
    <div className="rounded-lg border" data-slot="trigger-card">
      <div className="flex items-center gap-1.5 px-2 py-1.5">
        <button
          type="button"
          aria-expanded={expanded}
          aria-label={`${expanded ? "收起" : "展开"}触发器 ${label}`}
          onClick={() => setExpanded((v) => !v)}
          className="flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-muted-foreground hover:bg-muted hover:text-foreground"
        >
          <ChevronRight
            className={cn(
              "h-4 w-4 transition-transform",
              expanded && "rotate-90",
            )}
          />
        </button>
        <div className="min-w-0 flex-1">
          <div className="text-sm font-medium leading-tight">{label}</div>
          {!expanded && (
            <div className="truncate text-xs text-muted-foreground">
              {triggerSummary(trigger)}
            </div>
          )}
        </div>
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label={`删除触发器 ${label}`}
          onClick={onRemove}
        >
          <Trash2 className="h-4 w-4" />
        </Button>
      </div>

      {expanded && (
        <div className="space-y-3 border-t px-3 py-3">
          {trigger.trigger_type === "cron" && (
            <CronScheduleFields
              expression={trigger.cron_expression}
              timezone={trigger.timezone}
              onChange={(expr, tz) =>
                onChange({ ...trigger, cron_expression: expr, timezone: tz })
              }
            />
          )}

          {pr && (
            <p className="text-xs text-muted-foreground">
              GitHub PR
              触发不做事件字段过滤：仓库范围由 GitHub App
              安装与项目关联决定， draft 与 fork 不审查。
            </p>
          )}

          {isIssueTrigger(trigger.trigger_type) && (
            <EventFilterFields
              triggerType={trigger.trigger_type}
              inputId={`trigger-labels-${trigger.key}`}
              filter={trigger.event_filter ?? {}}
              onChange={(next) =>
                onChange({ ...trigger, event_filter: next })
              }
            />
          )}

          {trigger.trigger_type !== "cron" && (
            <div className="space-y-2">
              <Label>限定 Fast Ship 项目（可选）</Label>
              <IssueProjectSelect
                value={trigger.issue_project_id ?? ""}
                onChange={(next) =>
                  onChange({
                    ...trigger,
                    issue_project_id: next === "" ? null : next,
                  })
                }
              />
              <p className="text-xs text-muted-foreground">
                留空表示当前用户全部项目的事件都触发。
              </p>
            </div>
          )}

          {pr && (
            <>
              <div className="space-y-2">
                <Label>模型（可多选，每个模型各开一个运行）</Label>
                <ModelMultiPicker
                  providers={providers}
                  selected={trigger.model_selections.map((sel) => ({
                    instanceId: sel.instance_id,
                    model: sel.model,
                    efforts: {},
                  }))}
                  disabled={providers.length === 0}
                  className="w-full max-w-none"
                  onToggle={(pick) => {
                    const resolved = resolveModel(providers, t3Project, pick);
                    const sel = toAutomationModelSelection(
                      buildModelSelection(resolved),
                    ) ?? {
                      instance_id: pick.instanceId,
                      model: pick.model,
                    };
                    toggleModelSelection(sel);
                  }}
                />
                <p className="text-xs text-muted-foreground">
                  必须显式选择（不能「项目默认」）；一次触发每个模型并行开一个运行。
                </p>
                {!providers.length && (
                  <p className="text-xs text-muted-foreground">
                    选择 T3 环境与项目后展示可用模型。
                  </p>
                )}
              </div>
              <div className="space-y-2">
                <Label htmlFor={`trigger-debounce-${trigger.key}`}>
                  防抖窗口（秒）
                </Label>
                <Input
                  id={`trigger-debounce-${trigger.key}`}
                  type="number"
                  min={0}
                  max={86400}
                  value={trigger.debounce_seconds}
                  onChange={(e) => {
                    // 清空或非法输入不写入：Number("") === 0 会把「清空」变成
                    // 「立即开审」，非数字又会变 NaN。保留当前值，只接受有限数。
                    const next = Number(e.target.value);
                    if (e.target.value.trim() === "" || !Number.isFinite(next)) {
                      return;
                    }
                    onChange({ ...trigger, debounce_seconds: next });
                  }}
                />
                <p className="text-xs text-muted-foreground">
                  窗口内新推送会重置计时，到点只审查最新提交；0
                  表示立即开审。默认 60。
                </p>
              </div>
            </>
          )}
        </div>
      )}
    </div>
  );
}

// EventFilterFields 事件过滤的可选输入：无过滤 = 该类型全部真实事件。
function EventFilterFields({
  triggerType,
  inputId,
  filter,
  onChange,
}: {
  triggerType: AutomationTriggerType;
  inputId: string;
  filter: AutomationEventFilter;
  onChange: (filter: AutomationEventFilter | null) => void;
}) {
  if (triggerType === "issue_created") {
    return (
      <p className="text-xs text-muted-foreground">
        Issue 新建事件不做事件字段过滤；项目范围用下方「限定 Fast
        Ship 项目」控制。
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
      <Label htmlFor={inputId}>匹配标签（可选，逗号分隔）</Label>
      <Input
        id={inputId}
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
      onValueChange={(next) =>
        onChange(!next || next === ALL_PROJECTS ? "" : next)
      }
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
