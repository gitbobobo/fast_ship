import { describe, expect, it } from "vitest";
import {
  CARD_GAP,
  CARD_META_HEIGHT,
  CARD_WIDTH,
  GRID_COLUMNS,
  GROUP_TITLE_HEIGHT,
  MAX_COLUMNS,
  MAX_SCALE,
  MIN_COLUMNS,
  MIN_SCALE,
  aspectFromSize,
  clampScale,
  containedImageBox,
  countOpenAnnotations,
  countOpenOnOlderVersions,
  dragToRatioRect,
  filterAnnotationsForPanel,
  fitViewport,
  focusRect,
  isAnnotationRectTooSmall,
  layoutCanvas,
  normalizeRatioRect,
  pickCanvasColumns,
  rectsIntersect,
  screenToWorld,
  visibleWorldRect,
  wheelZoomFactor,
  worldToScreen,
  zoomAt,
} from "./screenshot-canvas";

function screen(id: string, group: string, at: string) {
  return { id, group, last_uploaded_at: at };
}

function annotation(
  overrides: Partial<ScreenshotAnnotation> = {},
): ScreenshotAnnotation {
  return {
    id: "a-1",
    project_id: "p-1",
    screen_id: "s-1",
    version_id: "v-1",
    issue_id: null,
    issue_reference: null,
    issue_title: null,
    screen_key: "home",
    screen_title: "",
    screen_group: "",
    image_url: "",
    image_width: 100,
    image_height: 100,
    is_latest_version: true,
    x: 0.1,
    y: 0.1,
    width: 0.2,
    height: 0.2,
    pixel_rect: null,
    body: "x",
    status: "open",
    crop_url: "",
    created_by: "bobo",
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
    resolved_at: null,
    ...overrides,
  };
}

describe("layoutCanvas", () => {
  it("orders named groups by latest upload desc and puts ungrouped last", () => {
    const layout = layoutCanvas(
      [
        screen("1", "设置", "2026-10-01T00:00:00Z"),
        screen("2", "", "2026-10-09T00:00:00Z"),
        screen("3", "首页", "2026-10-05T00:00:00Z"),
      ],
      () => 1,
    );
    expect(layout.groups.map((g) => g.title)).toEqual([
      "首页",
      "设置",
      "未分组",
    ]);
    expect(layout.groups.map((g) => g.titleRect.y)).toEqual(
      [...layout.groups.map((g) => g.titleRect.y)].sort((a, b) => a - b),
    );
  });

  it("sorts cards inside a group by last upload desc on a fixed-column grid", () => {
    const screens = Array.from({ length: GRID_COLUMNS + 1 }, (_, i) =>
      screen(`s${i}`, "", `2026-10-0${i + 1}T00:00:00Z`),
    );
    const layout = layoutCanvas(screens, () => 1);
    expect(layout.cards[0].screenId).toBe(`s${GRID_COLUMNS}`);
    expect(layout.cards[0].rect.x).toBe(0);
    expect(layout.cards[1].rect.x).toBe(CARD_WIDTH + CARD_GAP);
    // 第 GRID_COLUMNS+1 张换到第二行
    const last = layout.cards[GRID_COLUMNS];
    expect(last.rect.x).toBe(0);
    expect(last.rect.y).toBe(
      GROUP_TITLE_HEIGHT + CARD_WIDTH + CARD_META_HEIGHT + CARD_GAP,
    );
  });

  it("honors an explicit column count and caps it by group size", () => {
    const screens = Array.from({ length: 10 }, (_, i) =>
      screen(`s${i}`, "", `2026-10-${String(i + 10)}T00:00:00Z`),
    );
    const wide = layoutCanvas(screens, () => 1, 8);
    expect(wide.cards.filter((c) => c.rect.y === wide.cards[0].rect.y)).toHaveLength(8);
    expect(wide.bounds.width).toBe(8 * CARD_WIDTH + 7 * CARD_GAP);

    const few = layoutCanvas(screens.slice(0, 3), () => 1, 8);
    expect(few.bounds.width).toBe(3 * CARD_WIDTH + 2 * CARD_GAP);
  });

  it("uses image aspect, falling back to 4:3 when unknown", () => {
    const layout = layoutCanvas(
      [
        screen("known", "", "2026-10-02T00:00:00Z"),
        screen("unknown", "", "2026-10-01T00:00:00Z"),
      ],
      (id) => (id === "known" ? 2 : null),
    );
    const byId = Object.fromEntries(layout.cards.map((c) => [c.screenId, c]));
    expect(byId.known.imageHeight).toBe(CARD_WIDTH * 2);
    expect(byId.unknown.imageHeight).toBe(CARD_WIDTH * 0.75);
  });

  it("returns empty bounds for no screens", () => {
    const layout = layoutCanvas([], () => null);
    expect(layout.cards).toEqual([]);
    expect(layout.bounds.width).toBe(0);
    expect(layout.bounds.height).toBe(0);
  });
});

