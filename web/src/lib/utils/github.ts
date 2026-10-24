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


// normalizeCodeRepoRef 把代码仓引用归一成「host[:端口]/owner/repo」小写形态
// 供比较：scheme 有无与大小写、尾部斜杠与 .git 后缀不算改动（剥除顺序与
// 后端 parseRepositoryURL 一致：先 .git 再一个尾斜杠）；裸 owner/repo 输入
// 按 github.com 处理。解析不出 owner/repo 时返回空串，调用方按「有输入待
// 服务端校验」处理，不能当成未改动。
export function normalizeCodeRepoRef(raw: string): string {
  let rest = raw.trim();
  if (!rest) return "";
  const protoIdx = rest.indexOf("://");
  if (protoIdx !== -1) rest = rest.slice(protoIdx + 3);
  const slash = rest.indexOf("/");
  const first = slash === -1 ? rest : rest.slice(0, slash);
  let tail = slash === -1 ? "" : rest.slice(slash);
  let host: string;
  if (first.includes(".") || first.includes(":")) {
    host = first.toLowerCase();
  } else {
    host = "github.com";
    tail = "/" + first + tail; // 首段是 owner，不是 host
  }
  tail = tail.replace(/\.git$/i, "").replace(/\/$/, "");
  const parts = tail.split("/").filter(Boolean);
  if (parts.length < 2) return "";
  return `${host}/${parts[0]}/${parts[1]}`.toLowerCase();
}

// codeRepoUrlForInput 编辑回填的代码仓输入值：优先服务端投影的 code_repo_url
// （forgejo 集成时是实例基址形态）；为 null 且 owner/repo 还在时兜底 github
// 形态（覆盖集成行缺失的存量行）；都没有给空串。
export function codeRepoUrlForInput(project: {
  code_repo_url?: string | null;
  code_github_owner?: string;
  code_github_repo?: string;
}): string {
  if (project.code_repo_url) return project.code_repo_url;
  if (project.code_github_owner && project.code_github_repo) {
    return `https://github.com/${project.code_github_owner}/${project.code_github_repo}`;
  }
  return "";
}
