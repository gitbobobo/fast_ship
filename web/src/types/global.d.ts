declare const __APP_VERSION__: string;

interface User {
  id: string;
  username: string;
  email: string;
  avatar_url: string;
  created_at: string;
  updated_at: string;
}

interface Project {
  id: string;
  user_id: string;
  name: string;
  description: string;
  github_owner: string;
  github_repo: string;
  has_github_token: boolean;
  has_github_pr_token: boolean;
  latest_version?: {
    id: string;
    version_number: string;
    status: "pending" | "shipped";
    created_at: string;
  } | null;
  /** 项目 Issue 总数（不区分状态）；仅项目列表响应携带 */
  issue_count?: number;
  /** 按内部状态（workflow_status）分组的 Issue 计数；仅项目列表响应携带，无 Issue 时缺省 */
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

/** 截图库：一个「界面」（screen_key 标识）聚合全部历史版本截图 */
interface ScreenshotScreen {
  id: string;
  project_id: string;
  screen_key: string;
  /** 显示名；为空时回退展示 screen_key */
  title: string;
  /** 分组名；空串表示未分组 */
  group: string;
  version_count: number;
  last_uploaded_at: string;
  created_at: string;
}

interface ScreenshotVersion {
  id: string;
  screen_id: string;
  note: string;
  file_name: string;
  file_size: number;
  mime_type: string;
  /** 用户名或 "API Key: <name>" */
  uploaded_by: string;
  uploaded_at: string;
  /** 图片内容路径，需附带 ?token= 访问 */
  content_url: string;
  /** 原图宽像素；0 表示未知（存量版本未记录尺寸） */
  width: number;
  /** 原图高像素；0 表示未知 */
  height: number;
}

type ScreenshotAnnotationStatus = "open" | "resolved";

/** 像素矩形（原图坐标系） */
interface ScreenshotAnnotationRect {
  x: number;
  y: number;
  width: number;
  height: number;
}

/** 挂在某个截图版本上的「矩形框 + 文字」标注；x/y/width/height 为图片宽高比例（0~1） */
interface ScreenshotAnnotation {
  id: string;
  project_id: string;
  screen_id: string;
  version_id: string;
  issue_id: string | null;
  issue_reference: string | null;
  issue_title: string | null;
  screen_key: string;
  /** 界面显示名；空串时回退展示 screen_key */
  screen_title: string;
  /** 界面分组；空串表示未分组 */
  screen_group: string;
  /** 所在版本的原图地址，需附带 ?token= 访问 */
  image_url: string;
  image_width: number;
  image_height: number;
  /** 是否挂在界面当前最新版本上 */
  is_latest_version: boolean;
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

interface ScreenshotScreenListItem extends ScreenshotScreen {
  latest_version: ScreenshotVersion | null;
}

interface ScreenshotScreenDetail extends ScreenshotScreen {
  /** uploaded_at 倒序 */
  versions: ScreenshotVersion[];
}

interface ScreenshotScreenListData {
  items: ScreenshotScreenListItem[];
}

interface ScreenshotUploadResult {
  screen: ScreenshotScreen;
  version: ScreenshotVersion;
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

interface IssuePrompt {
  id: string;
  name: string;
  content: string;
  supports_batch: boolean;
}

interface IssuePromptSettings {
  prompts: IssuePrompt[] | null;
}
