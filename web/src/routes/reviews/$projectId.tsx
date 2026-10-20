import { useState } from "react";
import {
  Link,
  useParams,
  useSearchParams,
} from "react-router";
import {
  ChevronRight,
  ExternalLink,
  Plus,
  RotateCw,
} from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Header } from "@/components/layout/header";
import { CombinationRow } from "@/components/reviews/combination-row";
import { ConclusionBadge } from "@/components/reviews/conclusion-badge";
import { ApiRequestError } from "@/lib/api/client";
import {
  reviewRunStatusLabel,
  type ReviewCombination,
  type ReviewPullRequestRecord,
  type ReviewRoundRecord,
  type ReviewRunSummary,
  type ReviewSettings,
  type ReviewSettingsInput,
} from "@/lib/api/reviews";
import { useEnvironments } from "@/lib/hooks/use-threads";
import { useProject } from "@/lib/hooks/use-projects";
import {
  useReviewRecords,
  useReviewSettings,
  useRerunReview,
  useUpdateReviewSettings,
} from "@/lib/hooks/use-reviews";
import { formatDate } from "@/lib/utils/format";
import { cn } from "@/lib/utils";
import { uuid } from "@/lib/uuid";

// 项目审查页（INT-135）：单路由内两个分页——「设置」（开关、触发条件、
// 代码/视觉组合）与「审查记录」（PR → 轮次 → 各组合运行）。分页由
// ?tab=settings|records 驱动，刷新后保持。

const CODE_MAX = 5;
const VISUAL_MAX = 3;

type ReviewTab = "settings" | "records";

function tabFromParam(value: string | null): ReviewTab {
  return value === "records" ? "records" : "settings";
}

