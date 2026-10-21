import { api, decodeApiError } from "./client";

// PR 审查（INT-135）前端类型：与 server/api/openapi.yaml 的 Review* schemas 对应。

/** T3 模型选项，canonical `{id,value}` 数组形式（与 T3ModelSelection.options 同形）。 */
export interface ReviewModelOption {
  id: string;
  value: string | boolean;
}

/** 一个审查组合：环境 + harness（instance_id）+ 模型 + 选项（思考强度等）。 */
export interface ReviewCombination {
  environment_id: string;
  /** T3 provider 实例 id（harness 维度）。 */
  instance_id: string;
  model: string;
  /** 模型选项（如 reasoningEffort），原样交给 T3。 */
  options?: ReviewModelOption[];
  /** 仅响应；环境当前名称。 */
  environment_name?: string;
  /** 仅响应；绑定的 T3 项目 id（保存时快照）。 */
  t3_project_id?: string;
}

export type ReviewPrerequisiteCode =
  | "github_app"
  | "github_app_installation"
  | "public_url"
  | "github_token"
  | "environment_binding";

export interface ReviewPrerequisite {
  code: ReviewPrerequisiteCode;
  label: string;
  missing: boolean;
  detail?: string;
}

export interface ReviewSettings {
  project_id: string;
  /** 仅响应；审查实际使用的仓库（owner/repo，原样大小写）。 */
  repo_full_name: string;
  enabled: boolean;
  trigger_opened: boolean;
  trigger_synchronize: boolean;
  trigger_ready_for_review: boolean;
  trigger_reopened: boolean;
  code_combinations: ReviewCombination[];
  visual_combinations: ReviewCombination[];
  prerequisites: ReviewPrerequisite[];
  /** 同仓库已开启审查的其他项目占用者登录名（只读）。 */
  taken_by?: string | null;
  can_manage?: boolean;
}

export interface ReviewSettingsInput {
  enabled: boolean;
  trigger_opened: boolean;
  trigger_synchronize: boolean;
  trigger_ready_for_review: boolean;
  trigger_reopened: boolean;
  code_combinations: ReviewCombination[];
  visual_combinations: ReviewCombination[];
}

export interface ReviewProjectSummary {
  project_id: string;
  project_name: string;
  github_owner: string;
  github_repo: string;
  enabled: boolean;
  code_combination_count: number;
  visual_combination_count: number;
  /** 最近一次代码审查结论（success/failure/error/pending）。 */
  code_status?: string | null;
  visual_status?: string | null;
  last_review_at?: string | null;
  taken_by?: string | null;
  can_manage: boolean;
}

export type ReviewRunStatus =
  | "queued"
  | "dispatched"
  | "running"
  | "completed"
  | "failed"
  | "timed_out"
  | "cancelled";

export type ReviewRunKind = "code" | "visual";

export interface ReviewRunSummary {
  id: string;
  kind: ReviewRunKind;
  environment_id: string;
  environment_name?: string;
  instance_id: string;
  model: string;
  options?: ReviewModelOption[];
  status: ReviewRunStatus;
  thread_id: string;
  summary?: string;
  error_code?: string;
  error_message?: string;
  /** P0/P1/P2（代码）或 blocker/worth fixing（视觉）的条目数。 */
  findings_count?: number;
  /** 完整发现列表：代码含 file/line，视觉含 screenshot/dimension/fix。 */
  findings?: ReviewFinding[];
  /** 视觉审查「无法判断」项。 */
  unmeasurable?: ReviewUnmeasurable[];
  ui_changed?: boolean;
  worktree_state?: string | null;
  started_at?: string | null;
  finished_at?: string | null;
}

export interface ReviewRoundRecord {
  round_number: number;
  head_sha: string;
  /** 该轮第一条代码 run 的触发动作；没有代码 run 时服务端回退 "unknown"。 */
  trigger_action: string;
  created_at: string;
  code_runs: ReviewRunSummary[];
  visual_runs: ReviewRunSummary[];
}

export interface ReviewPullRequestRecord {
  repo_full_name: string;
  pr_number: number;
  pr_title: string;
  pr_url: string;
  head_sha: string;
  code_status?: string | null;
  visual_status?: string | null;
  updated_at: string;
  /** 记录是否属于项目当前仓库；跨仓库的历史记录只能查看。 */
  can_rerun: boolean;
  rounds: ReviewRoundRecord[];
}

export interface ReviewRerunResult {
  accepted: boolean;
  head_sha: string;
  round_number: number;
}

