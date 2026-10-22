import type { components } from "@/types/api.gen";
import { api, decodeApiError } from "./client";

// PR 审查（INT-135）前端类型：与契约形状等价的直接引用 api.gen.ts
//（server/api/openapi.yaml 生成，字段含义看其 @description）；字面量联合
// 用契约枚举字段下标抽取。ReviewRerunResult 是契约内联响应，保留手写。

type Schemas = components["schemas"];

/** T3 模型选项，canonical `{id,value}` 数组形式（与 T3ModelSelection.options 同形）。 */
export type ReviewModelOption = Schemas["ReviewModelOption"];

/** 一个审查组合：环境 + harness（instance_id）+ 模型 + 选项（思考强度等）。 */
export type ReviewCombination = Schemas["ReviewCombination"];

export type ReviewPrerequisiteCode = Schemas["ReviewPrerequisite"]["code"];

export type ReviewPrerequisite = Schemas["ReviewPrerequisite"];

export type ReviewSettings = Schemas["ReviewSettings"];

export type ReviewSettingsInput = Schemas["ReviewSettingsInput"];

export type ReviewProjectSummary = Schemas["ReviewProjectSummary"];

export type ReviewRunStatus = Schemas["ReviewRunSummary"]["status"];

export type ReviewRunKind = Schemas["ReviewRunSummary"]["kind"];

export type ReviewRunSummary = Schemas["ReviewRunSummary"];

export type ReviewRoundRecord = Schemas["ReviewRoundRecord"];

export type ReviewPullRequestRecord = Schemas["ReviewPullRequestRecord"];

/** rerun 端点的 data 是契约内联对象，无独立 schema。 */
export interface ReviewRerunResult {
  accepted: boolean;
  head_sha: string;
  round_number: number;
}

/** 单条审查发现：代码 level=P0/P1/P2/Note，视觉 level=blocker/worth_fixing/minor。 */
export type ReviewFinding = Schemas["ReviewFinding"];

/** 视觉审查「无法判断」的截图项。 */
export type ReviewUnmeasurable = Schemas["ReviewUnmeasurable"];

/* ---------------- /reviews 首页 PR 动态（INT-141） ---------------- */

export type ReviewPullRequestGroup =
  Schemas["ReviewPullRequestActivity"]["group"];

/** 进行中细分阶段；仅 in_progress 组的行返回。 */
export type ReviewActivityStage = NonNullable<
  Schemas["ReviewPullRequestActivity"]["stage"]
>;

export type ReviewStageStatus = Schemas["ReviewStageProgress"]["status"];

/** 一段（代码/视觉）的聚合进度，前端进度文案的唯一数据源。 */
export type ReviewStageProgress = Schemas["ReviewStageProgress"];

/** 当前轮发现计数；JSON key 与后端一致（P0/P1/P2 大写）。 */
export type ReviewFindingCounts = Schemas["ReviewFindingCounts"];

/** 需要处理的原因行：失败的组合与可读原因；账本兜底项 combination 为 null。 */
export type ReviewActivityIssue = Schemas["ReviewActivityIssue"];

/** 跨项目 PR 动态行；分组与阶段由服务端算好，前端只负责展示。 */
export type ReviewPullRequestActivity = Schemas["ReviewPullRequestActivity"];

/** 契约名为 ReviewPullRequestActivityPage。 */
export type ReviewPullRequestActivityResult =
  Schemas["ReviewPullRequestActivityPage"];

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