export default function ReviewProjectPage() {
  const { projectId = "" } = useParams();
  const [searchParams, setSearchParams] = useSearchParams();
  const tab = tabFromParam(searchParams.get("tab"));

  const { data: project } = useProject(projectId);
  const { data: settings, isLoading } = useReviewSettings(projectId);

  const title = (
    <span className="flex min-w-0 items-center gap-1.5">
      <Link
        to="/reviews"
        className="shrink-0 font-normal text-muted-foreground transition-colors hover:text-foreground"
      >
        审查
      </Link>
      <span className="shrink-0 text-muted-foreground">/</span>
      <span className="truncate">
        {project?.name ??
          (project ? `${project.github_owner}/${project.github_repo}` : projectId)}
      </span>
    </span>
  );

  if (isLoading) {
    return (
      <div className="flex flex-col">
        <Header title={title} />
        <div className="mx-auto w-full max-w-3xl space-y-3 p-4 md:p-6">
          <Skeleton className="h-8 w-1/3" />
          <Skeleton className="h-40 rounded-lg" />
        </div>
      </div>
    );
  }

  if (!settings) {
    return (
      <div className="flex flex-col">
        <Header title={title} />
        <div className="mx-auto w-full max-w-3xl p-4 md:p-6">
          <div className="space-y-4 rounded-lg border bg-muted/40 p-10 text-center">
            <p className="text-sm text-muted-foreground">
              项目不存在或没有关联 GitHub 仓库
            </p>
            <div>
              <Button
                variant="outline"
                size="sm"
                render={<Link to="/reviews" />}
              >
                返回审查列表
              </Button>
            </div>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="flex flex-col">
      <Header title={title} />
      <div className="mx-auto w-full max-w-3xl space-y-6 p-4 md:p-6">
        <Tabs
          value={tab}
          onValueChange={(next) => setSearchParams({ tab: String(next) })}
          className="space-y-4"
        >
          <TabsList>
            <TabsTrigger value="settings">设置</TabsTrigger>
            <TabsTrigger value="records">审查记录</TabsTrigger>
          </TabsList>
          <TabsContent value="settings">
            {/* key 固定项目 id：保存后的 refetch 不重挂载表单、不清输入。 */}
            <ReviewSettingsForm
              key={settings.project_id}
              projectId={settings.project_id}
              settings={settings}
            />
          </TabsContent>
          <TabsContent value="records">
            <ReviewRecordsPanel projectId={projectId} />
          </TabsContent>
        </Tabs>
      </div>
    </div>
  );
}

/* ---------------- 设置 ---------------- */

interface CombinationFormItem {
  /** 本地 React key（与服务端字段无关）。 */
  key: string;
  value: ReviewCombination;
}

interface ReviewFormState {
  enabled: boolean;
  trigger_opened: boolean;
  trigger_synchronize: boolean;
  trigger_ready_for_review: boolean;
  trigger_reopened: boolean;
  code: CombinationFormItem[];
  visual: CombinationFormItem[];
}

function toFormItem(c: ReviewCombination): CombinationFormItem {
  return { key: uuid(), value: c };
}

function settingsToForm(settings: ReviewSettings): ReviewFormState {
  return {
    enabled: settings.enabled,
    trigger_opened: settings.trigger_opened,
    trigger_synchronize: settings.trigger_synchronize,
    trigger_ready_for_review: settings.trigger_ready_for_review,
    trigger_reopened: settings.trigger_reopened,
    code: (settings.code_combinations ?? []).map(toFormItem),
    visual: (settings.visual_combinations ?? []).map(toFormItem),
  };
}

/** 保存时裁掉仅响应字段（environment_name/t3_project_id），只回传输入字段。 */
function toCombinationInput(c: ReviewCombination): ReviewCombination {
  const input: ReviewCombination = {
    environment_id: c.environment_id,
    instance_id: c.instance_id,
    model: c.model,
  };
  if (c.options?.length) input.options = c.options;
  return input;
}

/** 组合唯一性键：环境 + harness + 模型 + 规范化 options（id 排序后的 {id,value}）。 */
function combinationKey(c: ReviewCombination): string {
  const options = (c.options ?? [])
    .map((o) => `${o.id}=${String(o.value)}`)
    .sort()
    .join(",");
  return `${c.environment_id}|${c.instance_id}|${c.model}|${options}`;
}

function hasDuplicate(items: CombinationFormItem[]): boolean {
  const seen = new Set<string>();
  for (const item of items) {
    const key = combinationKey(item.value);
    if (seen.has(key)) return true;
    seen.add(key);
  }
  return false;
}

const TRIGGER_OPTIONS: {
  key: "trigger_opened" | "trigger_synchronize" | "trigger_ready_for_review" | "trigger_reopened";
  label: string;
}[] = [
  { key: "trigger_opened", label: "PR 打开" },
  { key: "trigger_synchronize", label: "新提交推送" },
  { key: "trigger_ready_for_review", label: "转为可审查" },
  { key: "trigger_reopened", label: "重新打开" },
];

function ReviewSettingsForm({
  projectId,
  settings,
}: {
  projectId: string;
  settings: ReviewSettings;
}) {
  const [form, setForm] = useState<ReviewFormState>(() =>
    settingsToForm(settings),
  );
  const [formError, setFormError] = useState<string | null>(null);
  const updateSettings = useUpdateReviewSettings(projectId);
  const { data: environments = [] } = useEnvironments();

  // can_manage=false 或同仓库已被其他项目开启：整页只读。
  const readOnly = settings.can_manage === false || Boolean(settings.taken_by);
  const missingPrereqs = (settings.prerequisites ?? []).filter(
    (p) => p.missing,
  );
  const hasMissing = missingPrereqs.length > 0;
  const submitting = updateSettings.isPending;

  const set = <K extends keyof ReviewFormState>(
    key: K,
    value: ReviewFormState[K],
  ) => setForm((f) => ({ ...f, [key]: value }));

  const updateCombination = (
    kind: "code" | "visual",
    key: string,
    next: ReviewCombination,
  ) =>
    setForm((f) => ({
      ...f,
      [kind]: f[kind].map((item) =>
        item.key === key ? { ...item, value: next } : item,
      ),
    }));

  const removeCombination = (kind: "code" | "visual", key: string) =>
    setForm((f) => ({
      ...f,
      [kind]: f[kind].filter((item) => item.key !== key),
    }));

  const addCombination = (kind: "code" | "visual") =>
    setForm((f) => ({
      ...f,
      [kind]: [
        ...f[kind],
        toFormItem({ environment_id: "", instance_id: "", model: "" }),
      ],
    }));

  const validate = (): string | null => {
    for (const [label, items, max] of [
      ["代码审查", form.code, CODE_MAX],
      ["视觉审查", form.visual, VISUAL_MAX],
    ] as const) {
      if (items.length > max) return `${label}组合最多 ${max} 个`;
      for (const item of items) {
        const v = item.value;
        if (!v.environment_id || !v.instance_id || !v.model) {
          return `${label}组合存在未完成的环境或模型选择`;
        }
      }
      if (hasDuplicate(items)) return `${label}组合存在重复项`;
    }
    if (form.enabled && form.code.length === 0) {
      return "开启审查需要至少 1 个代码审查组合";
    }
    if (form.enabled && form.code.length > CODE_MAX) {
      return `代码审查组合最多 ${CODE_MAX} 个`;
    }
    return null;
  };

  const handleSubmit = async () => {
    const error = validate();
    if (error) {
      toast.error(error);
      return;
    }
    setFormError(null);
    const input: ReviewSettingsInput = {
      enabled: form.enabled,
      trigger_opened: form.trigger_opened,
      trigger_synchronize: form.trigger_synchronize,
      trigger_ready_for_review: form.trigger_ready_for_review,
      trigger_reopened: form.trigger_reopened,
      code_combinations: form.code.map((item) =>
        toCombinationInput(item.value),
      ),
      visual_combinations: form.visual.map((item) =>
        toCombinationInput(item.value),
      ),
    };
    try {
      await updateSettings.mutateAsync(input);
      toast.success("审查设置已保存");
    } catch (err) {
      // 41202 前置条件缺失：逐条缺项在服务端 message 里，展示在表单顶部；
      // 40922 同仓库已被其他项目开启。
      if (
        err instanceof ApiRequestError &&
        (err.code === 41202 || err.code === 40922)
      ) {
        setFormError(
          err.message ||
            (err.code === 40922
              ? "同一仓库已被其他项目开启审查"
              : "前置条件未满足"),
        );
        return;
      }
      toast.error(errMessage(err, "保存失败"));
    }
  };

  // 前置条件缺项只拦「开启状态下的保存」：enabled=false 时改组合/触发
  // 条件仍然可保存（关闭本身不依赖前置），41202 由后端兜底。
  const saveDisabled = readOnly || submitting || (form.enabled && hasMissing);

  const combinationSection = (
    kind: "code" | "visual",
    title: string,
    max: number,
    hint: string,
  ) => (
    <section className="space-y-2">
      <div className="flex items-center justify-between">
        <h2 className="text-sm font-medium text-muted-foreground">{title}</h2>
        <Button
          variant="outline"
          size="sm"
          disabled={readOnly || submitting || form[kind].length >= max}
          onClick={() => addCombination(kind)}
        >
          <Plus className="mr-1.5 h-3.5 w-3.5" />
          添加组合
        </Button>
      </div>
      <div className="space-y-2">
        {form[kind].map((item) => (
          <CombinationRow
            key={item.key}
            value={item.value}
            projectId={projectId}
            environments={environments}
            disabled={readOnly || submitting}
            onChange={(next) => updateCombination(kind, item.key, next)}
            onRemove={() => removeCombination(kind, item.key)}
          />
        ))}
      </div>
      <p className="text-xs text-muted-foreground">{hint}</p>
    </section>
  );

  return (
    <div className="space-y-6">
      {readOnly && (
        <p
          role="note"
          className="rounded-lg bg-muted/50 px-3 py-2 text-sm text-muted-foreground"
        >
          {settings.taken_by
            ? `同一仓库已由 ${settings.taken_by} 的项目开启审查，当前项目为只读。`
            : "当前项目对审查设置为只读。"}
        </p>
      )}
      {formError && (
        <p
          role="alert"
          className="rounded-lg bg-destructive/10 px-3 py-2 text-sm text-destructive"
        >
          {formError}
        </p>
      )}

      <section className="space-y-2">
        <h2 className="text-sm font-medium text-muted-foreground">前置条件</h2>
        <ul className="space-y-1">
          {(settings.prerequisites ?? []).map((p) => (
            <li
              key={p.code}
              className={cn(
                "text-sm",
                p.missing ? "text-destructive" : "text-muted-foreground",
              )}
            >
              {p.missing ? "✗" : "✓"} {p.label}
              {p.missing && p.detail ? (
                <span className="ml-1 text-xs">（{p.detail}）</span>
              ) : null}
            </li>
          ))}
        </ul>
        {hasMissing && (
          <p className="text-xs text-destructive">
            前置条件未全部满足，无法开启审查。
          </p>
        )}
      </section>

      <section className="space-y-3">
        <div className="flex items-center gap-2">
          <Switch
            checked={form.enabled}
            disabled={
              readOnly || submitting || (!form.enabled && hasMissing)
            }
            onCheckedChange={(next) => set("enabled", next)}
            aria-label="开启审查"
          />
          <span className="text-sm text-muted-foreground">
            {form.enabled ? "已开启" : "未开启"}
          </span>
        </div>
      </section>

      <section className="space-y-2">
        <h2 className="text-sm font-medium text-muted-foreground">触发条件</h2>
        <div className="grid grid-cols-2 gap-2">
          {TRIGGER_OPTIONS.map((option) => (
            <Label
              key={option.key}
              className="items-center gap-2 font-normal text-sm text-foreground"
            >
              <input
                type="checkbox"
                checked={form[option.key]}
                disabled={readOnly || submitting}
                onChange={(e) => set(option.key, e.target.checked)}
                className="h-4 w-4 shrink-0 rounded border border-input accent-primary"
              />
              <span>{option.label}</span>
            </Label>
          ))}
        </div>
        <p className="text-xs text-muted-foreground">
          草稿与 fork 仓库的 PR 不审查；推送后有 60 秒防抖。
        </p>
      </section>

      {combinationSection(
        "code",
        "代码审查组合",
        CODE_MAX,
        "每个组合在 PR 上各开一个代码审查运行；开启时至少 1 个、最多 5 个。",
      )}

      {combinationSection(
        "visual",
        "视觉审查组合",
        VISUAL_MAX,
        "最多 3 个组合；不配置就从不启动视觉审查。",
      )}

      <div className="flex items-center gap-3">
        <Button size="sm" onClick={handleSubmit} disabled={saveDisabled}>
          {submitting ? "保存中..." : "保存"}
        </Button>
        {form.enabled && hasMissing && (
          <span className="text-xs text-destructive">
            前置条件未全部满足，无法开启审查。
          </span>
        )}
      </div>
    </div>
  );
}

/* ---------------- 审查记录 ---------------- */

function ReviewRecordsPanel({ projectId }: { projectId: string }) {
  const { data: records, isLoading } = useReviewRecords(projectId);
  const rerun = useRerunReview(projectId);
  const [expanded, setExpanded] = useState<Set<number>>(new Set());

  const toggle = (prNumber: number) =>
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(prNumber)) next.delete(prNumber);
      else next.add(prNumber);
      return next;
    });

  const handleRerun = async (prNumber: number, repoFullName: string) => {
    try {
      const res = await rerun.mutateAsync({ prNumber, repoFullName });
      toast.success(
        `已受理，第 ${res.data.round_number} 轮审查已排队`,
      );
    } catch (err) {
      if (err instanceof ApiRequestError && err.status === 404) {
        toast.error("重新审查暂不可用");
        return;
      }
      toast.error(errMessage(err, "重新审查失败"));
    }
  };

  if (isLoading) {
    return (
      <div className="space-y-2 py-2">
        {Array.from({ length: 3 }).map((_, i) => (
          <Skeleton key={i} className="h-12 rounded" />
        ))}
      </div>
    );
  }

  if (!records?.length) {
    return (
      <div className="rounded-lg border bg-muted/40 py-12 text-center">
        <p className="text-sm text-muted-foreground">还没有审查记录</p>
        <p className="mt-1 text-xs text-muted-foreground">
          该项目的 PR 触发审查后会出现在这里
        </p>
      </div>
    );
  }

  return (
    <div className="space-y-2">
      {records.map((pr) => (
        <PullRequestRecord
          key={`${pr.repo_full_name}#${pr.pr_number}`}
          record={pr}
          expanded={expanded.has(pr.pr_number)}
          onToggle={() => toggle(pr.pr_number)}
          onRerun={() => void handleRerun(pr.pr_number, pr.repo_full_name)}
          rerunning={rerun.isPending}
        />
      ))}
    </div>
  );
}

