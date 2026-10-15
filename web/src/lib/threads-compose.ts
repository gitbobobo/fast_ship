import type {
  ApprovalOption,
  ComposerOptions,
  ComposerProject,
  Environment,
  ModelOption,
  ModelSelection,
  ThreadDetail,
  ThreadItem,
  ThreadQueueItem,
  ThreadSummary,
  ThreadWorkspace,
  WorkspaceOptions,
  WorkspaceRef,
} from "@/lib/api/threads";
import { uuid } from "@/lib/uuid";

/** 写操作只看 access；环境未加载或缺省一律按只读处理。 */
export function canOperate(environment?: Pick<Environment, "access">) {
  return environment?.access === "operate";
}

export const READ_ONLY_HINT = "该环境为只读会话，重新配对后可操作";

/** 活动 run 存在（含等待审批的 run）或线程状态为 running，新消息只能入队。 */
export function isThreadActive(detail: ThreadDetail) {
  return detail.thread.status === "running" || Boolean(detail.activity?.active_run_id);
}

/**
 * 需要继续轮询详情的状态：运行中、停止中、有待作答请求、
 * 以及未暂停且队列还有待推进项。仅 held 且无活动/待答时可停。
 */
export function shouldPollThread(detail?: ThreadDetail) {
  if (!detail) return false;
  const { thread, activity, queue } = detail;
  return (
    thread.status === "running" ||
    Boolean(activity?.stopping) ||
    Boolean(activity?.has_pending_requests) ||
    (!queue?.held && (queue?.items.length ?? 0) > 0)
  );
}

export interface SendIds {
  command_id: string;
  message_id: string;
  thread_id: string;
}

/**
 * 同一份内容（fingerprint 相同）的重试复用同一组 ID；内容变了换新组。
 * 调用方在成功后 reset。
 */
export function createIdTracker() {
  let current: { fingerprint: string; ids: SendIds } | null = null;
  return {
    idsFor(fingerprint: string): SendIds {
      if (current?.fingerprint !== fingerprint) {
        current = {
          fingerprint,
          ids: { command_id: uuid(), message_id: uuid(), thread_id: uuid() },
        };
      }
      return current.ids;
    },
    reset() {
      current = null;
    },
  };
}

export const DEFAULT_APPROVAL_OPTIONS: ApprovalOption[] = [
  { decision: "accept", label: "Approve" },
  { decision: "decline", label: "Decline" },
  { decision: "acceptForSession", label: "Always allow this session" },
  { decision: "cancel", label: "Cancel" },
];

/** 审批选项用请求自带的（含 warning）；缺省才用默认四项，不自增 acceptAlways。 */
export function approvalOptions(item: ThreadItem) {
  return item.options?.length ? item.options : DEFAULT_APPROVAL_OPTIONS;
}

/** 仅本线程、pending 且应答通道允许（live/message）的请求可原地作答。 */
export function isRequestAnswerable(item: ThreadItem) {
  return (
    !item.inherited &&
    item.request_status === "pending" &&
    (item.response_capability === "live" ||
      item.response_capability === "message")
  );
}

/** 把排队项 index 上移/下移一位对应的 before_run_id（null 表示移到队尾）。 */
export function reorderTarget(
  items: ThreadQueueItem[],
  index: number,
  direction: "up" | "down",
): string | null | undefined {
  if (direction === "up") return index > 0 ? items[index - 1].run_id : undefined;
  if (index >= items.length - 1) return undefined;
  return items[index + 2]?.run_id ?? null;
}

export function sameAttachmentIds(
  a: { id: string }[] = [],
  b: { id: string }[] = [],
) {
  return a.length === b.length && a.every((x, i) => x.id === b[i].id);
}

/* ---------- 新建线程：工作区 ---------- */

export type WorkspaceChoice =
  | { kind: "root" }
  | { kind: "worktree"; baseRef: string; newBranch: string }
  | { kind: "existing"; path: string; branch: string };

/**
 * 请求体里的 workspace：root 不传 branch；新 worktree 必须有 base_ref，
 * 新分支名为空则省略 branch。
 */
