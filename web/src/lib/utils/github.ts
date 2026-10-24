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
// 供比较：scheme 大小写、尾部斜杠与 .git 后缀不算改动（剥除顺序与后端
// parseRepositoryURL 一致：先一个尾斜杠再 .git，.git 大小写敏感）；裸
// owner/repo 输入按 github.com 处理。host 无显式端口时按 scheme 补默认端口
// （http→80，其余→443，与后端主机键同口径）——http/https 同主机的两条集成
// 之间切换会归一出不同引用，表单不会误判「未改动」而漏发新地址；github.com
// 任意 scheme/默认端口归回裸主机名（内置 github-default）。解析不出
// owner/repo 时返回空串，调用方按「有输入待服务端校验」处理，不能当成未
// 改动。owner/repo 之外的多余路径段（后端 40001 拒绝的输入）直接返回原始
// 输入——归一结果必然与任何现值不等，原样发后端由 40001 表态，不静默丢弃
// 第三段。
export function normalizeCodeRepoRef(raw: string): string {
  let rest = raw.trim();
  if (!rest) return "";
  let scheme = "";
  const protoIdx = rest.indexOf("://");
  if (protoIdx !== -1) {
    scheme = rest.slice(0, protoIdx).toLowerCase();
    rest = rest.slice(protoIdx + 3);
  }
  const slash = rest.indexOf("/");
  const first = slash === -1 ? rest : rest.slice(0, slash);
  let tail = slash === -1 ? "" : rest.slice(slash);
  let host: string;
  if (scheme === "" && !first.includes(".") && !first.includes(":")) {
    host = "github.com";
    tail = "/" + first + tail; // 首段是 owner，不是 host
  } else {
    // 与后端 SplitHostPort 同口径的端口判定：单个冒号（host:port）或
    // 方括号 IPv6 带端口（[::1]:8443）算显式端口，其余按 scheme 补默认。
    const colonCount = (first.match(/:/g) || []).length;
    const hasPort = colonCount === 1 || /^\[.*\]:[^:]+$/.test(first);
    host =
      first.toLowerCase() + (hasPort ? "" : scheme === "http" ? ":80" : ":443");
    if (host === "github.com:80" || host === "github.com:443") {
      host = "github.com";
    }
  }
  // 与后端同序：先剥一个尾斜杠再剥 .git 后缀（大小写敏感，不加 i 标志）。
  const path = tail.slice(1).replace(/\/$/, "").replace(/\.git$/, "");
  const parts = path.split("/");
  if (parts.length > 2 && parts[2].trim() !== "") {
    return raw; // 多余路径段不是仓库根：原样交后端拒绝
  }
  if (parts.length < 2 || parts[0] === "" || parts[1] === "") return "";
  return `${host}/${parts[0]}/${parts[1]}`.toLowerCase();
}

// codeRepoUrlForInput 编辑回填的代码仓输入值：优先服务端投影的 code_repo_url
// （forgejo 集成时是实例基址形态）；为 null 且未绑定集成（code_integration_id
// 为空 = 内置 github 存量行）时按 owner/repo 兜底 github 形态。绑定了集成但
// 投影为空（悬空绑定）返回空串——不再回落 github.com/{o}/{r}：那会把
// forgejo 的 owner/repo 显示成 github 地址，保存时被静默改绑。
export function codeRepoUrlForInput(project: {
  code_repo_url?: string | null;
  code_integration_id?: string | null;
  code_github_owner?: string;
  code_github_repo?: string;
}): string {
  if (project.code_repo_url) return project.code_repo_url;
  if (
    !project.code_integration_id &&
    project.code_github_owner &&
    project.code_github_repo
  ) {
    return `https://github.com/${project.code_github_owner}/${project.code_github_repo}`;
  }
  return "";
}