function PullRequestRecord({
  record,
  expanded,
  onToggle,
  onRerun,
  rerunning,
}: {
  record: ReviewPullRequestRecord;
  expanded: boolean;
  onToggle: () => void;
  onRerun: () => void;
  rerunning: boolean;
}) {
  return (
    <div className="rounded-lg border">
      <div className="flex items-center gap-2 px-3 py-2">
        <button
          type="button"
          aria-expanded={expanded}
          aria-label={`${expanded ? "收起" : "展开"} PR #${record.pr_number}`}
          onClick={onToggle}
          className="flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-muted-foreground hover:bg-muted hover:text-foreground"
        >
          <ChevronRight
            className={cn("h-4 w-4 transition-transform", expanded && "rotate-90")}
          />
        </button>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-1.5">
            <a
              href={record.pr_url}
              target="_blank"
              rel="noreferrer"
              className="truncate text-sm font-medium hover:underline"
            >
              #{record.pr_number} {record.pr_title}
            </a>
            <ExternalLink className="h-3 w-3 shrink-0 text-muted-foreground" />
          </div>
          <div className="mt-0.5 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
            <span className="font-mono">{shortSHA(record.head_sha)}</span>
            <ConclusionBadge label="代码" status={record.code_status} />
            <ConclusionBadge label="视觉" status={record.visual_status} />
            <span>{formatDate(record.updated_at)}</span>
          </div>
        </div>
        <Button
          variant="outline"
          size="sm"
          disabled={rerunning || !record.can_rerun}
          onClick={onRerun}
          title={
            record.can_rerun
              ? undefined
              : "该记录属于历史仓库（项目仓库已变更），不能重跑"
          }
          aria-label={`重新审查 PR #${record.pr_number}`}
        >
          <RotateCw className="mr-1.5 h-3.5 w-3.5" />
          重新审查
        </Button>
      </div>

      {expanded && (
        <div className="space-y-3 border-t px-3 py-3">
          {record.rounds.map((round) => (
            <RoundRecord key={round.round_number} round={round} />
          ))}
        </div>
      )}
    </div>
  );
}

