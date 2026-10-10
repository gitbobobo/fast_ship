// 截图画布的纯函数：自动排版、视口变换、比例坐标换算。
// 世界坐标 → 屏幕坐标：screen = world * scale + (x, y)。

import { deriveGroupTabs, decodeGroupTab } from "./screenshots";

export interface Viewport {
  x: number;
  y: number;
  scale: number;
}

export interface Rect {
  x: number;
  y: number;
  width: number;
  height: number;
}

export interface Size {
  width: number;
  height: number;
}

export const MIN_SCALE = 0.05;
export const MAX_SCALE = 4;

export const CARD_WIDTH = 320;
export const CARD_META_HEIGHT = 56;
export const CARD_GAP = 32;
export const GROUP_TITLE_HEIGHT = 56;
export const GROUP_GAP = 72;
/** 容器尺寸未知时的默认列数 */
export const GRID_COLUMNS = 4;
/** 自动选列数的范围 */
export const MIN_COLUMNS = 2;
export const MAX_COLUMNS = 24;
/** 「适应全部」与选列数共用的容器留白 */
export const FIT_PADDING = 48;
export const DEFAULT_ASPECT = 3 / 4;
export const UNGROUPED_TITLE = "未分组";

/** 标注最小有效尺寸（占图宽/高的比例），更小的框视为误触 */
export const MIN_ANNOTATION_RATIO = 0.01;

export interface CanvasScreenInput {
  id: string;
  group: string;
  last_uploaded_at: string;
}

export interface CanvasCardLayout {
  screenId: string;
  /** 卡片整体（图片区 + 元信息行）的世界矩形 */
  rect: Rect;
  /** 图片区高度 */
  imageHeight: number;
}

export interface CanvasGroupLayout {
  key: string;
  title: string;
  count: number;
  /** 组标题行的世界矩形 */
  titleRect: Rect;
}

export interface CanvasLayout {
  groups: CanvasGroupLayout[];
  cards: CanvasCardLayout[];
  bounds: Rect;
}

export function clampScale(scale: number): number {
  return Math.min(Math.max(scale, MIN_SCALE), MAX_SCALE);
}

interface CanvasSection {
  key: string;
  title: string;
  items: CanvasScreenInput[];
}

/** 分组分区：命名组按组内最近上传倒序，未分组在最后；组内按 last_uploaded_at 倒序 */
function buildSections(screens: CanvasScreenInput[]): CanvasSection[] {
  const groupOrder = deriveGroupTabs(screens).map(decodeGroupTab);
  const buckets = new Map<string, CanvasScreenInput[]>();
  for (const name of groupOrder) buckets.set(name, []);
  const ungrouped: CanvasScreenInput[] = [];
  for (const screen of screens) {
    if (screen.group) buckets.get(screen.group)?.push(screen);
    else ungrouped.push(screen);
  }

  const sections: CanvasSection[] = groupOrder.map((name) => ({
    key: `g:${name}`,
    title: name,
    items: buckets.get(name) ?? [],
  }));
  if (ungrouped.length > 0) {
    sections.push({ key: "__ungrouped__", title: UNGROUPED_TITLE, items: ungrouped });
  }
  return sections.map((section) => ({
    ...section,
    items: [...section.items].sort((a, b) =>
      b.last_uploaded_at.localeCompare(a.last_uploaded_at),
    ),
  }));
}

