declare const __APP_VERSION__: string;

/** 用户角色：admin=管理员（可管成员/邀请/部署设置）；member=普通成员 */
type UserRole = "admin" | "member";

interface User {
  id: string;
  username: string;
  email: string;
  avatar_url: string;
  role: UserRole;
  /** 用户 IANA 时区（如 Asia/Shanghai）；空串表示未声明，机器人按部署配置回退 */
  timezone: string;
  /**
   * 生效时区（INT-138）：timezone 非空时为它，否则是部署配置的时区，再否则
   * UTC。只有 /auth/me 返回，成员列表省略。
   */
  effective_timezone?: string | null;
  created_at: string;
  updated_at: string;
}

/** 成员列表项（GET /api/members）：与 User 同形，按 created_at 升序 */
type Member = User;

/** 列表端点统一 `{items}` 信封，与 /api/environments 同形 */
interface MemberListData {
  items: Member[];
}

/** 邀请记录；明文 token 只在创建响应（CreatedInvite）里出现一次 */
interface Invite {
  id: string;
  expires_at: string;
  revoked_at: string | null;
  used_at: string | null;
  used_by: string | null;
  created_at: string;
}

/** 列表端点统一 `{items}` 信封，与 /api/environments 同形 */
interface InviteListData {
  items: Invite[];
}

interface CreatedInvite extends Invite {
  /** 明文 token，仅此一次下发；邀请链接由前端拼 /register?invite= */
  token: string;
}

interface CreateInviteRequest {
  /** 有效期（小时），缺省 72，允许 1..720 */
  expires_in_hours?: number;
}

interface UpdateMemberRoleRequest {
  role: UserRole;
}

/** POST /api/auth/invites/validate 的校验结果；无效邀请返回 40432 */
interface InviteValidation {
  expires_at: string;
}

interface Project {
  id: string;
  user_id: string;
  name: string;
  description: string;
  github_owner: string;
  github_repo: string;
  /** 代码仓库（PR 审查用）owner；未单独配置为空串，表示与公开仓库相同 */
  code_github_owner: string;
  /** 代码仓库（PR 审查用）repo；未单独配置为空串，表示与公开仓库相同 */
  code_github_repo: string;
  /** 公开仓库 Token 引用（Token 库记录 id）；未配置为空串 */
  public_github_token_id: string;
  /** 代码仓库 Token 引用；未配置为空串 */
  code_github_token_id: string;
  latest_version?: {
    id: string;
    version_number: string;
    status: "pending" | "shipped";
    created_at: string;
  } | null;
  /** 项目 Issue 总数（不区分状态）；仅项目列表响应携带 */
  issue_count?: number;
  /** 按内部状态（workflow_status）分组的 Issue 计数（仅统计 state=open）；仅项目列表响应携带，无 Issue 时缺省 */
  issue_workflow_counts?: {
    unset: number;
    todo: number;
    in_progress: number;
    done: number;
  };
  issue_sync?: {
    status: "idle" | "running" | "failed" | "completed";
    last_issue_updated_at?: string | null;
    last_synced_at?: string | null;
    last_successful_sync_at?: string | null;
    last_error: string;
  } | null;
  created_at: string;
  updated_at: string;
}

interface Version {
  id: string;
  project_id: string;
  version_number: string;
  status: "pending" | "shipped";
  release_notes: string | null;
  target_commitish: string | null;
  github_release_url: string | null;
  error_log: string | null;
  ship_status: "" | "in_progress" | "failed" | "completed";
  ship_stage:
    | ""
    | "precheck"
    | "create_tag"
    | "create_release"
    | "upload_assets"
    | "finalize";
  ship_message: string | null;
  ship_hooks_status?: "pending" | "completed" | "failed" | "incomplete" | null;
  created_at: string;
  shipped_at: string | null;
  artifacts?: Artifact[];
}

interface GitHubBranch {
  name: string;
  sha: string;
  default: boolean;
}

