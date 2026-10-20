// 工具调用的本地化展示映射表：调用卡/确认卡只查表渲染，不展示原始
// 工具标识（原始 tool.action 收进「查看原始调用」）。
// 唯一事实源是 server/internal/service/bot_tools.go 的 botToolRegistryV8()
// 与 botReadOnlyActions——后端新增或改名工具/action 时必须同步此表。
// fs_schedule 的 create、涉及 instruction/needs_report/model 的 update、
// run（dry_run=true 预览除外）、enable、delete 需要用户确认后异步执行；
// list/runs/disable 与只改 name/cron/timezone 的 update、dry_run 预览仍是
// 同步。同步不等于只读（disable/state_set 都写），effect 不能靠
// requires_confirmation 推断。表外条目一律安全回退：账本可能先于界面
// 升级，展示层不得抛错。

export type ToolEffect = "read" | "write";

interface ToolActionMeta {
  label: string;
  effect: ToolEffect;
}

const TOOL_ACTION_META: Record<string, Record<string, ToolActionMeta>> = {
  fs_project: {
    list: { label: "列出项目", effect: "read" },
    get: { label: "查看项目", effect: "read" },
    create: { label: "创建项目", effect: "write" },
    update: { label: "更新项目", effect: "write" },
  },
  fs_issue: {
    list: { label: "列出问题", effect: "read" },
    get: { label: "查看问题", effect: "read" },
    comments: { label: "查看问题评论", effect: "read" },
    timeline: { label: "查看问题时间线", effect: "read" },
    create: { label: "创建问题", effect: "write" },
    update: { label: "更新问题", effect: "write" },
    set_workflow: { label: "设置工作流状态", effect: "write" },
    replace_checklist: { label: "更新清单（整组替换）", effect: "write" },
    comment: { label: "添加评论", effect: "write" },
  },
  fs_thread: {
    list: { label: "列出线程", effect: "read" },
    get: { label: "查看线程", effect: "read" },
    item: { label: "查看线程条目", effect: "read" },
    composer_options: { label: "查看启动选项", effect: "read" },
    workspace_options: { label: "查看工作区选项", effect: "read" },
    launch: { label: "启动线程", effect: "write" },
    send: { label: "向线程发送消息", effect: "write" },
    stop: { label: "停止线程运行", effect: "write" },
  },
  fs_environment: {
    list: { label: "列出环境", effect: "read" },
    projects: { label: "查看环境项目", effect: "read" },
  },
  fs_schedule: {
    list: { label: "列出定时任务", effect: "read" },
    runs: { label: "查看任务运行记录", effect: "read" },
    status: { label: "查看调度状态", effect: "read" },
    run: { label: "立即运行定时任务", effect: "write" },
    state_get: { label: "读取任务状态", effect: "read" },
    state_set: { label: "写入任务状态", effect: "write" },
    create: { label: "创建定时任务", effect: "write" },
    update: { label: "更新定时任务", effect: "write" },
    delete: { label: "删除定时任务", effect: "write" },
    enable: { label: "启用定时任务", effect: "write" },
    disable: { label: "停用定时任务", effect: "write" },
  },
  fs_memory: {
    status: { label: "查看记忆状态", effect: "read" },
    list: { label: "列出记忆文件", effect: "read" },
    read: { label: "读取记忆", effect: "read" },
    history: { label: "查看记忆历史", effect: "read" },
    create: { label: "创建记忆文件", effect: "write" },
    replace: { label: "重写记忆文件", effect: "write" },
    append: { label: "追加记忆内容", effect: "write" },
    delete: { label: "删除记忆文件", effect: "write" },
    restore: { label: "恢复记忆版本", effect: "write" },
  },
  fs_memory_search: {
    search: { label: "搜索记忆", effect: "read" },
  },
  fs_help: {
    read: { label: "查看使用说明", effect: "read" },
  },
  fs_result: {
    read: { label: "读取完整结果", effect: "read" },
  },
  fs_stop: {
    stop: { label: "停止当前运行", effect: "write" },
  },
  fs_cancel: {
    cancel: { label: "取消后台调用", effect: "write" },
  },
};

/** action 的中文标签；表外回退为原始 `tool.action`，不抛错。 */
export function toolActionLabel(toolName: string, action: string): string {
  return TOOL_ACTION_META[toolName]?.[action]?.label ?? `${toolName}.${action}`;
}

/** 副作用类型；未知条目保守按写入处理。 */
export function toolActionEffect(toolName: string, action: string): ToolEffect {
  return TOOL_ACTION_META[toolName]?.[action]?.effect ?? "write";
}

export const TOOL_EFFECT_LABELS: Record<ToolEffect, string> = {
  read: "只读",
  write: "写入",
};

/** 副作用徽标样式：只读安静、写入用蓝色，与「需确认」的琥珀色确认卡区分开。 */
export const TOOL_EFFECT_BADGE: Record<ToolEffect, string> = {
  read: "bg-muted text-muted-foreground",
  write: "bg-blue-500/15 text-blue-700 dark:text-blue-400",
};

export interface ToolParamChip {
  /** 参数名（同时当渲染 key）。 */
  key: string;
  /** 参数的中文名。 */
  label: string;
  /** 展示值（ID 已缩写、长文本已截断）。 */
  value: string;
  /** 完整值，进 title。 */
  title?: string;
  /** 站内路由；拼不出可靠路由时省略。 */
  href?: string;
}