// emit 缺省时只算世界边界（选列数候选评估用），不分配卡片/组对象
function layoutSections(
  sections: CanvasSection[],
  aspectOf: (screenId: string) => number | null | undefined,
  gridColumns: number,
  emit?: {
    group: (group: CanvasGroupLayout) => void;
    card: (card: CanvasCardLayout) => void;
  },
): Rect {
  let cursorY = 0;
  let maxRight = 0;

  for (const { key, title, items } of sections) {
    const columns = Math.max(Math.min(gridColumns, items.length), 1);
    const sectionWidth =
      columns * CARD_WIDTH + Math.max(columns - 1, 0) * CARD_GAP;
    emit?.group({
      key,
      title,
      count: items.length,
      titleRect: {
        x: 0,
        y: cursorY,
        width: Math.max(sectionWidth, CARD_WIDTH),
        height: GROUP_TITLE_HEIGHT,
      },
    });
    cursorY += GROUP_TITLE_HEIGHT;
    maxRight = Math.max(maxRight, sectionWidth);

    for (let rowStart = 0; rowStart < items.length; rowStart += columns) {
      const row = items.slice(rowStart, rowStart + columns);
      const imageHeights = row.map((item) => {
        const aspect = aspectOf(item.id);
        return CARD_WIDTH * (aspect && aspect > 0 ? aspect : DEFAULT_ASPECT);
      });
      const rowHeight = Math.max(...imageHeights) + CARD_META_HEIGHT;
      if (emit) {
        row.forEach((item, i) => {
          emit.card({
            screenId: item.id,
            rect: {
              x: i * (CARD_WIDTH + CARD_GAP),
              y: cursorY,
              width: CARD_WIDTH,
              height: imageHeights[i] + CARD_META_HEIGHT,
            },
            imageHeight: imageHeights[i],
          });
        });
      }
      cursorY += rowHeight + CARD_GAP;
    }
    cursorY += GROUP_GAP - CARD_GAP;
  }

  return {
    x: 0,
    y: 0,
    width: maxRight,
    height: Math.max(cursorY - GROUP_GAP, 0),
  };
}

/**
 * 按分组自动排版：命名组按组内最近上传倒序，未分组在最后；组内按
 * last_uploaded_at 倒序，固定列数网格（默认 GRID_COLUMNS，每组不超过
 * 组内数量），行高取该行最高卡片。aspectOf 返回图片高/宽，未知时回退 4:3。
 */
export function layoutCanvas(
  screens: CanvasScreenInput[],
  aspectOf: (screenId: string) => number | null | undefined,
  columns = GRID_COLUMNS,
): CanvasLayout {
  const groups: CanvasGroupLayout[] = [];
  const cards: CanvasCardLayout[] = [];
  const bounds = layoutSections(buildSections(screens), aspectOf, columns, {
    group: (group) => groups.push(group),
    card: (card) => cards.push(card),
  });
  return { groups, cards, bounds };
}

/**
 * 按容器尺寸选网格列数：在 [MIN_COLUMNS, MAX_COLUMNS] 内试排，取「适应全部」
 * 缩放率最大的列数，使世界宽高比贴近容器；缩放率相同取更少的列。
 * 容器未量到尺寸时回退 GRID_COLUMNS。
 */
export function pickCanvasColumns(
  screens: CanvasScreenInput[],
  aspectOf: (screenId: string) => number | null | undefined,
  container: Size,
  padding = FIT_PADDING,
): number {
  if (container.width <= 0 || container.height <= 0 || screens.length === 0) {
    return GRID_COLUMNS;
  }
  const sections = buildSections(screens);
  const widest = Math.max(...sections.map((s) => s.items.length));
  const maxColumns = Math.max(Math.min(MAX_COLUMNS, widest), MIN_COLUMNS);
  const availW = Math.max(container.width - padding * 2, 1);
  const availH = Math.max(container.height - padding * 2, 1);

  let best = MIN_COLUMNS;
  let bestScale = -1;
  for (let columns = MIN_COLUMNS; columns <= maxColumns; columns++) {
    const bounds = layoutSections(sections, aspectOf, columns);
    const scale = Math.min(
      availW / Math.max(bounds.width, 1),
      availH / Math.max(bounds.height, 1),
    );
    if (scale > bestScale + 1e-9) {
      best = columns;
      bestScale = scale;
    }
  }
  return best;
}

export function worldToScreen(vp: Viewport, x: number, y: number) {
  return { x: x * vp.scale + vp.x, y: y * vp.scale + vp.y };
}

export function screenToWorld(vp: Viewport, x: number, y: number) {
  return { x: (x - vp.x) / vp.scale, y: (y - vp.y) / vp.scale };
}

