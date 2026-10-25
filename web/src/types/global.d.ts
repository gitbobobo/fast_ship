import type { components } from "./api.gen";
import type { ModelSelection } from "@/lib/api/threads";

/**
 * 全局类型分区：
 *
 * 1. 契约类型：直接引用 api.gen.ts（由 server/api/openapi.yaml 生成，唯一事实源）。
 *    字段含义看生成类型自带的 @description，不再在手写层复抄；契约与前端
 *    预期不一致时以契约为准，改前端而不是改这里。
 * 2. 前端自有类型：没有同名/语义等价 schema 的信封、过滤参数与 UI 表单类型。
 */
declare global {
  const __APP_VERSION__: string;

  /* ---------- 认证 / 账号 / 成员 ---------- */

  type UserRole = components["schemas"]["UserRole"];
  type User = components["schemas"]["User"];
  type Member = components["schemas"]["Member"];
  type Invite = components["schemas"]["Invite"];
  type CreatedInvite = components["schemas"]["CreatedInvite"];
  type CreateInviteRequest = components["schemas"]["CreateInviteRequest"];
  type UpdateMemberRoleRequest = components["schemas"]["UpdateMemberRoleRequest"];

  /** 列表端点统一 `{items}` 信封（契约无对应 schema）。 */
  interface MemberListData {
    items: Member[];
  }

  /** 列表端点统一 `{items}` 信封，与 /api/members 同形。 */
  interface InviteListData {
    items: Invite[];
  }

  /** POST /api/auth/invites/validate 的校验结果；无效邀请返回 40432（响应内联，无 schema）。 */
  interface InviteValidation {
    expires_at: string;
  }

  /* ---------- 项目 / 版本 / 发货 ---------- */

  type Project = components["schemas"]["Project"];
  type Version = components["schemas"]["Version"];
  type Artifact = components["schemas"]["Artifact"];
  type ShipCheckItem = components["schemas"]["ShipCheckItem"];
  type PendingIssueHook = components["schemas"]["PendingIssueHook"];
  type ShipCheck = components["schemas"]["ShipCheck"];
  type ShipResult = components["schemas"]["ShipResult"];
  type IssueShipHook = components["schemas"]["IssueShipHook"];
  type IssueShipHookActionResult = components["schemas"]["IssueShipHookActionResult"];
  type IssueShipHookResults = components["schemas"]["IssueShipHookResults"];

  /** GET /projects/:id/branches 的响应（内联对象，无 schema）。 */
  interface ProjectBranchesResponse {
    branches: components["schemas"]["Branch"][];
    default_branch: string;
  }

  /* ---------- API Key / GitHub Token ---------- */

  type ApiKey = components["schemas"]["ApiKey"];
  type GitHubToken = components["schemas"]["GitHubToken"];
  /** 从 GitHubToken 契约字段抽取的字面量联合。 */
  type GitHubTokenDetectionStatus =
    components["schemas"]["GitHubToken"]["detection_status"];
  type GitHubTokenDetectionTokenType =
    components["schemas"]["GitHubToken"]["detection_token_type"];

  /* ---------- 部署设置 / 部署级 GitHub App ---------- */

  /** 部署级配置来源，从契约字段抽取。 */
  type DeploymentSource = components["schemas"]["DeploymentSettings"]["public_url_source"];
  type GitHubAppDetectionStatus =
    components["schemas"]["GitHubAppDeployment"]["detection_status"];
  type DeploymentSettings = components["schemas"]["DeploymentSettings"];
  type UpdateDeploymentSettingsRequest =
    components["schemas"]["UpdateDeploymentSettingsRequest"];
  type GitHubAppDeployment = components["schemas"]["GitHubAppDeployment"];
  type UpdateGitHubAppDeploymentRequest =
    components["schemas"]["UpdateGitHubAppDeploymentRequest"];
  type GitHubAppManifest = components["schemas"]["GitHubAppManifest"];
  type GitHubAppManifestRequest = components["schemas"]["GitHubAppManifestRequest"];
  type GitHubAppManifestResponse = components["schemas"]["GitHubAppManifestResponse"];
  type CompleteGitHubAppManifestRequest =
    components["schemas"]["CompleteGitHubAppManifestRequest"];

  /* ---------- 部署级代码仓集成（INT-166） ---------- */

  type ForgeIntegration = components["schemas"]["ForgeIntegration"];
  type ForgeIntegrationTest = components["schemas"]["ForgeIntegrationTest"];
  type CreateForgeIntegrationRequest =
    components["schemas"]["CreateForgeIntegrationRequest"];
  type UpdateForgeIntegrationRequest =
    components["schemas"]["UpdateForgeIntegrationRequest"];

  /** 契约名为 UpdateProjectRequest；字段三态见 schema 的 @description。 */
  type UpdateProjectPayload = components["schemas"]["UpdateProjectRequest"];

  /* ---------- AI 设置 / 提示词 ---------- */

  type AISettings = components["schemas"]["AISettings"];
  type IssueChecklistSuggestions = components["schemas"]["IssueChecklistSuggestions"];
  type GenerateTitleResponse = components["schemas"]["GenerateTitleResponse"];

  /**
   * 提示词偏好保留手写：契约 workspace 是 string，前端以 "worktree"|"root"
   * 收窄；model_selection 的选项值运行时可为 boolean（见 threads.ts 的
   * ModelOption 说明），契约的 T3ModelOption.value 表达不了。
   */
  interface IssuePromptPreferences {
    /** "auto" 表示按绑定与负载自动选环境；否则为具体环境 id */
    environment?: "auto" | string;
    workspace?: "worktree" | "root";
    model_selection?: ModelSelection;
  }

  interface IssuePrompt {
    id: string;
    name: string;
    content: string;
    preferences?: IssuePromptPreferences;
  }

  /** 未配置时 prompts 为 null，前端兜底默认提示词。 */
  interface IssuePromptSettings {
    prompts: IssuePrompt[] | null;
  }

  /* ---------- Issue ---------- */

  type Issue = components["schemas"]["Issue"];
  type IssueActor = components["schemas"]["IssueActor"];
  type IssueLabel = components["schemas"]["IssueLabel"];
  type IssueMilestone = components["schemas"]["IssueMilestone"];
  /** 契约名为 IssueReactionSummary。 */
  type IssueReactions = components["schemas"]["IssueReactionSummary"];
  type IssueGitHubMeta = components["schemas"]["IssueGitHubMeta"];
  type IssueInternalMeta = components["schemas"]["IssueInternalMeta"];
  type IssueChecklistItem = components["schemas"]["IssueChecklistItem"];
  type IssueAttachment = components["schemas"]["IssueAttachment"];
  type IssueAsset = components["schemas"]["IssueAsset"];
  type IssueComment = components["schemas"]["IssueComment"];

  /**
   * 契约类型 + payload 局部覆盖：yaml 把 payload 建模为无属性对象（生成
   * Record<string, never>），实际是任意事件 JSON，前端要按 event_type 读取
   * 里面的字段。
   */
  type IssueTimelineEvent =
    Omit<components["schemas"]["IssueTimelineEvent"], "payload"> & {
      payload: Record<string, unknown>;
    };

  type IssueSyncResult = components["schemas"]["IssueSyncResult"];
  type IssueFilterOptions = components["schemas"]["IssueFilterOptions"];
  type IssuePullRequest = components["schemas"]["IssuePullRequest"];
  type IssuePullRequestSummary = components["schemas"]["IssuePullRequestSummary"];
  type IssuePullRequestSyncResult = components["schemas"]["IssuePullRequestSyncResult"];
  type IssueCollabDoc = components["schemas"]["IssueCollabDoc"];
  type IssueCollabArea = components["schemas"]["IssueCollabArea"];

  /** 协作区作者（契约把它内联在 IssueCollabDoc.author 里，直接取内联类型）。 */
  type IssueCollabActor = components["schemas"]["IssueCollabDoc"]["author"];

  /* ---------- Issue 推荐 ---------- */

  type RecommendationIssueSummary = components["schemas"]["RecommendationIssueSummary"];
  type RecommendationDependency = components["schemas"]["RecommendationDependency"];
  type IssueRecommendation = components["schemas"]["IssueRecommendation"];

  /** 列表端点统一 `{items}` 信封（契约无对应 schema）。 */
  interface IssueRecommendationListData {
    items: IssueRecommendation[];
  }

  /* ---------- 日志 / 文档 / 看板 ---------- */

  type LogEntry = components["schemas"]["LogEntry"];
  type LogRun = components["schemas"]["LogRun"];
  type DocumentListItem = components["schemas"]["DocumentListItem"];
  /** 契约名为 Document（DocumentListItem + body）。 */
  type DocumentDetail = components["schemas"]["Document"];

  /** 文档列表 data 为 {items, total}，非分页信封（契约内联，无 schema）。 */
  interface DocumentListData {
    items: DocumentListItem[];
    total: number;
  }

  type DashboardOverview = components["schemas"]["DashboardOverview"];
  type DashboardProjectOpenIssuePoint =
    components["schemas"]["DashboardProjectOpenIssuePoint"];
  type DashboardDailyResolvedProjectPoint =
    components["schemas"]["DashboardDailyResolvedProjectPoint"];
  type DashboardDailyResolvedPoint =
    components["schemas"]["DashboardDailyResolvedPoint"];

  /* ---------- 截图库 / 标注 ---------- */

  type ScreenshotScreen = components["schemas"]["ScreenshotScreen"];
  /** 从 ScreenshotScreen.theme 抽取（契约同字段含 null）。 */
  type ScreenshotTheme = NonNullable<components["schemas"]["ScreenshotScreen"]["theme"]>;
  type ScreenshotAnnotationStatus = components["schemas"]["ScreenshotAnnotationStatus"];
  type ScreenshotAnnotationRect = components["schemas"]["ScreenshotAnnotationRect"];
  type ScreenshotAnnotation = components["schemas"]["ScreenshotAnnotation"];
  /** 契约名为 CreateScreenshotAnnotationRequest。 */
  type CreateScreenshotAnnotationPayload =
    components["schemas"]["CreateScreenshotAnnotationRequest"];
  /** 契约名为 UpdateScreenshotAnnotationRequest（指针语义）。 */
  type UpdateScreenshotAnnotationPayload =
    components["schemas"]["UpdateScreenshotAnnotationRequest"];
  type ScreenshotUploadResult = components["schemas"]["ScreenshotUploadResult"];

  /** 截图列表 data 为 {items}（契约内联，无 schema）。 */
  interface ScreenshotScreenListData {
    items: ScreenshotScreen[];
  }

  /**
   * 批量动作请求。保留手写：filter.theme 从 URL 自由文本解析（仅 __none__ 哨兵
   * 归位），契约字面量联合 ("light"|"dark"|"__none__")[] 需要运行时校验或强转
   * 才能满足；宽类型无害，服务端仍会校验。
   */
  interface ScreenshotAnnotationBatchPayload {
    action: "confirm" | "resolve";
    filter?: {
      platform?: string[];
      locale?: string[];
      theme?: string[];
      status?: ScreenshotAnnotationStatus;
      outdated?: boolean | null;
      screen_id?: string;
    };
  }

  type ScreenshotAnnotationBatchResult =
    components["schemas"]["ScreenshotAnnotationBatchResult"];

  /* ---------- 通用信封 ---------- */

  /** 统一 JSON 信封；data 形状随端点变化，用泛型由调用点收窄。 */
  interface ApiResponse<T> {
    code: number;
    message: string;
    data: T;
  }

  /** 分页信封；items 形状由调用点收窄。 */
  interface PaginatedData<T> {
    items: T[];
    total: number;
    page: number;
    page_size: number;
  }

  /* ---------- 套餐额度（INT-109） ---------- */

  type QuotaWindow = components["schemas"]["QuotaWindow"];
  type QuotaWindowKind = components["schemas"]["QuotaWindow"]["kind"];
  type QuotaBalance = components["schemas"]["QuotaBalance"];
  type QuotaCredentialField = components["schemas"]["QuotaCredentialField"];
  type QuotaProvider = components["schemas"]["QuotaProvider"];
  type QuotaAccount = components["schemas"]["QuotaAccount"];
  type QuotaAccountStatus = components["schemas"]["QuotaAccount"]["status"];
  type QuotaProbe = components["schemas"]["QuotaProbe"];
  type QuotaDeviceAuthorization = components["schemas"]["QuotaDeviceAuthorization"];
  type QuotaDeviceAuthStatus =
    components["schemas"]["QuotaDeviceAuthorization"]["status"];
}