describe("pickCanvasColumns", () => {
  const many = (n: number) =>
    Array.from({ length: n }, (_, i) =>
      screen(`s${i}`, "", `2026-10-01T00:00:${String(i % 60).padStart(2, "0")}Z`),
    );
  const fitScale = (n: number, columns: number, container: { width: number; height: number }) => {
    const { bounds } = layoutCanvas(many(n), () => 2, columns);
    return Math.min(
      (container.width - 96) / bounds.width,
      (container.height - 96) / bounds.height,
    );
  };

  it("falls back to the default columns without a measured container", () => {
    expect(pickCanvasColumns(many(30), () => 2, { width: 0, height: 0 })).toBe(
      GRID_COLUMNS,
    );
  });

  it("uses more than the default columns for many screenshots", () => {
    const container = { width: 1400, height: 800 };
    const columns = pickCanvasColumns(many(120), () => 2, container);
    expect(columns).toBeGreaterThan(GRID_COLUMNS);
    expect(columns).toBeLessThanOrEqual(MAX_COLUMNS);
    expect(fitScale(120, columns, container)).toBeGreaterThan(
      fitScale(120, GRID_COLUMNS, container),
    );
  });

  it("uses fewer columns in a tall narrow container", () => {
    const wide = pickCanvasColumns(many(40), () => 2, { width: 1600, height: 700 });
    const narrow = pickCanvasColumns(many(40), () => 2, { width: 500, height: 900 });
    expect(narrow).toBeLessThan(wide);
    expect(narrow).toBeGreaterThanOrEqual(MIN_COLUMNS);
  });

  it("lays a handful of screenshots out in one row on a wide container", () => {
    expect(
      pickCanvasColumns(many(3), () => 0.75, { width: 1600, height: 700 }),
    ).toBe(3);
  });
});

describe("viewport math", () => {
  it("round-trips world and screen coordinates", () => {
    const vp = { x: 40, y: -20, scale: 0.5 };
    const s = worldToScreen(vp, 100, 200);
    expect(screenToWorld(vp, s.x, s.y)).toEqual({ x: 100, y: 200 });
  });

  it("keeps the anchor point fixed when zooming", () => {
    const vp = { x: 10, y: 20, scale: 1 };
    const before = screenToWorld(vp, 300, 200);
    const next = zoomAt(vp, 2, 300, 200);
    const after = screenToWorld(next, 300, 200);
    expect(after.x).toBeCloseTo(before.x);
    expect(after.y).toBeCloseTo(before.y);
  });

  it("clamps scale", () => {
    expect(clampScale(100)).toBe(MAX_SCALE);
    expect(clampScale(0)).toBe(MIN_SCALE);
    expect(zoomAt({ x: 0, y: 0, scale: 1 }, 99, 0, 0).scale).toBe(MAX_SCALE);
  });

  it("zooms in for negative wheel delta, faster with ctrl", () => {
    expect(wheelZoomFactor(-100, false)).toBeGreaterThan(1);
    expect(wheelZoomFactor(100, false)).toBeLessThan(1);
    expect(wheelZoomFactor(-10, true)).toBeGreaterThan(wheelZoomFactor(-10, false));
  });

  it("centers the focused rect in the container", () => {
    const vp = focusRect(
      { x: 100, y: 200, width: 320, height: 240 },
      { width: 1000, height: 800 },
    );
    const center = worldToScreen(vp, 260, 320);
    expect(center.x).toBeCloseTo(500);
    expect(center.y).toBeCloseTo(400);
  });

  it("fit never zooms beyond 1x and handles empty bounds", () => {
    const small = fitViewport(
      { x: 0, y: 0, width: 100, height: 100 },
      { width: 1000, height: 800 },
    );
    expect(small.scale).toBe(1);
    const empty = fitViewport(
      { x: 0, y: 0, width: 0, height: 0 },
      { width: 1000, height: 800 },
    );
    expect(empty.scale).toBe(1);
  });

  it("computes the visible world rect with margin", () => {
    const rect = visibleWorldRect(
      { x: -100, y: -50, scale: 2 },
      { width: 400, height: 200 },
      20,
    );
    expect(rect).toEqual({ x: 40, y: 15, width: 220, height: 120 });
  });

  it("detects rect intersection", () => {
    const a = { x: 0, y: 0, width: 10, height: 10 };
    expect(rectsIntersect(a, { x: 5, y: 5, width: 10, height: 10 })).toBe(true);
    expect(rectsIntersect(a, { x: 10, y: 0, width: 10, height: 10 })).toBe(false);
  });
});