/** 以屏幕点 (cx, cy) 为锚点缩放到新比例，锚点下的世界坐标保持不动 */
export function zoomAt(
  vp: Viewport,
  nextScale: number,
  cx: number,
  cy: number,
): Viewport {
  const scale = clampScale(nextScale);
  const world = screenToWorld(vp, cx, cy);
  return { scale, x: cx - world.x * scale, y: cy - world.y * scale };
}

/** 滚轮增量 → 缩放倍率；ctrl（触控板捏合）灵敏度更高 */
export function wheelZoomFactor(
  deltaY: number,
  ctrlKey: boolean,
  deltaMode = 0,
): number {
  const lineScale = deltaMode === 1 ? 16 : deltaMode === 2 ? 100 : 1;
  const sensitivity = ctrlKey ? 0.01 : 0.0018;
  return Math.exp(-deltaY * lineScale * sensitivity);
}

/** 让世界矩形居中并尽量铺满容器（留 padding），缩放不超过 maxScale */
export function focusRect(
  rect: Rect,
  container: Size,
  options: { padding?: number; maxScale?: number } = {},
): Viewport {
  const padding = options.padding ?? FIT_PADDING;
  const maxScale = options.maxScale ?? MAX_SCALE;
  const availW = Math.max(container.width - padding * 2, 1);
  const availH = Math.max(container.height - padding * 2, 1);
  const scale = Math.min(
    clampScale(Math.min(availW / Math.max(rect.width, 1), availH / Math.max(rect.height, 1))),
    maxScale,
  );
  return {
    scale,
    x: container.width / 2 - (rect.x + rect.width / 2) * scale,
    y: container.height / 2 - (rect.y + rect.height / 2) * scale,
  };
}

/** 适应全部：内容为空时回到原点 1 倍 */
export function fitViewport(
  bounds: Rect,
  container: Size,
  padding = FIT_PADDING,
): Viewport {
  if (bounds.width <= 0 || bounds.height <= 0) {
    return { x: padding, y: padding, scale: 1 };
  }
  return focusRect(bounds, container, { padding, maxScale: 1 });
}

/** 当前视口覆盖的世界矩形，margin 为每侧额外外扩的屏幕像素 */
export function visibleWorldRect(
  vp: Viewport,
  container: Size,
  margin = 0,
): Rect {
  const topLeft = screenToWorld(vp, -margin, -margin);
  return {
    x: topLeft.x,
    y: topLeft.y,
    width: (container.width + margin * 2) / vp.scale,
    height: (container.height + margin * 2) / vp.scale,
  };
}

export function rectsIntersect(a: Rect, b: Rect): boolean {
  return (
    a.x < b.x + b.width &&
    a.x + a.width > b.x &&
    a.y < b.y + b.height &&
    a.y + a.height > b.y
  );
}

export function clamp01(n: number): number {
  return Math.min(Math.max(n, 0), 1);
}

/**
 * 把图片盒内的两个像素点（相对盒左上角）换算成比例矩形，坐标夹到 [0,1]，
 * 起止点顺序无关。盒尺寸为 0 时返回 null。
 */
export function dragToRatioRect(
  start: { x: number; y: number },
  end: { x: number; y: number },
  box: Size,
): Rect | null {
  if (box.width <= 0 || box.height <= 0) return null;
  const x1 = clamp01(Math.min(start.x, end.x) / box.width);
  const y1 = clamp01(Math.min(start.y, end.y) / box.height);
  const x2 = clamp01(Math.max(start.x, end.x) / box.width);
  const y2 = clamp01(Math.max(start.y, end.y) / box.height);
  return { x: x1, y: y1, width: x2 - x1, height: y2 - y1 };
}

const RATIO_PRECISION = 10_000;

/**
 * 提交前把比例矩形量化到 1e-4：避免 x+width 因浮点误差略超 1 被服务端
 * 拒绝；贴到右/下边缘时多让出一个量化单位，保证严格 ≤ 1。
 */
