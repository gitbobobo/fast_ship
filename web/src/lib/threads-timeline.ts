import type { ThreadItem, ThreadItemType } from "@/lib/api/threads";

/** 可折叠进「工具调用分组」的条目类型；其余（消息、错误、审批等）单独成行。 */
const FOLDABLE_TYPES: ReadonlySet<ThreadItemType> = new Set([
  "reasoning",
  "command_execution",
  "dynamic_tool",
  "file_change",
  "subagent",
]);

/** T3 条目的进行中状态；其余（含缺省）视为已结束。 */
const ACTIVE_STATUSES: ReadonlySet<string> = new Set([
  "preparing",
  "starting",
  "running",
  "pending",
  "in_progress",
]);

export type TimelineEntry =
  | { kind: "item"; item: ThreadItem }
  | { kind: "group"; id: string; items: ThreadItem[] };

export function isFoldableItem(item: ThreadItem) {
  return FOLDABLE_TYPES.has(item.type);
}

export function isActiveItem(item: ThreadItem) {
  return ACTIVE_STATUSES.has(item.status ?? "");
}

/**
 * 把连续的可折叠条目合并成一个分组（哪怕只有一条），其余条目原样保留。
 * 分组 id 取首条的 id：新条目追加到末尾时，已有分组的身份不变。
 */
export function groupTimeline(items: ThreadItem[]): TimelineEntry[] {
  const entries: TimelineEntry[] = [];
  for (const item of items) {
    const last = entries[entries.length - 1];
    if (!isFoldableItem(item)) {
      entries.push({ kind: "item", item });
    } else if (last?.kind === "group") {
      last.items.push(item);
    } else {
      entries.push({ kind: "group", id: item.id, items: [item] });
    }
  }
  return entries;
}

const DONE_LABELS: Record<string, (n: number) => string> = {
  command_execution: (n) => `运行 ${n} 条命令`,
  dynamic_tool: (n) => `使用 ${n} 个工具`,
  file_change: (n) => `修改 ${n} 个文件`,
  reasoning: (n) => `思考 ${n} 段`,
  subagent: (n) => `${n} 个子代理`,
};

/** 命令摘要的首个词（跳过 VAR=1 形式的环境变量赋值），用于「正在运行 git」。 */
export function commandProgram(summary: string) {
  const words = summary.trim().split(/\s+/);
  return words.find((w) => !/^\w+=/.test(w)) ?? "";
}

function activeLabel(item: ThreadItem) {
  const summary = item.summary.trim().split("\n")[0];
  switch (item.type) {
    case "command_execution": {
      const program = commandProgram(summary);
      return program ? `正在运行 ${program}` : "正在运行命令";
    }
    case "dynamic_tool":
      return summary ? `正在使用 ${summary}` : "正在使用工具";
    case "file_change":
      return "正在修改文件";
    case "subagent":
      return "正在运行子代理";
    default:
      return "正在思考";
  }
}

/**
 * 分组行的一行摘要。live 为 false（线程已停）时不会有进行中状态，
 * 避免上游遗留的 running 条目让摘要永远停在进行式。
 * 进行中取组内最后一个进行中条目；否则按类型计数（首次出现顺序）用「、」连接。
 */
export function summarizeGroup(items: ThreadItem[], live = true) {
  if (live) {
    const active = [...items].reverse().find(isActiveItem);
    if (active) return { text: activeLabel(active), active: true };
  }
  const counts = new Map<string, number>();
  for (const item of items) {
    counts.set(item.type, (counts.get(item.type) ?? 0) + 1);
  }
  const text = [...counts]
    .map(([type, n]) => DONE_LABELS[type]?.(n))
    .filter(Boolean)
    .join("、");
  return { text, active: false };
}