interface ProjectBranchesResponse {
  branches: GitHubBranch[];
  default_branch: string;
}

interface ShipCheckItem {
  key: string;
  label: string;
  ok: boolean;
  detail?: string;
}

interface ShipCheck {
  can_ship: boolean;
  items: ShipCheckItem[];
  pending_issue_hooks: PendingIssueHook[];
}

interface PendingIssueHook {
  issue_id: string;
  reference: string;
  title: string;
  comment: boolean;
  close: boolean;
  workflow_enabled: boolean;
  workflow_status: string;
}

interface ShipResult {
  hook_total: number;
  hook_failed: number;
  hook_status?: "completed" | "failed" | "incomplete";
  hook_error?: string;
}

interface IssueShipHookActionResult {
  ok: boolean;
  skipped?: boolean;
  error?: string;
}

interface IssueShipHookResults {
  comment?: IssueShipHookActionResult;
  close?: IssueShipHookActionResult;
  workflow_status?: IssueShipHookActionResult;
}

interface IssueShipHook {
  status: "pending" | "running" | "fired";
  comment_enabled: boolean;
  comment_body?: string;
  close_enabled: boolean;
  workflow_enabled: boolean;
  workflow_status: string;
  version_id?: string;
  version_number?: string;
  release_url?: string;
  fired_at?: string;
  results?: IssueShipHookResults;
}

interface Artifact {
  id: string;
  version_id: string;
  file_name: string;
  file_size: number;
  file_path: string;
  platform: string | null;
  uploaded_by: string | null;
  uploaded_at: string;
}

interface ApiKey {
  id: string;
  name: string;
  key_prefix: string;
  last_used_at: string | null;
  created_at: string;
}

/** 检测状态：unchecked=未检测；ok=认证成功；invalid=凭证无效（401）；failed=检测失败（网络/5xx） */
type GitHubTokenDetectionStatus = "unchecked" | "ok" | "invalid" | "failed";

/** classic=有 OAuth scopes 的 PAT；fine_grained=github_pat_ 前缀；unknown=无法判断 */
type GitHubTokenDetectionTokenType = "classic" | "fine_grained" | "unknown";

/** 用户级 GitHub Token 库记录；明文与密文均不回显 */
interface GitHubToken {
  id: string;
  title: string;
  /** 明文前 8 个字符（如 ghp_Ab12），用于列表辨识 */
  token_prefix: string;
  detection_status: GitHubTokenDetectionStatus;
  detection_token_type: GitHubTokenDetectionTokenType;
  /** Classic PAT 的 OAuth scopes；fine-grained 恒为空数组 */
  detection_scopes: string[];
  /** 认证成功时的 GitHub login，否则空串 */
  detection_login: string;
  /** 补充说明（fine-grained 的「无法自动获取完整授权」、失败原因等） */
  detection_detail: string;
  detected_at: string | null;
  /** 两个用途中任一引用了该 Token 的项目 */
  referenced_projects: Array<{ id: string; name: string }>;
  created_at: string;
  updated_at: string;
}

/** 部署级配置来源：database=网页保存（最优先）；config=config.yaml 兜底；none=未配置 */
type DeploymentSource = "database" | "config" | "none";

/** 检测状态：unchecked=未检测；ok=App 身份认证成功；invalid=GitHub 拒绝凭证（401）；failed=检测失败（网络/限流/5xx，不是凭证无效） */
type GitHubAppDetectionStatus = "unchecked" | "ok" | "invalid" | "failed";

/** 部署设置（GET /api/deployment/settings）；PUT 仅管理员 */
interface DeploymentSettings {
  /** 对外访问地址；空串表示未在数据库保存，回落部署配置 */
  public_url: string;
  public_url_source: DeploymentSource;
  /** 数据库值最近保存时间；source 为 config/none 时为 null */
  updated_at: string | null;
}

