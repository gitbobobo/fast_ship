// 项目领域的共享小逻辑：激活项目裁决与搁置状态文案。
// 各项目列表页（问题、看板、版本、日志、文档、截图）共用同一份实现，
// 历史上曾各自拷贝过 getActiveProjectId，改动时容易漏改——统一收口在这里。

/**
 * 当前激活项目 ID 的裁决顺序：
 * 1. 已选项仍在列表中（含已搁置项目）→ 沿用已选项；
 * 2. URL 指定的项目在列表中 → 用 URL 项；
 * 3. 否则取第一个未搁置项目——搁置项目不该被默认选中；
 * 4. 全部搁置时回落到列表首项，保证页面仍有一个可用项目。
 */
export function getActiveProjectId(
  projects: Project[],
  selectedId: string,
  urlId: string | null,
): string {
  if (projects.some((p) => p.id === selectedId)) {
    return selectedId;
  }
  if (urlId && projects.some((p) => p.id === urlId)) {
    return urlId;
  }
  return (projects.find((p) => !p.is_shelved) ?? projects[0])?.id ?? "";
}

/**
 * 搁置状态的统一文案：无限期「已搁置」，有截止日期「搁置至 YYYY-MM-DD」
 * （按本地时区取日期）。未搁置返回 null。
 */
export function shelvedStatusLabel(project: Project): string | null {
  if (!project.is_shelved) return null;
  const until = project.shelved_until;
  if (!until) return "已搁置";
  const d = new Date(until);
  if (Number.isNaN(d.getTime())) return "已搁置";
  const pad = (n: number) => String(n).padStart(2, "0");
  const ymd = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
  return `搁置至 ${ymd}`;
}
