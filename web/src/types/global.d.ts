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