interface UpdateDeploymentSettingsRequest {
  /** http(s) 绝对 URL；空串表示清空数据库值、回落 config */
  public_url: string;
}

/**
 * 部署级 GitHub App（INT-129），整部署一份，读写见 /api/deployment/github-app；
 * 私钥与 webhook 密钥本体永不回显
 */
interface GitHubAppDeployment {
  /** 数据库或部署配置任一存在 */
  configured: boolean;
  source: DeploymentSource;
  /** 未配置时为空串 */
  app_id: string;
  /** App 的 URL slug；未配置时为空串 */
  app_slug: string;
  private_key_set: boolean;
  webhook_secret_set: boolean;
  /** {public_url}/api/github/webhooks；public_url 未配置时为空串 */
  webhook_url: string;
  /** https://github.com/apps/{slug}/installations/new；slug 为空时为空串 */
  install_url: string;
  detection_status: GitHubAppDetectionStatus;
  detection_detail: string;
  detected_at: string | null;
  /** 数据库配置最近保存时间；source 为 config/none 时为 null */
  updated_at: string | null;
}

interface UpdateGitHubAppDeploymentRequest {
  app_id: string;
  /** 省略或 null 保留已存值 */
  private_key_pem?: string | null;
  /** 省略或 null 保留已存值；空串清空 */
  webhook_secret?: string | null;
}

/** 原样 POST 给 GitHub 的 App Manifest（见 GitHub App Manifest 创建流程） */
interface GitHubAppManifest {
  name: string;
  /** public_url */
  url: string;
  hook_attributes: { url: string; active: boolean };
  /** {public_url}/settings/integrations/github/callback */
  redirect_url: string;
  /** {public_url}/settings/integrations */
  setup_url: string;
  public: boolean;
  default_events: string[];
  default_permissions: Record<string, string>;
}

interface GitHubAppManifestRequest {
  /** 填了则在指定组织下创建，否则建在个人账号下 */
  org?: string;
}

interface GitHubAppManifestResponse {
  /** https://github.com/settings/apps/new 或 .../organizations/{org}/settings/apps/new */
  action_url: string;
  state: string;
  manifest: GitHubAppManifest;
}

interface CompleteGitHubAppManifestRequest {
  code: string;
  state: string;
}

/** 项目表单的 Token 引用三态：缺省保留、空/null 清空、非空替换 */
interface UpdateProjectPayload {
  name?: string;
  description?: string;
  repository_url?: string;
  /** 代码仓库；三态同 Token 引用：缺省保留、空/null 清空（回落公开仓库）、非空替换 */
  code_repository_url?: string | null;
  public_github_token_id?: string | null;
  code_github_token_id?: string | null;
}

interface AISettings {
  api_host: string;
  model: string;
  configured: boolean;
  updated_at?: string | null;
}

interface IssueChecklistSuggestion {
  title: string;
}

interface IssueChecklistSuggestions {
  items: IssueChecklistSuggestion[];
}

interface GenerateTitleResponse {
  titles: string[];
}

interface IssueActor {
  login: string;
  avatar_url: string;
}

interface IssueLabel {
  name: string;
  color: string;
  description: string;
}

interface IssueMilestone {
  number: number;
  title: string;
  state: string;
  description: string;
}

interface IssueReactions {
  total_count: number;
  "+1": number;
  "-1": number;
  laugh: number;
  hooray: number;
  confused: number;
  heart: number;
  rocket: number;
  eyes: number;
}

