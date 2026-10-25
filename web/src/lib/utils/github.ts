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
  // 与后端同口径：scheme 非空只认 http/https，ssh://、ftp:// 原样返回交
  // 后端 40001——静默当 https 归一会把仓库绑到与输入协议不符的集成。
  if (scheme !== "" && scheme !== "http" && scheme !== "https") {
    return raw;
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
    let port = "";
    // 与后端 normHost 同口径剥 DNS 根点尾缀（forge.example. ≡ forge.example）。
    let name = first.toLowerCase().replace(/\.+$/, "");
    if (hasPort) {
      const sep = first.startsWith("[") ? first.lastIndexOf("]:") : first.lastIndexOf(":");
      port = first.slice(sep + (first.startsWith("[") ? 2 : 1));
      name = first.slice(0, sep + (first.startsWith("[") ? 1 : 0)).toLowerCase().replace(/\.+$/, "");
      // 数字端口统一拼写（:0443 → :443），与后端 parseBaseURL/repoHostKey
      // 同口径——同一端点的不同写法归一出同一个主机键。
      if (/^\d+$/.test(port)) port = String(parseInt(port, 10));
    }
    // 与 SplitHostPort 同口径剥掉 IPv6 方括号：[::1] 与 [::1]:443 两种
    // 写法归一出同一个主机键，不能只剥带端口的一支。
    if (name.startsWith("[") && name.endsWith("]")) name = name.slice(1, -1);
    host = name + (hasPort ? ":" + port : scheme === "http" ? ":80" : ":443");
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

// hostFromRepoInput 提取输入里的显式主机：scheme 之后的首段，或裸形态
// 首段含点/冒号（o.r/o:r）的主机段。无主机输入（owner/repo）返回 ""。
// 用于提交前比对所选集成主机——ownerRepoFromInput 会剥掉主机段，不比
// 会把 forgejo-B 的 URL 静默绑到 forgejo-A 下。
export function hostFromRepoInput(raw: string): string {
  const rest = raw.trim();
  const protoIdx = rest.indexOf("://");
  if (protoIdx !== -1) {
    const slash = rest.indexOf("/", protoIdx + 3);
    let host = slash === -1 ? rest.slice(protoIdx + 3) : rest.slice(protoIdx + 3, slash);
    // userinfo 不属于主机：https://user:pass@host/o/r 取 @ 后段。
    const atIdx = host.lastIndexOf("@");
    if (atIdx !== -1) host = host.slice(atIdx + 1);
    // scheme 缺省端口与 new URL().host 的归一同口径——https://x:443 与
    // https://x 是同一主机，不剥会把等价地址误报成主机不一致。
    const scheme = rest.slice(0, protoIdx).toLowerCase();
    const defaultPort = scheme === "https" ? ":443" : scheme === "http" ? ":80" : "";
    if (defaultPort !== "" && host.toLowerCase().endsWith(defaultPort)) {
      host = host.slice(0, -defaultPort.length);
    }
    return host.toLowerCase();
  }
  // 裸形态与 ownerRepoFromInput 会剥的口径对齐：只有 github.com/ 前缀会被
  // 剥掉，故只有它算显式主机；两段式 my.org/repo 是含点 owner（剥掉只剩
  // 一段，本就不是主机），三段以上 host/o/r 反正解析失败——取首段只为把
  // 「格式错误」换成更准的「主机不一致」。
  const slash = rest.indexOf("/");
  if (slash === -1) return "";
  const first = rest.slice(0, slash);
  if (first.toLowerCase() === "github.com") return "github.com";
  const tailSegments = rest.slice(slash + 1).split("/").length;
  if (tailSegments >= 2 && (first.includes(".") || first.includes(":"))) {
    return first.toLowerCase();
  }
  return "";
}

// ownerRepoFromInput 解析选择器的 owner/repo 输入：剥 scheme（其后首段必是
// 主机，连主机一起剥）或裸 github.com/ 前缀后，必须恰好剩 owner/repo 两段。
// 无 scheme 的多余主机段（my.org/a/b）不猜——把它当主机剥掉会把 Forgejo
// 含点的组织名静默错绑到别的路径，解析不出就交表单就地报错。
export function ownerRepoFromInput(raw: string): { owner: string; repo: string } {
  let rest = raw.trim();
  const protoIdx = rest.indexOf("://");
  if (protoIdx !== -1) {
    const slash = rest.indexOf("/", protoIdx + 3);
    rest = slash === -1 ? "" : rest.slice(slash + 1);
  } else if (rest.toLowerCase().startsWith("github.com/")) {
    rest = rest.slice(11);
  }
  // 与后端同序：先一个尾斜杠再 .git（大小写敏感）。
  rest = rest.replace(/\/$/, "").replace(/\.git$/, "");
  const parts = rest.split("/");
  if (parts.length !== 2 || !parts[0] || !parts[1]) {
    return { owner: "", repo: "" };
  }
  return { owner: parts[0], repo: parts[1] };
}

// codeRepoSlugFromSaved 从服务端投影的 code_repo_url 剥出无主机的 owner/repo
// 供选择器回填。投影由后端按绑定集成选好主机，前端不拼 scheme；剥法与
// normalizeCodeRepoRef 的裸形态主机判定同口径：首段含点或冒号视为主机段，
// 且剥掉后仍须剩 owner/repo 两段——含点的组织名（裸形态 my.org/repo）剥完
// 只剩一段，说明首段本来就是 owner，不剥。
export function codeRepoSlugFromSaved(raw: string): { owner: string; repo: string } {
  let rest = raw.trim();
  const protoIdx = rest.indexOf("://");
  if (protoIdx !== -1) {
    // scheme 后首段必是主机，连主机一起剥
    const slash = rest.indexOf("/", protoIdx + 3);
    rest = slash === -1 ? "" : rest.slice(slash + 1);
  } else {
    const slash = rest.indexOf("/");
    if (slash !== -1) {
      const first = rest.slice(0, slash);
      const tail = rest.slice(slash + 1);
      if (
        (first.includes(".") || first.includes(":")) &&
        tail.split("/").length >= 2
      ) {
        rest = tail;
      }
    }
  }
  // 与后端同序：先一个尾斜杠再 .git（大小写敏感）。
  rest = rest.replace(/\/$/, "").replace(/\.git$/, "");
  const parts = rest.split("/");
  if (parts.length !== 2 || !parts[0] || !parts[1]) {
    return { owner: "", repo: "" };
  }
  return { owner: parts[0], repo: parts[1] };
}

// codeRepoUrlFromSlug 按所选集成把 owner/repo 合成提交用的
// code_repository_url：内置 github 传裸 owner/repo（后端按 github.com 解析，
// scheme 有无都归回同一主机，前端不自拼 https://github.com）；forgejo 传
// {base_url}/{owner}/{repo}，与后端 parseRepositoryURL 的主机匹配同口径。
export function codeRepoUrlFromSlug(
  integration: Pick<IntegrationOption, "kind" | "base_url">,
  ownerRepo: { owner: string; repo: string },
): string {
  const slug = `${ownerRepo.owner}/${ownerRepo.repo}`;
  if (integration.kind === "github") return slug;
  return `${integration.base_url.replace(/\/+$/, "")}/${slug}`;
}
