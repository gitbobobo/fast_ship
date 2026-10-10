// 分组 tab 的固定 key；「全部」「未分组」是内置分组
export const SCREENSHOT_TAB_ALL = "__all__";
export const SCREENSHOT_TAB_UNGROUPED = "__ungrouped__";

// 命名分组的 tab value 加前缀编码，避免用户组名与内置 tab 值冲突
export const GROUP_TAB_PREFIX = "g:";

export function decodeGroupTab(tab: string): string {
  return tab.startsWith(GROUP_TAB_PREFIX)
    ? tab.slice(GROUP_TAB_PREFIX.length)
    : tab;
}

export function screenDisplayName(
  screen: Pick<ScreenshotScreen, "title" | "screen_key">,
): string {
  return screen.title || screen.screen_key;
}

// 命名分组按「组内最近上传时间」倒序；空 group 归入「未分组」，不占命名分组位
export function deriveGroupTabs(
  items: Pick<ScreenshotScreenListItem, "group" | "last_uploaded_at">[],
): string[] {
  const latestByGroup = new Map<string, string>();
  for (const item of items) {
    if (!item.group) continue;
    const prev = latestByGroup.get(item.group);
    if (!prev || item.last_uploaded_at > prev) {
      latestByGroup.set(item.group, item.last_uploaded_at);
    }
  }
  return [...latestByGroup.entries()]
    .sort((a, b) => b[1].localeCompare(a[1]))
    .map(([name]) => `${GROUP_TAB_PREFIX}${name}`);
}

export function hasUngroupedScreens(
  items: ScreenshotScreenListItem[],
): boolean {
  return items.some((item) => !item.group);
}

export function filterScreensByTab(
  items: ScreenshotScreenListItem[],
  tab: string,
): ScreenshotScreenListItem[] {
  if (tab === SCREENSHOT_TAB_ALL) return items;
  if (tab === SCREENSHOT_TAB_UNGROUPED) {
    return items.filter((item) => !item.group);
  }
  const name = decodeGroupTab(tab);
  return items.filter((item) => item.group === name);
}

export function filterScreensByQuery(
  items: ScreenshotScreenListItem[],
  query: string,
): ScreenshotScreenListItem[] {
  const q = query.trim().toLowerCase();
  if (!q) return items;
  return items.filter(
    (item) =>
      item.title.toLowerCase().includes(q) ||
      item.screen_key.toLowerCase().includes(q),
  );
}
