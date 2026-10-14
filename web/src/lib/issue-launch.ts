import type { Environment, ThreadSummary } from "@/lib/api/threads";
import type {
  ProjectT3Binding,
  T3EnvironmentProjects,
} from "@/lib/api/t3-bindings";
import { ENVIRONMENT_STATUS_LABELS } from "@/lib/threads";
import {
  canOperate,
  type ModelPick,
  type ProviderChoice,
} from "@/lib/threads-compose";

/** 启动弹框的环境行：按环境列表序 join 绑定（每环境至多一条）。 */
export interface LaunchEnvironmentRow {
  environment: Environment;
  binding?: ProjectT3Binding;
}

export function joinEnvironmentsWithBindings(
  environments: readonly Environment[],
  bindings: readonly ProjectT3Binding[],
): LaunchEnvironmentRow[] {
  const byEnvironment = new Map(
    bindings.map((binding) => [binding.environment_id, binding]),
  );
  return environments.map((environment) => ({
    environment,
    binding: byEnvironment.get(environment.id),
  }));
}

/** 可启动 = 已绑定 + 环境正常 + 可操作。 */
export function isLaunchable(row: LaunchEnvironmentRow) {
  return (
    Boolean(row.binding) &&
    row.environment.status === "ok" &&
    canOperate(row.environment)
  );
}

/** 自动负载的忙碌口径：该环境 running + waiting 的线程数。 */
export function countBusyThreads(
  threads: readonly ThreadSummary[],
  environmentId: string,
) {
  return threads.filter(
    (thread) =>
      thread.environment_id === environmentId &&
      (thread.status === "running" || thread.status === "waiting"),
  ).length;
}

/** 自动选环境：可启动候选中忙碌数最少者；平手取环境列表顺序靠前者。 */
export function pickAutoEnvironmentRow(
  rows: readonly LaunchEnvironmentRow[],
  threads: readonly ThreadSummary[],
): LaunchEnvironmentRow | undefined {
  let best: LaunchEnvironmentRow | undefined;
  let bestBusy = Infinity;
  for (const row of rows) {
    if (!isLaunchable(row)) continue;
    const busy = countBusyThreads(threads, row.environment.id);
    if (busy < bestBusy) {
      best = row;
      bestBusy = busy;
    }
  }
  return best;
}

/** 已绑定但不可启动的环境行的原因文案。 */
export function unlaunchableReason(row: LaunchEnvironmentRow): string {
  if (!canOperate(row.environment)) return "只读";
  if (row.environment.status !== "ok") {
    return (
      ENVIRONMENT_STATUS_LABELS[row.environment.status] ??
      row.environment.status
    );
  }
  return "不可用";
}

/**
 * 模板偏好 → ModelPick：偏好的实例或模型在该环境不存在时返回 null，
 * 由调用方回落项目默认并给出提示。
 */
export function preferenceModelPick(
  selection: IssuePromptPreferences["model_selection"],
  providers: readonly ProviderChoice[],
): ModelPick | null {
  if (!selection) return null;
  const provider = providers.find(
    (p) => p.instanceId === selection.instance_id,
  );
  if (!provider?.models.some((m) => m.slug === selection.model)) return null;
  const efforts: Record<string, string> = {};
  for (const option of selection.options ?? []) {
    if (typeof option.value === "string") efforts[option.id] = option.value;
  }
  return {
    instanceId: selection.instance_id,
    model: selection.model,
    efforts,
  };
}

/**
 * 绑定失效原因：环境不可达，或快照与最新发现不一致（T3 项目被删/改名/挪路径）。
 * 该环境没有返回发现结果时不下结论（null），避免把「没拉到」误判成「已失效」。
 */
export function bindingInvalidReason(
  binding: ProjectT3Binding,
  environment: Environment | undefined,
  discovered: T3EnvironmentProjects | undefined,
): string | null {
  if (environment && environment.status !== "ok") {
    return `环境${ENVIRONMENT_STATUS_LABELS[environment.status] ?? "不可达"}`;
  }
  if (!discovered) return null;
  if (discovered.error) return "环境发现失败";
  const found = (discovered.projects ?? []).find(
    (project) => project.id === binding.t3_project_id,
  );
  if (!found) return "绑定的 T3 项目已不存在";
  if (binding.t3_project_title && found.title !== binding.t3_project_title) {
    return "T3 项目已改名";
  }
  if (
    binding.workspace_root &&
    found.workspace_root &&
    found.workspace_root !== binding.workspace_root
  ) {
    return "T3 项目工作区已变化";
  }
  return null;
}

/**
 * 弹框环境默认值：偏好指定的环境仍可启动时用偏好；否则回落自动负载，
 * 由调用方决定是否提示（仅偏好环境曾经有效现在不可启动时需要）。
 */
export function resolveEnvironmentChoice(
  preference: IssuePromptPreferences["environment"],
  rows: readonly LaunchEnvironmentRow[],
): { choice: "auto" | string; preferredMissing: boolean } {
  if (!preference || preference === "auto") {
    return { choice: "auto", preferredMissing: false };
  }
  const row = rows.find((r) => r.environment.id === preference);
  if (row && isLaunchable(row)) {
    return { choice: row.environment.id, preferredMissing: false };
  }
  return { choice: "auto", preferredMissing: Boolean(row?.binding) };
}

/** 设置页偏好摘要：环境/工作区/模型各段，缺省的段不出现。 */
export function describeIssuePromptPreferences(
  preferences: IssuePromptPreferences,
  environmentName?: (id: string) => string | undefined,
): string {
  const parts: string[] = [];
  if (preferences.environment) {
    parts.push(
      preferences.environment === "auto"
        ? "环境 自动"
        : `环境 ${environmentName?.(preferences.environment) ?? preferences.environment}`,
    );
  }
  if (preferences.workspace) {
    parts.push(
      preferences.workspace === "root"
        ? "工作区 Current checkout"
        : "工作区 New worktree",
    );
  }
  if (preferences.model_selection?.model) {
    parts.push(`模型 ${preferences.model_selection.model}`);
  }
  return parts.join(" · ");
}
