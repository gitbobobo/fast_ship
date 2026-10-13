import { HTTPError } from "ky";
import { toast } from "sonner";

export function parseRepoUrl(url: string): { owner: string; repo: string } {
  let rest = url.trim();
  const protoIdx = rest.indexOf("://");
  if (protoIdx !== -1) rest = rest.slice(protoIdx + 3);
  if (rest.toLowerCase().startsWith("github.com/")) {
    rest = rest.slice(11);
  }
  rest = rest.replace(/\.git$/i, "").replace(/\/$/, "");
  const parts = rest.split("/", 2);
  return { owner: parts[0] || "", repo: parts[1] || "" };
}

export function hasGitHubRepo(project?: { github_owner?: string; github_repo?: string } | null): boolean {
  return !!(project?.github_owner && project?.github_repo);
}

export function repoSlug(project?: { github_owner?: string; github_repo?: string } | null): string {
  return hasGitHubRepo(project) ? `${project!.github_owner}/${project!.github_repo}` : "";
}

export function ensureGitHubLinked(
  project: { github_owner?: string; github_repo?: string } | undefined | null,
  action: string,
): boolean {
  if (!hasGitHubRepo(project)) {
    toast.error("请先关联 GitHub 仓库", {
      description: `在项目设置中关联 GitHub 仓库后即可${action}`,
    });
    return false;
  }
  return true;
}

// ensureGitHubToken 提示「仓库已关联但未配公开仓库 Token」：
// 同步与 GitHub 写操作依赖公开仓库 Token，代码仓库 Token 不能替代。
export function ensureGitHubToken(
  project: { public_github_token_id?: string } | undefined | null,
  action: string,
): boolean {
  if (project && !project.public_github_token_id) {
    toast.error("该项目未配置公开仓库 Token", {
      description: `在设置 → GitHub Tokens 添加 Token 并在项目设置中引用后即可${action}`,
    });
    return false;
  }
  return true;
}

// formatSyncError 把同步失败的响应信封转成提示：缺公开仓库 Token（40006）
// 点明补配路径，其余展示服务端消息或通用文案。
export async function formatSyncError(
  error: unknown,
  fallback: string,
): Promise<string> {
  if (error instanceof HTTPError) {
    const body = await error.response
      .json<ApiResponse<unknown>>()
      .catch(() => null);
    if (body?.code === 40006) {
      return "该项目未配置公开仓库 Token，无法同步（不影响已同步 Issue 的浏览）";
    }
    if (body?.message) return body.message;
  }
  return fallback;
}