interface Issue {
  id: string;
  project_id: string;
  source: "github" | "internal";
  sequence_number: number;
  reference: string;
  state: "open" | "closed";
  state_reason: string;
  title: string;
  body: string;
  body_html: string;
  author: IssueActor;
  closed_at?: string | null;
  created_at: string;
  updated_at: string;
  /** 未读 GitHub 评论数（排除发货钩子留言）。内部 Issue 恒为 0。 */
  unread_comments_count: number;
  internal_meta?: IssueInternalMeta | null;
  github?: IssueGitHubMeta | null;
  ship_hook?: IssueShipHook | null;
  /** 关联的实现 PR 明细；仅 Issue 详情响应携带，列表项不出现 */
  pull_requests?: IssuePullRequest[];
  /** 关联 PR 的聚合计数；仅列表项携带 */
  pull_request_summary?: IssuePullRequestSummary;
  /** 附件列表；仅 internal Issue 详情响应携带 */
  attachments?: IssueAttachment[];
  /** 关联到该 Issue 的截图标注；仅 Issue 详情响应携带 */
  screenshot_annotations?: ScreenshotAnnotation[];
}

/** Issue 附件记录；github 源 Issue 恒为空 */
interface IssueAttachment {
  id: string;
  file_name: string;
  file_size: number;
  mime_type: string;
  created_at: string;
  /** 用户名或 "API Key: <name>" */
  uploader: string;
  /** 附件下载路径，浏览器访问需附带 ?token= */
  download_url: string;
}

/** Issue 关联的实现 PR。PR 状态与 Issue workflow_status 不联动 */
interface IssuePullRequest {
  id: string;
  issue_id: string;
  project_id: string;
  /** 当前恒为 github；字段预留 gitlab */
  provider: string;
  /** owner/repo；允许与项目配置的仓库不同（跨仓库 attach） */
  repo_full_name: string;
  number: number;
  html_url: string;
  title: string;
  /** merged = GitHub 上 closed 且 merged_at 非空 */
  state: "open" | "closed" | "merged";
  is_draft: boolean;
  author_login: string;
  head_ref: string;
  base_ref: string;
  /** state=merged 时有值 */
  merged_at?: string | null;
  closed_at?: string | null;
  /** manual=用户显式 attach；synced=远端同步投影产生 */
  link_origin: "manual" | "synced";
  /** 最近一次从 GitHub 刷新成功的时间 */
  synced_at: string;
  created_at: string;
  updated_at: string;
}

/** Issue 关联 PR 的聚合计数；closed 计数 = total - open - merged */
interface IssuePullRequestSummary {
  total: number;
  open: number;
  merged: number;
}

/** sync 端点响应形状：逐行失败域，失败行保留旧数据 */
interface IssuePullRequestSyncResult {
  items: IssuePullRequest[];
  failures: Array<{ id: string; error: string }>;
}

interface IssueGitHubMeta {
  github_issue_id: number;
  github_node_id: string;
  number: number;
  html_url: string;
  author_association: string;
  assignees: IssueActor[];
  labels: IssueLabel[];
  milestone?: IssueMilestone | null;
  reactions: IssueReactions;
  comments_count: number;
  locked: boolean;
  active_lock_reason: string;
  synced_at: string;
}

interface IssueInternalMeta {
  workflow_status: "" | "todo" | "in_progress" | "done";
  progress_percent?: number | null;
  checklist_total: number;
  checklist_done: number;
  started_at?: string | null;
  completed_at?: string | null;
  checklist_updated_at?: string | null;
  updated_at?: string | null;
  checklist?: IssueChecklistItem[];
  labels?: IssueLabel[];
}

interface IssueChecklistItem {
  id: string;
  title: string;
  is_completed: boolean;
  sort_order: number;
}

interface IssueAsset {
  id: string;
  issue_id: string;
  file_name: string;
  mime_type: string;
  file_size: number;
  content_url: string;
  markdown: string;
  created_at: string;
}

interface IssueComment {
  id: string;
  issue_id: string;
  source: "github" | "internal";
  github_comment_id: number;
  github_node_id: string;
  body: string;
  body_html: string;
  html_url: string;
  author: IssueActor;
  author_association: string;
  reactions: IssueReactions;
  created_at: string;
  updated_at: string;
}