export function buildWorkspace(choice: WorkspaceChoice): ThreadWorkspace | null {
  switch (choice.kind) {
    case "root":
      return { type: "root" };
    case "worktree": {
      const baseRef = choice.baseRef.trim();
      if (!baseRef) return null;
      const branch = choice.newBranch.trim();
      return {
        type: "worktree",
        base_ref: baseRef,
        ...(branch ? { branch } : {}),
      };
    }
    case "existing":
      return choice.path
        ? { type: "existing_worktree", worktree_path: choice.path }
        : null;
  }
}

/**
 * 选中带 worktree_path 的分支候选：切成已有 worktree；路径就是主检出
 * cwd 则回到 root；普通分支则作为新 worktree 的基线（保留已填的新分支名）。
 */
export function chooseRef(
  current: WorkspaceChoice,
  ref: WorkspaceRef,
  cwd: string,
): WorkspaceChoice {
  if (ref.worktree_path) {
    return ref.worktree_path === cwd
      ? { kind: "root" }
      : { kind: "existing", path: ref.worktree_path, branch: ref.name };
  }
  return {
    kind: "worktree",
    baseRef: ref.name,
    newBranch: current.kind === "worktree" ? current.newBranch : "",
  };
}

export interface PreviousWorktree {
  branch: string;
  path: string;
}

function timestamp(value: string) {
  const ms = Date.parse(value);
  return Number.isNaN(ms) || ms <= 0 ? null : ms;
}

/**
 * 同环境、同项目、未归档、非子代理、updated_at 有效、且不是当前 worktree 的
 * 最近线程。线程摘要没有 worktree 路径，用 workspace-options.refs 里同名
 * 分支的 worktree_path 反推；主检出（cwd）属于 Current checkout，不算。
 */
export function resolvePreviousWorktree(input: {
  threads: ThreadSummary[];
  environmentId: string;
  projectId: string;
  workspace?: WorkspaceOptions;
  currentWorktreePath?: string;
}): PreviousWorktree | null {
  const { workspace } = input;
  if (!workspace) return null;
  const pathByBranch = new Map<string, string>();
  for (const ref of workspace.refs) {
    if (ref.worktree_path && ref.worktree_path !== workspace.cwd) {
      pathByBranch.set(ref.name, ref.worktree_path);
    }
  }
  let latest: (PreviousWorktree & { at: number }) | null = null;
  for (const thread of input.threads) {
    if (
      thread.environment_id !== input.environmentId ||
      thread.project_id !== input.projectId ||
      thread.status === "archived" ||
      thread.parent_thread_id
    ) {
      continue;
    }
    const path = pathByBranch.get(thread.branch);
    const at = timestamp(thread.updated_at);
    if (!path || path === input.currentWorktreePath || at === null) continue;
    if (!latest || at > latest.at) latest = { branch: thread.branch, path, at };
  }
  return latest && { branch: latest.branch, path: latest.path };
}

export function previousWorktreeLabel(seed: PreviousWorktree) {
  return seed.branch ? `Previous worktree (${seed.branch})` : "Previous worktree";
}

/* ---------- 新建线程：provider / 模型 ---------- */

export interface ModelChoice {
  slug: string;
  name: string;
  isDefault: boolean;
  descriptors: EffortDescriptor[];
}

export interface EffortDescriptor {
  id: string;
  label: string;
  choices: { id: string; label: string; isDefault: boolean }[];
  currentValue?: string;
}

export interface ProviderChoice {
  instanceId: string;
  name: string;
  models: ModelChoice[];
}

type Json = Record<string, unknown>;

const isRecord = (v: unknown): v is Json =>
  typeof v === "object" && v !== null && !Array.isArray(v);
const str = (v: unknown) => (typeof v === "string" ? v : "");
const list = (v: unknown): unknown[] => (Array.isArray(v) ? v : []);

function parseDescriptors(capabilities: unknown): EffortDescriptor[] {
  if (!isRecord(capabilities)) return [];
  return list(capabilities.optionDescriptors).flatMap((raw) => {
    if (!isRecord(raw) || raw.type !== "select" || !str(raw.id)) return [];
    const choices = list(raw.options).flatMap((c) =>
      isRecord(c) && str(c.id)
        ? [{ id: str(c.id), label: str(c.label) || str(c.id), isDefault: c.isDefault === true }]
        : [],
    );
    return choices.length
      ? [
          {
            id: str(raw.id),
            label: str(raw.label) || str(raw.id),
            choices,
            currentValue: str(raw.currentValue) || undefined,
          },
        ]
      : [];
  });
}