export function normalizeRatioRect(rect: Rect): Rect {
  const x = Math.round(clamp01(rect.x) * RATIO_PRECISION);
  const y = Math.round(clamp01(rect.y) * RATIO_PRECISION);
  const fit = (start: number, size: number) => {
    const raw = Math.max(Math.round(size * RATIO_PRECISION), 1);
    return start + raw >= RATIO_PRECISION
      ? Math.max(RATIO_PRECISION - start - 1, 1)
      : raw;
  };
  return {
    x: x / RATIO_PRECISION,
    y: y / RATIO_PRECISION,
    width: fit(x, rect.width) / RATIO_PRECISION,
    height: fit(y, rect.height) / RATIO_PRECISION,
  };
}

export function isAnnotationRectTooSmall(rect: Rect): boolean {
  return (
    rect.width < MIN_ANNOTATION_RATIO || rect.height < MIN_ANNOTATION_RATIO
  );
}

/**
 * object-contain 下图片在容器中的实际显示盒（相对容器左上角）。
 * 自然尺寸未知时返回 null，调用方不应叠加标注。
 */
export function containedImageBox(
  container: Size,
  natural: Size,
): Rect | null {
  if (
    natural.width <= 0 ||
    natural.height <= 0 ||
    container.width <= 0 ||
    container.height <= 0
  ) {
    return null;
  }
  const ratio = Math.min(
    container.width / natural.width,
    container.height / natural.height,
  );
  const width = natural.width * ratio;
  const height = natural.height * ratio;
  return {
    x: (container.width - width) / 2,
    y: (container.height - height) / 2,
    width,
    height,
  };
}

/** 比例矩形 → 图片盒内的 CSS 百分比定位 */
export function ratioRectToPercentStyle(rect: Rect) {
  return {
    left: `${rect.x * 100}%`,
    top: `${rect.y * 100}%`,
    width: `${rect.width * 100}%`,
    height: `${rect.height * 100}%`,
  };
}

/** 图片高/宽；尺寸未知（0）返回 null */
export function aspectFromSize(
  width: number | undefined,
  height: number | undefined,
): number | null {
  if (!width || !height || width <= 0 || height <= 0) return null;
  return height / width;
}

/** 每个界面「挂在非最新版本且仍未解决」的标注数 */
export function countOpenOnOlderVersions(
  annotations: ScreenshotAnnotation[],
  latestVersionIdOf: (screenId: string) => string | null | undefined,
): Map<string, number> {
  const counts = new Map<string, number>();
  for (const a of annotations) {
    if (a.status !== "open") continue;
    const latestId = latestVersionIdOf(a.screen_id);
    if (!latestId || a.version_id === latestId) continue;
    counts.set(a.screen_id, (counts.get(a.screen_id) ?? 0) + 1);
  }
  return counts;
}

/** 删除界面/版本时会被级联删除的标注数（不分状态） */
export function countAnnotations(
  annotations: ScreenshotAnnotation[],
  scope: { screenId?: string; versionId?: string },
): number {
  return annotations.filter(
    (a) =>
      (scope.screenId === undefined || a.screen_id === scope.screenId) &&
      (scope.versionId === undefined || a.version_id === scope.versionId),
  ).length;
}

/** 删除界面/版本时会被级联删除的未解决标注数 */
export function countOpenAnnotations(
  annotations: ScreenshotAnnotation[],
  scope: { screenId?: string; versionId?: string },
): number {
  return annotations.filter(
    (a) =>
      a.status === "open" &&
      (scope.screenId === undefined || a.screen_id === scope.screenId) &&
      (scope.versionId === undefined || a.version_id === scope.versionId),
  ).length;
}

export type AnnotationStatusFilter = "open" | "resolved" | "all";

export function filterAnnotationsForPanel(
  annotations: ScreenshotAnnotation[],
  screenIds: ReadonlySet<string>,
  status: AnnotationStatusFilter,
): ScreenshotAnnotation[] {
  return annotations.filter(
    (a) =>
      screenIds.has(a.screen_id) && (status === "all" || a.status === status),
  );
}