interface IssueTimelineEvent {
  id: string;
  issue_id: string;
  event_key: string;
  event_type: string;
  github_event_id: number;
  actor: IssueActor;
  body: string;
  summary: string;
  payload: Record<string, unknown>;
  created_at: string;
}

interface IssueSyncResult {
  project_id: string;
  synced_issue_count: number;
  synced_comment_count: number;
  synced_timeline_count: number;
  started_at: string;
  completed_at: string;
  last_issue_updated_at?: string | null;
}

interface IssueFilterOptions {
  labels: string[];
  assignees: string[];
  milestones: string[];
}

interface IssueCollabActor {
  kind: "user" | "agent";
  login: string;
  avatar_url?: string;
}

interface IssueCollabDoc {
  issue_id: string;
  body: string;
  author: IssueCollabActor;
  created_at: string;
  updated_at: string;
}

interface IssueCollabArea {
  consensus: IssueCollabDoc | null;
  summary: IssueCollabDoc | null;
}

interface RecommendationIssueSummary {
  id: string;
  project_id: string;
  project_name: string;
  source: "github" | "internal";
  sequence_number: number;
  reference: string;
  title: string;
  state: "open" | "closed";
  workflow_status: "" | "todo" | "in_progress" | "done";
}

interface RecommendationDependency {
  issue_id: string;
  title: string;
  state: "open" | "closed";
  workflow_status: "" | "todo" | "in_progress" | "done";
  project_id: string;
  sequence_number: number;
  reference: string;
}

interface IssueRecommendation {
  issue: RecommendationIssueSummary;
  reason: string;
  priority: "high" | "medium" | "low";
  status: "active" | "deferred";
  deferred_at: string | null;
  defer_note: string | null;
  created_by: string;
  created_at: string;
  updated_at: string;
  dependencies: RecommendationDependency[];
}

interface IssueRecommendationListData {
  items: IssueRecommendation[];
}

interface LogEntry {
  id: string;
  run_id: string;
  timestamp: string;
  level: "debug" | "info" | "warn" | "error" | "fatal";
  source: string;
  message: string;
  metadata?: string;
  created_at: string;
}

interface LogRun {
  project_id: string;
  run_id: string;
  source: string;
  description: string;
  entry_count: number;
  first_entry_at: string | null;
  last_entry_at: string | null;
  created_at: string;
  updated_at: string;
  uploader_api_key_id?: string | null;
}

interface DocumentListItem {
  id: string;
  project_id: string;
  parent_id: string | null;
  title: string;
  created_at: string;
  updated_at: string;
}

interface DocumentDetail {
  id: string;
  project_id: string;
  parent_id: string | null;
  title: string;
  body: string;
  created_at: string;
  updated_at: string;
}

interface DocumentListData {
  items: DocumentListItem[];
  total: number;
}

interface DashboardOverview {
  open_issues_by_project: DashboardProjectOpenIssuePoint[];
  daily_resolved: DashboardDailyResolvedPoint[];
}

interface DashboardProjectOpenIssuePoint {
  project_id: string;
  project_name: string;
  open_issue_count: number;
}

interface DashboardDailyResolvedProjectPoint {
  project_id: string;
  project_name: string;
  count: number;
}

interface DashboardDailyResolvedPoint {
  date: string;
  resolved_count: number;
  projects: DashboardDailyResolvedProjectPoint[];
}

/** 截图库：一张截图 = (screen_key, platform, locale, theme) 变体，再上传即覆盖 */
interface ScreenshotScreen {
  id: string;
  project_id: string;
  screen_key: string;
  /** 显示名；为空时回退展示 screen_key */
  title: string;
  /** 平台（开放词表小写）；null 表示未标注 */
  platform: string | null;
  /** 语言（BCP 47 规范化大小写）；null 表示未标注 */
  locale: string | null;
  /** 亮度主题；null 表示未标注 */
  theme: ScreenshotTheme | null;
  /** 上传时内容嗅探得到的类型 */
  mime_type: string;
  file_size: number;
  /** 原图宽像素；0 表示未知 */
  width: number;
  /** 原图高像素；0 表示未知 */
  height: number;
  /** 用户名或 "API Key: <name>" */
  uploaded_by: string;
  uploaded_at: string;
  created_at: string;
  /** 图片内容路径，需附带 ?token= 访问 */
  content_url: string;
}

