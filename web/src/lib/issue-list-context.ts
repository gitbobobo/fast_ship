import { ISSUE_WORKFLOW_STATUS_LABELS, type BoardWorkflowStatusParam } from "@/lib/issue-workflow-status";

export type { BoardWorkflowStatusParam };

export interface IssueListContext {
  state?: string;
  q?: string;
  label?: string;
  source?: string;
  workflowStatus?: string;
  sort?: string;
  page?: number;
}

// 合法取值清单由 ISSUE_WORKFLOW_STATUS_LABELS 键派生（"未设置"列对外编码为 unset），
// 避免与编码出处再各写一份（INT-239）。
const BOARD_WORKFLOW_STATUS_VALUES: readonly string[] = [
  ...Object.keys(ISSUE_WORKFLOW_STATUS_LABELS).filter((v) => v !== ""),
  "unset",
];

/** 看板卡片跳详情时携带的列上下文 */
export interface BoardIssueColumnContext {
  workflowStatus: BoardWorkflowStatusParam;
  label: string;
  source: string;
}

/**
 * 详情页来源上下文的三态：
 * - board：来自看板卡片（issue_from=board + 合法列值），固定 state=open，只用列/标签/来源；
 * - list：来自问题列表页（issue_* 参数，旧约定）；
 * - none：无来源（如直接打开链接），不显示来源列表入口。
 */
export type IssueSourceContext =
  | { kind: "none" }
  | ({ kind: "board" } & BoardIssueColumnContext)
  | {
      kind: "list";
      state: string;
      q: string;
      label: string;
      source: string;
      workflowStatus: string;
      sort: string;
      page: number;
    };

const ISSUE_DETAIL_SEARCH_PARAM_MAP = {
  state: "issue_state",
  q: "issue_q",
  label: "issue_label",
  source: "issue_source",
  workflowStatus: "issue_workflow_status",
  sort: "issue_sort",
  page: "issue_page",
} as const;

/** "all"/空串统一归一为空，表示不筛选 */
function normalizeFilterValue(value: string | null) {
  const trimmed = (value ?? "").trim();
  return !trimmed || trimmed === "all" ? "" : trimmed;
}

export function buildIssueDetailSearchParams(context: IssueListContext) {
  const params = new URLSearchParams();

  if (context.state) {
    params.set(ISSUE_DETAIL_SEARCH_PARAM_MAP.state, context.state);
  }
  if (context.q?.trim()) {
    params.set(ISSUE_DETAIL_SEARCH_PARAM_MAP.q, context.q.trim());
  }
  if (context.label && context.label !== "all") {
    params.set(ISSUE_DETAIL_SEARCH_PARAM_MAP.label, context.label);
  }
  if (context.source && context.source !== "all") {
    params.set(ISSUE_DETAIL_SEARCH_PARAM_MAP.source, context.source);
  }
  if (context.workflowStatus && context.workflowStatus !== "all") {
    params.set(ISSUE_DETAIL_SEARCH_PARAM_MAP.workflowStatus, context.workflowStatus);
  }
  if (context.sort) {
    params.set(ISSUE_DETAIL_SEARCH_PARAM_MAP.sort, context.sort);
  }
  if ((context.page ?? 1) > 1) {
    params.set(ISSUE_DETAIL_SEARCH_PARAM_MAP.page, String(context.page));
  }

  return params;
}

/** 看板列上下文 → issue_* 查询参数（标签/来源为 all 时不带） */
export function buildBoardSourceSearchParams(context: BoardIssueColumnContext) {
  const params = new URLSearchParams();
  params.set("issue_from", "board");
  params.set("issue_workflow_status", context.workflowStatus);
  if (context.label && context.label !== "all") {
    params.set("issue_label", context.label);
  }
  if (context.source && context.source !== "all") {
    params.set("issue_source", context.source);
  }
  return params;
}

