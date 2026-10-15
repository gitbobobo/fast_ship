// 截图维度过滤的纯函数：选项推导（含计数）、URL 编解码、客户端过滤。

/** 「未标注」桶：对应维度为 NULL 的截图/标注 */
export const SCREENSHOT_NONE = "__none__";

export function screenDisplayName(
  screen: Pick<ScreenshotScreen, "title" | "screen_key">,
): string {
  return screen.title || screen.screen_key;
}

/** 一个维度的多选值（含未标注桶） */
export type DimensionFilter = string[];

/** URL ↔ 过滤状态的编解码：逗号分隔；非法片段丢弃 */
export function encodeDimensionFilter(values: DimensionFilter): string {
  return values.join(",");
}

export function decodeDimensionFilter(raw: string | null): DimensionFilter {
  if (!raw) return [];
  return raw
    .split(",")
    .map((v) => v.trim())
    .filter((v) => v.length > 0);
}

export interface DimensionOption {
  value: string;
  count: number;
}

/** 从截图集合推导一个维度的选项与计数；NULL 归入未标注桶，按 count 倒序 */
export function deriveDimensionOptions(
  screens: ScreenshotScreen[],
  dimension: "platform" | "locale" | "theme",
): DimensionOption[] {
  const counts = new Map<string, number>();
  for (const screen of screens) {
    const value = screen[dimension] ?? SCREENSHOT_NONE;
    counts.set(value, (counts.get(value) ?? 0) + 1);
  }
  return [...counts.entries()]
    .map(([value, count]) => ({ value, count }))
    .sort((a, b) => b.count - a.count || a.value.localeCompare(b.value));
}

/** 未标注桶的计数（维度为 NULL 的截图数） */
export function countUntagged(
  screens: ScreenshotScreen[],
  dimension: "platform" | "locale" | "theme",
): number {
  return screens.filter((s) => s[dimension] === null).length;
}

/** 多选切换：已选则移除，未选则加入 */
export function toggleDimensionValue(
  values: DimensionFilter,
  value: string,
): DimensionFilter {
  return values.includes(value)
    ? values.filter((v) => v !== value)
    : [...values, value];
}

/** 单维度是否命中：空过滤放行全部；命中任一选中值或（选中未标注桶时）NULL */
export function matchesDimension(
  value: string | null,
  filter: DimensionFilter,
): boolean {
  if (filter.length === 0) return true;
  const key = value ?? SCREENSHOT_NONE;
  return filter.includes(key);
}

/** 画布截图集合过滤：三个维度各自独立多选，空过滤放行 */
export function filterScreensByDimensions(
  screens: ScreenshotScreen[],
  filters: {
    platform: DimensionFilter;
    locale: DimensionFilter;
    theme: DimensionFilter;
  },
): ScreenshotScreen[] {
  return screens.filter(
    (s) =>
      matchesDimension(s.platform, filters.platform) &&
      matchesDimension(s.locale, filters.locale) &&
      matchesDimension(s.theme, filters.theme),
  );
}

/** 按关键词过滤截图：标题或界面标识大小写不敏感的子串匹配；空白查询放行全部 */
export function filterScreensByQuery(
  screens: ScreenshotScreen[],
  query: string,
): ScreenshotScreen[] {
  const q = query.trim().toLowerCase();
  if (!q) return screens;
  return screens.filter(
    (screen) =>
      screen.title.toLowerCase().includes(q) ||
      screen.screen_key.toLowerCase().includes(q),
  );
}

/** 标注按所属截图的维度过滤；空过滤放行 */
export function filterAnnotationsByDimensions(
  annotations: ScreenshotAnnotation[],
  screensById: ReadonlyMap<string, ScreenshotScreen>,
  filters: {
    platform: DimensionFilter;
    locale: DimensionFilter;
    theme: DimensionFilter;
  },
): ScreenshotAnnotation[] {
  return annotations.filter((a) => {
    const screen = screensById.get(a.screen_id);
    if (!screen) return false;
    return (
      matchesDimension(screen.platform, filters.platform) &&
      matchesDimension(screen.locale, filters.locale) &&
      matchesDimension(screen.theme, filters.theme)
    );
  });
}

/** 选项显示名：未标注桶之外原样展示 */
export function dimensionLabel(value: string): string {
  return value === SCREENSHOT_NONE ? "未标注" : value;
}