/** 从 composer-options.config.providers 解析可用实例与模型（跳过停用/未安装/不可用的）。 */
export function parseProviders(config: ComposerOptions["config"] | undefined) {
  return list(config?.providers).flatMap((raw): ProviderChoice[] => {
    if (!isRecord(raw) || !str(raw.instanceId)) return [];
    if (
      raw.enabled === false ||
      raw.installed === false ||
      raw.availability === "unavailable"
    ) {
      return [];
    }
    const models = list(raw.models).flatMap((m): ModelChoice[] =>
      isRecord(m) && str(m.slug)
        ? [
            {
              slug: str(m.slug),
              name: str(m.name) || str(m.slug),
              isDefault: m.isDefault === true,
              descriptors: parseDescriptors(m.capabilities),
            },
          ]
        : [],
    );
    return models.length
      ? [
          {
            instanceId: str(raw.instanceId),
            name: str(raw.displayName) || str(raw.instanceId),
            models,
          },
        ]
      : [];
  });
}

export interface ModelPick {
  instanceId: string;
  model: string;
  /** 描述符 id → 选中的 choice id。 */
  efforts: Record<string, string>;
}

function descriptorValue(d: EffortDescriptor, picked?: string) {
  const valid = (id?: string) => d.choices.some((c) => c.id === id);
  return (
    (valid(picked) ? picked : undefined) ??
    (valid(d.currentValue) ? d.currentValue : undefined) ??
    d.choices.find((c) => c.isDefault)?.id
  );
}

export interface ResolvedModel {
  provider?: ProviderChoice;
  model?: ModelChoice;
  efforts: Record<string, string>;
}

/**
 * 选择优先级：用户挑的（仍存在）→ 项目默认 → 第一个实例的默认/第一个模型。
 * 实例或模型在换环境/换项目后不存在时自动回落，不留下不兼容的选择。
 */
export function resolveModel(
  providers: ProviderChoice[],
  project: ComposerProject | undefined,
  pick: ModelPick | null,
): ResolvedModel {
  const find = (instanceId?: string, model?: string) => {
    const provider = providers.find((p) => p.instanceId === instanceId);
    const m = provider?.models.find((x) => x.slug === model);
    return provider && m ? { provider, model: m } : null;
  };
  const picked = pick && find(pick.instanceId, pick.model);
  if (picked) return { ...picked, efforts: pick.efforts };
  const def = project?.default_model_selection;
  const fromDefault = def && find(def.instance_id, def.model);
  if (fromDefault) {
    const efforts: Record<string, string> = {};
    for (const o of def.options ?? []) {
      if (typeof o.value === "string") efforts[o.id] = o.value;
    }
    return { ...fromDefault, efforts };
  }
  const provider = providers[0];
  const model = provider?.models.find((m) => m.isDefault) ?? provider?.models[0];
  return { provider, model, efforts: {} };
}

/** 推理强度等选项以 canonical 数组 [{id,value}] 发送，不是对象。 */
export function buildModelSelection(resolved: ResolvedModel): ModelSelection | undefined {
  const { provider, model } = resolved;
  if (!provider || !model) return undefined;
  const options: ModelOption[] = [];
  for (const d of model.descriptors) {
    const value = descriptorValue(d, resolved.efforts[d.id]);
    if (value) options.push({ id: d.id, value });
  }
  return {
    instance_id: provider.instanceId,
    model: model.slug,
    ...(options.length ? { options } : {}),
  };
}

export function effortValue(d: EffortDescriptor, resolved: ResolvedModel) {
  return descriptorValue(d, resolved.efforts[d.id]);
}

/* ---------- 提问答案 ---------- */

export type AnswerDraft = { picked: string[]; custom: string };

/** 单选给字符串，多选给字符串数组；自定义文本并入同一份答案。 */
export function questionAnswer(
  multi: boolean,
  draft: AnswerDraft,
): string | string[] | undefined {
  const custom = draft.custom.trim();
  const values = [...draft.picked, ...(custom ? [custom] : [])];
  if (values.length === 0) return undefined;
  return multi ? values : values[values.length - 1];
}
