import type {
  Environment,
  EnvironmentStatus,
  ThreadStatus,
  ThreadSummary,
} from "@/lib/api/threads";

export const THREAD_PAGE_SIZE = 60;

export const THREAD_STATUS_LABELS: Record<ThreadStatus, string> = {
  running: "运行中",
  waiting: "等待输入或审批",
  idle: "空闲",
  archived: "已归档",
};

export const ENVIRONMENT_STATUS_LABELS: Record<EnvironmentStatus, string> = {
  ok: "正常",
  unreachable: "不可达",
  repair: "需重新配对",
  degraded: "部分可读",
};

/** T3 条目原始状态的中文名（快照透传值，未知值原样显示）。 */
export const ITEM_STATUS_LABELS: Record<string, string> = {
  idle: "空闲",
  pending: "排队中",
  running: "运行中",
  waiting: "等待输入",
  completed: "已完成",
  failed: "失败",
  cancelled: "已取消",
  interrupted: "已中断",
};

export function itemStatusLabel(status: string) {
  return ITEM_STATUS_LABELS[status] ?? status;
}

/** status 为空表示默认视图：除已归档外的全部线程。 */
export interface ThreadFilters {
  environmentIds: string[];
  /** projectKey 值，空表示全部项目。 */
  project: string;
  status: ThreadStatus | "";
  /** 完结筛选，与 status 独立叠加；空表示不过滤。 */
  settled: "" | "settled" | "unsettled";
  query: string;
}

export const DEFAULT_THREAD_FILTERS: ThreadFilters = {
  environmentIds: [],
  project: "",
  status: "",
  settled: "",
  query: "",
};

export function isDefaultThreadFilters(filters: ThreadFilters) {
  return (
    filters.environmentIds.length === 0 &&
    !filters.project &&
    !filters.status &&
    !filters.settled &&
    !filters.query.trim()
  );
}

function time(value: string) {
  const ms = new Date(value).getTime();
  return Number.isNaN(ms) ? 0 : ms;
}

/** 等待输入或审批的线程置顶，其余按更新时间倒序。 */
export function sortThreads(threads: ThreadSummary[]) {
  return [...threads].sort((a, b) => {
    const waitingDiff =
      Number(b.status === "waiting") - Number(a.status === "waiting");
    return waitingDiff || time(b.updated_at) - time(a.updated_at);
  });
}

export function filterThreads(
  threads: ThreadSummary[],
  filters: ThreadFilters,
) {
  const query = filters.query.trim().toLowerCase();
  return threads.filter((thread) => {
    if (filters.status) {
      if (thread.status !== filters.status) return false;
    } else if (thread.status === "archived") {
      return false;
    }
    if (filters.settled && thread.settled !== (filters.settled === "settled")) {
      return false;
    }
    if (
      filters.environmentIds.length > 0 &&
      !filters.environmentIds.includes(thread.environment_id)
    ) {
      return false;
    }
    if (filters.project && projectKey(thread) !== filters.project) {
      return false;
    }
    return !query || thread.title.toLowerCase().includes(query);
  });
}

/** 项目筛选的稳定身份：环境 + 项目 id，不随项目改名或同名项目而变。 */
export function projectKey(thread: ThreadSummary) {
  return `${thread.environment_id}:${thread.project_id}`;
}

export interface ProjectOption {
  key: string;
  label: string;
}

/** 按 环境+项目 id 去重；不同项目重名时在名称后附上环境名以区分。 */
export function listProjects(threads: ThreadSummary[]): ProjectOption[] {
  const projects = new Map<string, ThreadSummary>();
  for (const t of threads) {
    if (t.project_id && t.project_name) projects.set(projectKey(t), t);
  }
  const nameCounts = new Map<string, number>();
  for (const t of projects.values()) {
    nameCounts.set(t.project_name, (nameCounts.get(t.project_name) ?? 0) + 1);
  }
  return [...projects.entries()]
    .map(([key, t]) => ({
      key,
      label:
        (nameCounts.get(t.project_name) ?? 0) > 1
          ? `${t.project_name} · ${t.environment_name}`
          : t.project_name,
    }))
    .sort((a, b) => a.label.localeCompare(b.label, "zh-CN"));
}

export function listProblemEnvironments(environments: Environment[]) {
  return environments.filter((env) => env.status !== "ok");
}

export function threadDetailPath(thread: ThreadSummary) {
  return `/threads/${encodeURIComponent(thread.environment_id)}/${encodeURIComponent(thread.id)}`;
}

/** 项目图标徽章取项目名首字符；无名时用 "?"。 */
export function projectInitial(name: string) {
  const first = [...name.trim()][0];
  return first ? first.toUpperCase() : "?";
}

/** 同名项目恒得同一个色号（0..count-1），用于从调色板里挑徽章底色。 */
export function projectColorIndex(name: string, count: number) {
  let hash = 0;
  for (const ch of name) hash = (hash * 31 + (ch.codePointAt(0) ?? 0)) >>> 0;
  return hash % count;
}

/**
 * 运行已持续的分钟数；起点缺失、无法解析或在未来（时钟偏差）时返回 undefined，
 * 调用方据此省略时长。
 */
export function runMinutes(startedAt: string | undefined, now: number) {
  if (!startedAt) return undefined;
  const started = new Date(startedAt).getTime();
  if (Number.isNaN(started) || started > now) return undefined;
  return Math.floor((now - started) / 60_000);
}

export function formatRunMinutes(minutes: number) {
  if (minutes < 1) return "不足 1 分钟";
  if (minutes < 60) return `${minutes} 分钟`;
  const rest = minutes % 60;
  return `${Math.floor(minutes / 60)} 小时${rest ? ` ${rest} 分钟` : ""}`;
}