type ScreenshotTheme = "light" | "dark";

type ScreenshotAnnotationStatus = "open" | "resolved";

/** 像素矩形（原图坐标系） */
interface ScreenshotAnnotationRect {
  x: number;
  y: number;
  width: number;
  height: number;
}

/** 挂在某张截图上的「矩形框 + 文字」标注；x/y/width/height 为图片宽高比例（0~1） */
interface ScreenshotAnnotation {
  id: string;
  project_id: string;
  screen_id: string;
  issue_id: string | null;
  issue_reference: string | null;
  issue_title: string | null;
  screen_key: string;
  /** 截图显示名；空串时回退展示 screen_key */
  screen_title: string;
  screen_platform: string | null;
  screen_locale: string | null;
  screen_theme: ScreenshotTheme | null;
  /** 所在截图的原图地址，需附带 ?token= 访问 */
  image_url: string;
  image_width: number;
  image_height: number;
  /** 截图被覆盖更新后未「确认保留」；resolved 恒为 false */
  outdated: boolean;
  x: number;
  y: number;
  width: number;
  height: number;
  pixel_rect: ScreenshotAnnotationRect | null;
  body: string;
  status: ScreenshotAnnotationStatus;
  /** 裁剪图 PNG 路径，需附带 ?token= 访问 */
  crop_url: string;
  created_by: string;
  created_at: string;
  updated_at: string;
  resolved_at: string | null;
}

interface CreateScreenshotAnnotationPayload {
  x: number;
  y: number;
  width: number;
  height: number;
  body: string;
  /** 缺省或空串 = 不关联 */
  issue_id?: string;
}

/** 指针语义：出现才更新；issue_id 空串=解除关联 */
interface UpdateScreenshotAnnotationPayload {
  body?: string;
  status?: ScreenshotAnnotationStatus;
  issue_id?: string;
}

interface ScreenshotScreenListData {
  items: ScreenshotScreen[];
}

interface ScreenshotUploadResult {
  screen: ScreenshotScreen;
}

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

interface ScreenshotAnnotationBatchResult {
  updated: number;
}

interface ApiResponse<T> {
  code: number;
  message: string;
  data: T;
}

interface PaginatedData<T> {
  items: T[];
  total: number;
  page: number;
  page_size: number;
}

/** 提示词模板的启动偏好；由「启动线程」弹框勾选「保存为此模板的默认」写入。 */
interface IssuePromptPreferences {
  /** "auto" 表示按绑定与负载自动选环境；否则为具体环境 id */
  environment?: "auto" | string;
  workspace?: "worktree" | "root";
  model_selection?: {
    instance_id: string;
    model: string;
    options?: { id: string; value: string | boolean }[];
  };
}

interface IssuePrompt {
  id: string;
  name: string;
  content: string;
  preferences?: IssuePromptPreferences;
}

interface IssuePromptSettings {
  prompts: IssuePrompt[] | null;
}

/* 套餐额度（INT-109）：字段与冻结契约 v1 §2–§5 逐字一致。 */

/** 额度窗口类型；other 用于非时间窗口（如 MCP 次数），label 由后端给准确名字 */
type QuotaWindowKind = "five_hour" | "daily" | "weekly" | "monthly" | "other";

interface QuotaWindow {
  kind: QuotaWindowKind;
  /** 面向用户的窗口名 */
  label: string;
  /** 0..100；算不出百分比时为 null */
  used_percent: number | null;
  /** 服务端 omitempty：nil 时字段整个缺省（undefined），区别于显式 null */
  used?: number | null;
  limit?: number | null;
  unit: "percent" | "usd" | "count";
  /** UTC RFC3339；无重置时间为 null */
  reset_at: string | null;
  /** 并行额度池名（INT-130）：仅同一账号有多个池时才下发（如 Factory「标准」/「Core」），其它供应商缺省 */
  pool?: string;
}