const PARAM_LABELS: Record<string, string> = {
  id: "ID",
  project_id: "项目",
  issue_id: "问题",
  thread_id: "线程",
  env_id: "环境",
  task_id: "任务",
  call_id: "调用",
  item_id: "条目",
  name: "名称",
  title: "标题",
  query: "关键词",
  path: "路径",
  prefix: "路径前缀",
  path_prefix: "路径前缀",
  cron: "cron",
  timezone: "时区",
  instruction: "指令",
  model: "模型",
  text: "消息",
  body: "正文",
  content: "正文",
  comment_body: "评论",
  description: "描述",
  repository_url: "仓库",
  state: "状态",
  status: "状态",
  workflow: "工作流",
  workflow_status: "工作流",
  label: "标签",
  labels: "标签",
  source: "来源",
  sort: "排序",
  state_reason: "原因",
  items: "清单",
  version: "版本",
  needs_report: "需汇报",
  settled: "已收合",
  include_archived: "含归档",
  updated_after: "更新晚于",
};

// ID 型参数：展示为前 8 位短标识，完整值进 title。
const ID_PARAMS = new Set([
  "id",
  "project_id",
  "issue_id",
  "thread_id",
  "env_id",
  "task_id",
  "call_id",
  "item_id",
  "run_id",
]);

// 各工具进 chips 的参数（按声明顺序）；不在表内的参数、action 自身与
// 分页/乐观锁字段（page/limit/offset/cursor/expected_* 等）默认折叠进
// 「查看原始调用」。
const TOOL_CHIP_PARAMS: Record<string, string[]> = {
  fs_project: ["id", "name", "description", "repository_url"],
  fs_issue: [
    "project_id",
    "issue_id",
    "title",
    "query",
    "state",
    "workflow_status",
    "workflow",
    "state_reason",
    "label",
    "labels",
    "items",
    "comment_body",
    "body",
    "source",
    "sort",
  ],
  fs_thread: [
    "env_id",
    "thread_id",
    "item_id",
    "project_id",
    "text",
    "title",
    "status",
    "settled",
    "include_archived",
    "updated_after",
  ],
  fs_environment: [],
  fs_schedule: [
    "task_id",
    "name",
    "cron",
    "timezone",
    "instruction",
    "model",
    "needs_report",
  ],
  fs_memory: ["path", "prefix", "version", "content"],
  fs_memory_search: ["query", "path_prefix"],
  fs_help: [],
  fs_result: ["call_id"],
  fs_stop: [],
  fs_cancel: ["call_id"],
};

// 未知工具的兜底：只挑语义明确的通用字段，宁少勿滥。
const GENERIC_CHIP_PARAMS = [
  "project_id",
  "issue_id",
  "thread_id",
  "env_id",
  "task_id",
  "call_id",
  "item_id",
  "id",
  "name",
  "title",
  "query",
  "path",
  "cron",
  "text",
  "instruction",
];

function truncateText(text: string, max = 40): string {
  const flat = text.replace(/\s+/g, " ").trim();
  return flat.length > max ? `${flat.slice(0, max)}…` : flat;
}

function chipRoute(
  key: string,
  value: string,
  args: Record<string, unknown>,
): string | undefined {
  // 站内路由以 App.tsx 为准：issue 详情需要 project_id，线程详情需要 env_id；
  // 其余 ID 没有对应详情页，只显示短标识不造坏链。
  if (key === "issue_id" && typeof args.project_id === "string" && args.project_id) {
    return `/projects/${encodeURIComponent(args.project_id)}/issues/${encodeURIComponent(value)}`;
  }
  if (key === "thread_id" && typeof args.env_id === "string" && args.env_id) {
    return `/threads/${encodeURIComponent(args.env_id)}/${encodeURIComponent(value)}`;
  }
  return undefined;
}

function chipValue(
  key: string,
  raw: unknown,
  args: Record<string, unknown>,
): ToolParamChip | null {
  const label = PARAM_LABELS[key] ?? key;
  if (raw === undefined || raw === null || raw === "") return null;

  if (typeof raw === "string") {
    if (ID_PARAMS.has(key)) {
      const short = raw.length > 8 ? `${raw.slice(0, 8)}…` : raw;
      return { key, label, value: short, title: raw, href: chipRoute(key, raw, args) };
    }
    return { key, label, value: truncateText(raw), title: raw };
  }
  if (typeof raw === "number" || typeof raw === "boolean") {
    return { key, label, value: typeof raw === "boolean" ? (raw ? "是" : "否") : String(raw) };
  }
  if (Array.isArray(raw)) {
    if (raw.length === 0) return null;
    if (raw.every((v) => typeof v === "string")) {
      const joined = raw.join("、");
      return { key, label, value: truncateText(joined), title: joined };
    }
    // 结构化数组（如 replace_checklist 的 items）只给数量摘要，
    // 完整内容看确认卡/原始调用里的 JSON。
    return { key, label, value: `${raw.length} 项` };
  }
  try {
    const json = JSON.stringify(raw);
    return { key, label, value: truncateText(json), title: json };
  } catch {
    return { key, label, value: String(raw) };
  }
}

/**
 * 把一次调用的关键参数摘要成 chips：chips 名单按工具在 TOOL_CHIP_PARAMS
 * 里声明，action 自身与分页/乐观锁等次要字段本就不在名单内，默认折叠在
 * 「查看原始调用」里。ID 型缩写为短标识（能拼出站内外路由的给 href），
 * 长文本截断；未知工具回退到通用字段集，参数缺失/为空时不生成 chip。
 */
export function toolParamChips(
  toolName: string,
  args: Record<string, unknown> | null | undefined,
): ToolParamChip[] {
  if (!args) return [];
  const keys = TOOL_CHIP_PARAMS[toolName] ?? GENERIC_CHIP_PARAMS;
  const chips: ToolParamChip[] = [];
  for (const key of keys) {
    const chip = chipValue(key, args[key], args);
    if (chip) chips.push(chip);
  }
  return chips;
}
