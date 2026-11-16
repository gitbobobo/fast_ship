import { matchPath } from "react-router";

/** 项目级路由的六个 section；切换项目时同名 section 互相延续。 */
const PROJECT_SECTIONS = [
  "issues",
  "board",
  "versions",
  "documents",
  "screenshots",
  "logs",
] as const;

export type ProjectSection = (typeof PROJECT_SECTIONS)[number];

const SECTION_SET: ReadonlySet<string> = new Set(PROJECT_SECTIONS);

/**
 * 计算从当前路径切换到项目 nextProjectId 的目标路径；无法延续项目上下文时返回 null。
 *
 * 规则：
 * - 列表页 /projects/A/<section>（issues/board/versions/documents/screenshots/logs）
 *   → /projects/B/<section>；
 * - 详情或子页（issues/:iid、issues/:iid/edit、issues/new、versions/:vid、
 *   versions/new、logs/:runId）→ B 的对应列表页 /projects/B/issues|versions|logs；
 * - 其余（全局页、/projects、未知 section）不属于项目级路由，返回 null。
 * 只处理 pathname：query 由调用方决定去留。
 */
export function projectSwitchPath(
  pathname: string,
  nextProjectId: string,
): string | null {
  if (!nextProjectId) return null;
  const match = matchPath("/projects/:id/*", pathname);
  if (!match) return null;
  // section 之后的路径段（详情、new、edit、runId）一律丢弃，落到 section 列表页
  const [section] = (match.params["*"] ?? "").split("/").filter(Boolean);
  if (!section || !SECTION_SET.has(section)) return null;
  return `/projects/${encodeURIComponent(nextProjectId)}/${section}`;
}