/**
 * 从详情页 URL 解析来源上下文。
 * issue_from=board 且 issue_workflow_status 为合法列值才算 board（非法/缺失不算）；
 * 未知 issue_from 值一律 none；无 issue_from 时出现任一 issue_* 参数即 list。
 */
const BOARD_WORKFLOW_STATUS_PARAM_LABELS: Record<
  BoardWorkflowStatusParam,
  string
> = {
  todo: "待处理",
  in_progress: "开发中",
  done: "已完成",
  unset: "未设置",
};

/**
 * 来源列表标题副行：带来源类型前缀（「看板 · …」/「列表 · …」）说明是哪一列/哪组
 * 筛选，让「在哪个上下文里翻页」一眼可辨。
 */
export function describeIssueSourceContext(context: IssueSourceContext): string {
  if (context.kind === "board") {
    const segments = ["看板", BOARD_WORKFLOW_STATUS_PARAM_LABELS[context.workflowStatus]];
    if (context.label) {
      segments.push(`标签 "${context.label}"`);
    }
    if (context.source) {
      segments.push(`来源 ${context.source}`);
    }
    return segments.join(" · ");
  }
  if (context.kind === "list") {
    const segments: string[] = [];
    if (context.state) {
      segments.push(`状态 ${context.state === "open" ? "OPEN" : "CLOSED"}`);
    }
    if (context.q) {
      segments.push(`搜索 "${context.q}"`);
    }
    if (context.label) {
      segments.push(`标签 "${context.label}"`);
    }
    if (context.source) {
      segments.push(`来源 ${context.source}`);
    }
    if (context.workflowStatus) {
      segments.push(
        `内部状态 ${
          ISSUE_WORKFLOW_STATUS_LABELS[
            context.workflowStatus as keyof typeof ISSUE_WORKFLOW_STATUS_LABELS
          ] ?? context.workflowStatus
        }`,
      );
    }
    return segments.length > 0 ? `列表 · ${segments.join(" · ")}` : "列表 · 全部";
  }
  return "";
}

export function readIssueSourceContext(
  searchParams: URLSearchParams,
): IssueSourceContext {
  const issueFrom = searchParams.get("issue_from");
  if (issueFrom) {
    if (issueFrom !== "board") {
      return { kind: "none" };
    }
    const workflowStatus = searchParams.get("issue_workflow_status") ?? "";
    if (!BOARD_WORKFLOW_STATUS_VALUES.includes(workflowStatus)) {
      return { kind: "none" };
    }
    return {
      kind: "board",
      workflowStatus: workflowStatus as BoardWorkflowStatusParam,
      label: normalizeFilterValue(searchParams.get("issue_label")),
      source: normalizeFilterValue(searchParams.get("issue_source")),
    };
  }

  const raw = {
    state: searchParams.get(ISSUE_DETAIL_SEARCH_PARAM_MAP.state),
    q: searchParams.get(ISSUE_DETAIL_SEARCH_PARAM_MAP.q),
    label: searchParams.get(ISSUE_DETAIL_SEARCH_PARAM_MAP.label),
    source: searchParams.get(ISSUE_DETAIL_SEARCH_PARAM_MAP.source),
    workflowStatus: searchParams.get(ISSUE_DETAIL_SEARCH_PARAM_MAP.workflowStatus),
    sort: searchParams.get(ISSUE_DETAIL_SEARCH_PARAM_MAP.sort),
    page: searchParams.get(ISSUE_DETAIL_SEARCH_PARAM_MAP.page),
  };
  const hasAnyParam = Object.values(raw).some((value) => value !== null);
  if (!hasAnyParam) {
    return { kind: "none" };
  }

  return {
    kind: "list",
    state: normalizeFilterValue(raw.state),
    q: (raw.q ?? "").trim(),
    label: normalizeFilterValue(raw.label),
    source: normalizeFilterValue(raw.source),
    workflowStatus: normalizeFilterValue(raw.workflowStatus),
    sort: raw.sort || "updated_desc",
    page: Math.max(Number(raw.page ?? "1") || 1, 1),
  };
}