/** 单条审查发现：代码 level=P0/P1/P2/Note，视觉 level=blocker/worth_fixing/minor。 */
export interface ReviewFinding {
  level: string;
  body: string;
  file?: string;
  line?: number;
  dimension?: string;
  fix?: string;
  screenshot?: string;
}

/** 视觉审查「无法判断」的截图项。 */
export interface ReviewUnmeasurable {
  screenshot: string;
  reason: string;
}

/* ---------------- /reviews 首页 PR 动态（INT-141） ---------------- */

export type ReviewPullRequestGroup =
  | "in_progress"
  | "needs_action"
  | "recently_passed";

/** 进行中细分阶段；仅 in_progress 组的行返回。 */
export type ReviewActivityStage = "debouncing" | "queued" | "reviewing";

export type ReviewStageStatus =
  | "pending"
  | "queued"
  | "running"
  | "completed"
  | "failed"
  | "skipped";

/** 一段（代码/视觉）的聚合进度，前端进度文案的唯一数据源。 */
export interface ReviewStageProgress {
  kind: ReviewRunKind;
  total: number;
  completed: number;
  status: ReviewStageStatus;
}

/** 当前轮发现计数；JSON key 与后端一致（P0/P1/P2 大写）。 */
export interface ReviewFindingCounts {
  P0: number;
  P1: number;
  P2: number;
  blocker: number;
  worth_fixing: number;
}

/** 需要处理的原因行：失败的组合与可读原因；账本兜底项 combination 为 null。 */
export interface ReviewActivityIssue {
  kind: ReviewRunKind;
  combination?: string | null;
  reason: string;
  message: string;
}

/** 跨项目 PR 动态行；分组与阶段由服务端算好，前端只负责展示。 */
export interface ReviewPullRequestActivity {
  project_id: string;
  project_name: string;
  repo_full_name: string;
  pr_number: number;
  pr_title: string;
  pr_url: string;
  group: ReviewPullRequestGroup;
  stage?: ReviewActivityStage | null;
  debounce_fire_at?: string | null;
  round_number: number;
  head_sha: string;
  updated_at: string;
  started_at: string;
  stages: ReviewStageProgress[];
  findings: ReviewFindingCounts;
  issues: ReviewActivityIssue[];
  can_rerun: boolean;
}

export interface ReviewPullRequestActivityResult {
  items: ReviewPullRequestActivity[];
  counts: {
    in_progress: number;
    needs_action: number;
    recently_passed: number;
  };
  total: number;
}

const projectPath = (projectId: string) =>
  `projects/${encodeURIComponent(projectId)}`;

export const reviewApi = {
  listProjects: () =>
    decodeApiError(
      api
        .get("reviews")
        .json<ApiResponse<{ items: ReviewProjectSummary[] }>>(),
    ),

  getSettings: (projectId: string) =>
    decodeApiError(
      api
        .get(`${projectPath(projectId)}/review-settings`)
        .json<ApiResponse<ReviewSettings>>(),
    ),

  /** 全量替换保存；启用时服务端校验前置条件与组合。 */
  updateSettings: (projectId: string, input: ReviewSettingsInput) =>
    decodeApiError(
      api
        .put(`${projectPath(projectId)}/review-settings`, { json: input })
        .json<ApiResponse<ReviewSettings>>(),
    ),

  /** 跨项目 PR 动态；分组由服务端算好。 */
  listPullRequests: (params: {
    projectId?: string;
    limit?: number;
    offset?: number;
  }) =>
    decodeApiError(
      api
        .get("reviews/pull-requests", {
          searchParams: {
            ...(params.projectId ? { project_id: params.projectId } : {}),
            limit: params.limit ?? 50,
            offset: params.offset ?? 0,
          },
        })
        .json<ApiResponse<ReviewPullRequestActivityResult>>(),
    ),

  /** 单个 PR 的审查详情（与 review-records 同结构，含 findings/unmeasurable）。 */
  getPullRequestDetail: (projectId: string, prNumber: number) =>
    decodeApiError(
      api
        .get(`reviews/pull-requests/${prNumber}`, {
          searchParams: { project_id: projectId },
        })
        .json<ApiResponse<ReviewPullRequestRecord>>(),
    ),

  /** 对 PR 的实时 HEAD 重跑代码审查；带上记录所属仓库，服务端据此拒绝历史仓库。 */
  rerun: (projectId: string, prNumber: number, repoFullName: string) =>
    decodeApiError(
      api
        .post(`${projectPath(projectId)}/reviews/rerun`, {
          json: { pr_number: prNumber, repo_full_name: repoFullName },
        })
        .json<ApiResponse<ReviewRerunResult>>(),
    ),
};