describe("annotation geometry", () => {
  it("converts a drag to a clamped ratio rect regardless of direction", () => {
    const rect = dragToRatioRect(
      { x: 150, y: 80 },
      { x: -20, y: 20 },
      { width: 200, height: 100 },
    );
    expect(rect?.x).toBe(0);
    expect(rect?.y).toBeCloseTo(0.2);
    expect(rect?.width).toBeCloseTo(0.75);
    expect(rect?.height).toBeCloseTo(0.6);
    expect(
      dragToRatioRect({ x: 0, y: 0 }, { x: 300, y: 300 }, { width: 200, height: 100 }),
    ).toEqual({ x: 0, y: 0, width: 1, height: 1 });
    expect(
      dragToRatioRect({ x: 0, y: 0 }, { x: 5, y: 5 }, { width: 0, height: 0 }),
    ).toBeNull();
  });

  it("normalizes ratio rects so x+width and y+height stay strictly below 1", () => {
    const nearEdge = normalizeRatioRect({
      x: 0.30000000000000004,
      y: 0.5,
      width: 0.7,
      height: 0.5,
    });
    expect(nearEdge.x + nearEdge.width).toBeLessThanOrEqual(1);
    expect(nearEdge.y + nearEdge.height).toBeLessThanOrEqual(1);
    expect(nearEdge.width).toBeGreaterThan(0.69);

    expect(
      normalizeRatioRect({ x: 0.123456, y: 0.2, width: 0.1, height: 0.1 }),
    ).toEqual({ x: 0.1235, y: 0.2, width: 0.1, height: 0.1 });
  });

  it("flags rects under 1% of width or height as too small", () => {
    expect(isAnnotationRectTooSmall({ x: 0, y: 0, width: 0.005, height: 0.5 })).toBe(true);
    expect(isAnnotationRectTooSmall({ x: 0, y: 0, width: 0.5, height: 0.009 })).toBe(true);
    expect(isAnnotationRectTooSmall({ x: 0, y: 0, width: 0.01, height: 0.01 })).toBe(false);
  });

  it("computes the object-contain image box", () => {
    expect(
      containedImageBox({ width: 400, height: 400 }, { width: 200, height: 100 }),
    ).toEqual({ x: 0, y: 100, width: 400, height: 200 });
    expect(
      containedImageBox({ width: 400, height: 200 }, { width: 100, height: 100 }),
    ).toEqual({ x: 100, y: 0, width: 200, height: 200 });
    expect(
      containedImageBox({ width: 400, height: 200 }, { width: 0, height: 0 }),
    ).toBeNull();
  });

  it("derives aspect from size, null when unknown", () => {
    expect(aspectFromSize(100, 200)).toBe(2);
    expect(aspectFromSize(0, 0)).toBeNull();
    expect(aspectFromSize(undefined, 10)).toBeNull();
  });
});

describe("annotation aggregation", () => {
  const latest = (id: string) => (id === "s-1" ? "v-2" : "v-9");

  it("counts open annotations on older versions per screen", () => {
    const counts = countOpenOnOlderVersions(
      [
        annotation({ id: "1", version_id: "v-1" }),
        annotation({ id: "2", version_id: "v-1", status: "resolved" }),
        annotation({ id: "3", version_id: "v-2" }),
        annotation({ id: "4", screen_id: "s-2", version_id: "v-9" }),
      ],
      latest,
    );
    expect(counts.get("s-1")).toBe(1);
    expect(counts.has("s-2")).toBe(false);
  });

  it("counts open annotations that a delete would cascade", () => {
    const list = [
      annotation({ id: "1", version_id: "v-1" }),
      annotation({ id: "2", version_id: "v-2" }),
      annotation({ id: "3", version_id: "v-2", status: "resolved" }),
      annotation({ id: "4", screen_id: "s-2", version_id: "v-3" }),
    ];
    expect(countOpenAnnotations(list, { screenId: "s-1" })).toBe(2);
    expect(countOpenAnnotations(list, { versionId: "v-2" })).toBe(1);
  });

  it("filters panel annotations by scope and status", () => {
    const list = [
      annotation({ id: "1" }),
      annotation({ id: "2", status: "resolved" }),
      annotation({ id: "3", screen_id: "s-2" }),
    ];
    const scope = new Set(["s-1"]);
    expect(filterAnnotationsForPanel(list, scope, "open").map((a) => a.id)).toEqual(["1"]);
    expect(filterAnnotationsForPanel(list, scope, "resolved").map((a) => a.id)).toEqual(["2"]);
    expect(filterAnnotationsForPanel(list, scope, "all").map((a) => a.id)).toEqual(["1", "2"]);
  });
});
