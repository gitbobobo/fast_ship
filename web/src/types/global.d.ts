interface User {
  id: string;
  username: string;
  email: string;
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
  latest_version?: {
    id: string;
    version_number: string;
    status: "pending" | "shipped";
    created_at: string;
  } | null;
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
  created_at: string;
  shipped_at: string | null;
  artifacts?: Artifact[];
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
  internal_meta?: IssueInternalMeta | null;
  github?: IssueGitHubMeta | null;
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