/** commit status 结论（code_status/visual_status）的中文标签。 */
export function reviewConclusionLabel(status: string | null | undefined): string {
  switch (status) {
    case "success":
      return "通过";
    case "failure":
      return "发现问题";
    case "error":
      return "未完成";
    case "pending":
      return "审查中";
    default:
      return status || "—";
  }
}

/** 单次运行的状态标签。 */
export function reviewRunStatusLabel(status: ReviewRunStatus): string {
  switch (status) {
    case "queued":
      return "排队中";
    case "dispatched":
      return "已派发";
    case "running":
      return "运行中";
    case "completed":
      return "已完成";
    case "failed":
      return "已失败";
    case "timed_out":
      return "已超时";
    case "cancelled":
      return "已取消";
    default:
      return status;
  }
}

/** PR 动态分组标签（服务端已算好分组，这里只做文案）。 */
export function reviewGroupLabel(group: ReviewPullRequestGroup): string {
  switch (group) {
    case "in_progress":
      return "进行中";
    case "needs_action":
      return "需要处理";
    case "recently_passed":
      return "最近通过";
  }
}

/** 进行中阶段标签：防抖中 / 排队中 / 审查中。 */
export function reviewActivityStageLabel(
  stage: ReviewActivityStage | null | undefined,
): string {
  switch (stage) {
    case "debouncing":
      return "防抖中";
    case "queued":
      return "排队中";
    case "reviewing":
      return "审查中";
    default:
      return "进行中";
  }
}

/** 一轮审查的触发方式标签（trigger_action 取自该轮第一条代码 run）。 */
export function reviewTriggerActionLabel(
  action: string | null | undefined,
): string {
  switch (action) {
    case "synchronize":
      return "推送";
    case "opened":
      return "打开";
    case "ready_for_review":
      return "转为可审查";
    case "reopened":
      return "重新打开";
    case "manual":
      return "手动";
    case "visual":
      return "视觉审查";
    case "unknown":
      return "未知";
    default:
      return action || "未知";
  }
}

/**
 * 单段进度文案（不含「代码审查 / 视觉审查」前缀）：
 * 如「1/2 完成」「等待代码审查」「未启动」。
 */
export function reviewStageStatusText(stage: ReviewStageProgress): string {
  switch (stage.status) {
    case "running":
      return `${stage.completed}/${stage.total} 完成`;
    case "queued":
      return "排队中";
    case "completed":
      return "已完成";
    case "failed":
      return "未通过";
    case "skipped":
      return "未启动";
    case "pending":
    default:
      // 视觉 pending：代码段未就绪或等待衔接；代码 pending 理论上不出现
      // （没有当前轮 run 的 PR 不会返回），同样按等待处理。
      return stage.kind === "visual" ? "等待代码审查" : "等待中";
  }
}

/** 两段进度文案：「代码审查 1/2 完成 → 视觉审查 等待代码审查」。 */
export function reviewStagesText(stages: ReviewStageProgress[]): string {
  const kindLabel = (kind: ReviewRunKind) =>
    kind === "visual" ? "视觉审查" : "代码审查";
  const codeDone = stages.some(
    (s) => s.kind === "code" && s.status === "completed",
  );
  return stages
    .map((s) => {
      // 代码已完成、视觉段仍在 pending：是视觉衔接窗口，不是「等待代码审查」。
      if (s.kind === "visual" && s.status === "pending" && codeDone) {
        return "视觉审查 等待视觉审查衔接";
      }
      return `${kindLabel(s.kind)} ${reviewStageStatusText(s)}`;
    })
    .join(" → ");
}

/** 发现计数文案：「P0 ×1，P1 ×2」「视觉问题 ×N」；无发现返回空串。 */
export function reviewFindingsText(counts: ReviewFindingCounts): string {
  const parts: string[] = [];
  if (counts.P0 > 0) parts.push(`P0 ×${counts.P0}`);
  if (counts.P1 > 0) parts.push(`P1 ×${counts.P1}`);
  if (counts.P2 > 0) parts.push(`P2 ×${counts.P2}`);
  const visual = counts.blocker + counts.worth_fixing;
  if (visual > 0) parts.push(`视觉问题 ×${visual}`);
  return parts.join("，");
}
