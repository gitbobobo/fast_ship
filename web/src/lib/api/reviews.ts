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
  ui_changed?: boolean;
  worktree_state?: string | null;
  started_at?: string | null;
  finished_at?: string | null;
}

export interface ReviewRoundRecord {
  round_number: number;
  head_sha: string;
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

  listRecords: (projectId: string, limit?: number) =>
    decodeApiError(
      api
        .get(`${projectPath(projectId)}/review-records`, {
          searchParams: limit ? { limit } : undefined,
        })
        .json<ApiResponse<{ items: ReviewPullRequestRecord[] }>>(),
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