interface QuotaBalance {
  label: string;
  amount: number;
  currency: string;
}

/** 供应商凭据表单字段元数据：按它动态渲染输入框 */
interface QuotaCredentialField {
  key: string;
  label: string;
  /** true 时用 type="password" 渲染 */
  secret: boolean;
  required: boolean;
  placeholder: string;
  help: string;
}

/** GET /api/quota/providers 的 data 元素 */
interface QuotaProvider {
  id: string;
  name: string;
  /** 可选，仅供空态提示 */
  plan_hint?: string;
  credential_fields: QuotaCredentialField[];
  /** 「凭据从哪里获取」分步说明，逐条一句话 */
  instructions: string[];
  /** 会话令牌类供应商的过期警告；其余为空串 */
  warning: string;
  /** 是否支持浏览器设备授权获取凭据；true 时前端显示「使用浏览器授权」 */
  device_authorization: boolean;
}

/** ok 正常；credential_invalid 凭据失效；provider_error 接口异常；pending 暂未抓取 */
type QuotaAccountStatus = "ok" | "credential_invalid" | "provider_error" | "pending";

interface QuotaAccount {
  id: string;
  provider: string;
  provider_name: string;
  label: string;
  /** 只回显末 4 位；绝不含明文 */
  credential_hint: string;
  /** 最近一次成功快照的套餐名；未抓取过为空串 */
  plan_name: string;
  status: QuotaAccountStatus;
  /** 最近一次失败原因（脱敏后的用户可读文本） */
  last_error: string;
  /** 最近一次成功抓取时间（= 快照数据时间）；未成功过为 null */
  fetched_at: string | null;
  last_success_at: string | null;
  /** 最近一次成功快照的窗口；失败时保留旧数据 */
  windows: QuotaWindow[];
  balance: QuotaBalance | null;
  /** 供应商特有的补充说明（如「无 Design Plan 额度，按钱包计费」）；空串表示无 */
  note: string;
  created_at: string;
  updated_at: string;
}

/** /api/quota/test 的探测结果：不落库、不改账号状态 */
interface QuotaProbe {
  status: QuotaAccountStatus;
  plan_name: string;
  windows: QuotaWindow[];
  balance: QuotaBalance | null;
  /** 供应商特有的补充说明；空串表示无 */
  note: string;
  /** status != ok 时给用户可读原因 */
  error: string;
  /** 可选的更细诊断（HTTP 状态码等），可为空 */
  detail: string;
}

/**
 * 设备授权会话状态（INT-117）：
 * pending 等待用户批准；approved 已拿到控制面密钥并自动建号；
 * expired 验证码过期/本地超时；denied 用户拒绝或取消；invalid 设备密钥失效。
 */
type QuotaDeviceAuthStatus =
  | "pending"
  | "approved"
  | "expired"
  | "denied"
  | "invalid";

/** /api/quota/device-authorizations 的会话视图；deviceSecret 从不下发到前端 */
interface QuotaDeviceAuthorization {
  id: string;
  provider: string;
  status: QuotaDeviceAuthStatus;
  /** 用户在批准页面输入的验证码 */
  user_code: string;
  /** 浏览器批准页面地址（前端 target=_blank 打开） */
  activation_url: string;
  /** UTC RFC3339；过期后 GET 返回 expired */
  expires_at: string;
  /** 前端轮询节奏（秒），upstream 给的值，缺省 2 */
  poll_interval_seconds: number;
  /** approved 时新建账号的 id；其余空串 */
  account_id: string;
  /** expired/denied/invalid 时的用户可读原因；其余空串 */
  error: string;
}
