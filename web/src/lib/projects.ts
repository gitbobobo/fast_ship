// 项目领域的共享小逻辑：搁置状态文案。
// 当前项目不再由页面裁决：项目级页面只认路径（useRouteProject），
// 全局当前项目由侧边栏等消费方走 useCurrentProjectId（web/src/lib/current-project.ts）。

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