function RoundRecord({ round }: { round: ReviewRoundRecord }) {
  return (
    <div className="space-y-1.5">
      <p className="text-xs font-medium text-muted-foreground">
        第 {round.round_number} 轮 ·{" "}
        <span className="font-mono">{shortSHA(round.head_sha)}</span> ·{" "}
        {formatDate(round.created_at)}
      </p>
      <div className="space-y-1">
        {round.code_runs.map((run) => (
          <RunRow key={run.id} run={run} kindLabel="代码审查" />
        ))}
        {round.visual_runs.map((run) => (
          <RunRow key={run.id} run={run} kindLabel="视觉审查" />
        ))}
      </div>
    </div>
  );
}

function RunRow({
  run,
  kindLabel,
}: {
  run: ReviewRunSummary;
  kindLabel: string;
}) {
  return (
    <div className="rounded-md bg-muted/40 px-3 py-2">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm">
        <span className="text-muted-foreground">{kindLabel}</span>
        <span className="font-medium">
          {run.environment_name || run.environment_id}
        </span>
        <span className="text-muted-foreground">
          {run.instance_id} · {run.model}
          {run.options?.length
            ? ` · ${run.options.map((o) => `${o.id}=${String(o.value)}`).join(", ")}`
            : null}
        </span>
        <Badge variant={runStatusVariant(run.status)}>
          {reviewRunStatusLabel(run.status)}
        </Badge>
        {typeof run.findings_count === "number" && run.findings_count > 0 && (
          <span className="text-xs text-muted-foreground">
            {run.findings_count} 条发现
          </span>
        )}
        {run.thread_id && (
          <a
            className="ml-auto text-xs text-primary hover:underline"
            href={`/threads/${run.environment_id}/${run.thread_id}`}
          >
            线程
          </a>
        )}
      </div>
      {(run.error_message || run.error_code) && (
        <p className="mt-1 text-xs text-destructive">
          {run.error_message || run.error_code}
        </p>
      )}
      {run.summary && (
        <p className="mt-1 line-clamp-2 text-xs text-muted-foreground">
          {run.summary}
        </p>
      )}
    </div>
  );
}

function runStatusVariant(
  status: ReviewRunSummary["status"],
): "default" | "secondary" | "destructive" | "outline" {
  switch (status) {
    case "completed":
      return "secondary";
    case "failed":
    case "timed_out":
      return "destructive";
    case "cancelled":
      return "outline";
    default:
      return "default";
  }
}

function shortSHA(sha: string): string {
  return sha.length > 7 ? sha.slice(0, 7) : sha;
}

function errMessage(err: unknown, fallback: string): string {
  if (err instanceof ApiRequestError && err.message) return err.message;
  return fallback;
}
