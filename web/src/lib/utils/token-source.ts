import { hasGitHubRepo, repoSlug } from "./github";

export type PrTokenSourceKind = "access" | "pr";

export interface PrTokenSourceOption {
  /** Select 项的复合值 `<projectId>:<kind>`，选中即同时确定源项目与凭证种类 */
  value: string;
  projectId: string;
  kind: PrTokenSourceKind;
  label: string;
}

// Access Token「从已有项目复制」候选：只列确实配置了 Access Token 的项目，
// 避免选到无凭证项目后静默提交空密文。
export function accessTokenSourceProjects(
  projects: Project[],
  excludeProjectId?: string,
): Project[] {
  return projects.filter(
    (p) => p.has_github_token && p.id !== excludeProjectId,
  );
}

// PR Token「从已有项目复制」下拉行：按「项目 × 凭证」平铺——项目的
// Access Token 可兼作 PR 访问凭证，与专属 PR Token 各占一行。
export function prTokenSourceOptions(
  projects: Project[],
  excludeProjectId?: string,
): PrTokenSourceOption[] {
  const options: PrTokenSourceOption[] = [];
  for (const p of projects) {
    if (p.id === excludeProjectId) continue;
    if (p.has_github_token) {
      const slug = hasGitHubRepo(p) ? ` (${repoSlug(p)})` : "";
      options.push({
        value: `${p.id}:access`,
        projectId: p.id,
        kind: "access",
        label: `${p.name}${slug} · Access Token`,
      });
    }
    if (p.has_github_pr_token) {
      options.push({
        value: `${p.id}:pr`,
        projectId: p.id,
        kind: "pr",
        label: `${p.name} · PR Token`,
      });
    }
  }
  return options;
}

export function parsePrTokenSourceValue(
  value: string,
): { projectId: string; kind: PrTokenSourceKind } | null {
  const idx = value.lastIndexOf(":");
  if (idx <= 0) return null;
  const kind = value.slice(idx + 1);
  if (kind !== "access" && kind !== "pr") return null;
  return { projectId: value.slice(0, idx), kind };
}
