import { describe, expect, it } from "vitest";
import {
  SCREENSHOT_TAB_ALL,
  SCREENSHOT_TAB_UNGROUPED,
  deriveGroupTabs,
  filterScreensByQuery,
  filterScreensByTab,
  hasUngroupedScreens,
  screenDisplayName,
} from "./screenshots";

function makeScreen(
  overrides: Partial<ScreenshotScreenListItem> = {},
): ScreenshotScreenListItem {
  return {
    id: overrides.id ?? "s-1",
    project_id: "p-1",
    screen_key: "home",
    title: "",
    group: "",
    version_count: 1,
    last_uploaded_at: "2026-10-01T00:00:00Z",
    created_at: "2026-10-01T00:00:00Z",
    latest_version: null,
    ...overrides,
  };
}

describe("deriveGroupTabs", () => {
  it("returns prefixed group tabs ordered by latest upload desc", () => {
    const items = [
      makeScreen({ id: "1", group: "设置", last_uploaded_at: "2026-10-01T00:00:00Z" }),
      makeScreen({ id: "2", group: "首页", last_uploaded_at: "2026-10-05T00:00:00Z" }),
      makeScreen({ id: "3", group: "设置", last_uploaded_at: "2026-10-03T00:00:00Z" }),
      makeScreen({ id: "4", group: "首页", last_uploaded_at: "2026-10-02T00:00:00Z" }),
    ];
    // 首页最近上传 10-05 > 设置最近上传 10-03
    expect(deriveGroupTabs(items)).toEqual(["g:首页", "g:设置"]);
  });

  it("skips ungrouped screens and dedupes group names", () => {
    const items = [
      makeScreen({ id: "1", group: "" }),
      makeScreen({ id: "2", group: "看板" }),
      makeScreen({ id: "3", group: "看板" }),
    ];
    expect(deriveGroupTabs(items)).toEqual(["g:看板"]);
  });

  it("returns empty list when there are no named groups", () => {
    expect(deriveGroupTabs([makeScreen()])).toEqual([]);
    expect(deriveGroupTabs([])).toEqual([]);
  });

  it("prefixes group names that collide with builtin tab values", () => {
    const items = [
      makeScreen({ id: "1", group: "__all__" }),
      makeScreen({ id: "2", group: "__ungrouped__" }),
    ];
    const tabs = deriveGroupTabs(items);
    expect(tabs).toEqual(
      expect.arrayContaining(["g:__all__", "g:__ungrouped__"]),
    );
    expect(tabs).not.toContain(SCREENSHOT_TAB_ALL);
    expect(tabs).not.toContain(SCREENSHOT_TAB_UNGROUPED);
  });
});

describe("hasUngroupedScreens", () => {
  it("detects empty group", () => {
    expect(hasUngroupedScreens([makeScreen({ group: "" })])).toBe(true);
    expect(hasUngroupedScreens([makeScreen({ group: "首页" })])).toBe(false);
  });
});

describe("filterScreensByTab", () => {
  const items = [
    makeScreen({ id: "a", group: "首页" }),
    makeScreen({ id: "b", group: "" }),
    makeScreen({ id: "c", group: "设置" }),
    makeScreen({ id: "d", group: "__ungrouped__" }),
    makeScreen({ id: "e", group: "__all__" }),
  ];

  it("all tab returns everything", () => {
    expect(filterScreensByTab(items, SCREENSHOT_TAB_ALL)).toHaveLength(5);
  });

  it("ungrouped tab returns only empty-group screens", () => {
    const result = filterScreensByTab(items, SCREENSHOT_TAB_UNGROUPED);
    expect(result.map((s) => s.id)).toEqual(["b"]);
  });

  it("named tab filters by decoded group name", () => {
    const result = filterScreensByTab(items, "g:首页");
    expect(result.map((s) => s.id)).toEqual(["a"]);
  });

  it("does not confuse user groups named like builtin tabs", () => {
    expect(
      filterScreensByTab(items, "g:__ungrouped__").map((s) => s.id),
    ).toEqual(["d"]);
    expect(filterScreensByTab(items, "g:__all__").map((s) => s.id)).toEqual([
      "e",
    ]);
  });
});

describe("filterScreensByQuery", () => {
  const items = [
    makeScreen({ id: "a", screen_key: "home", title: "首页" }),
    makeScreen({ id: "b", screen_key: "settings", title: "" }),
  ];

  it("matches title and screen_key case-insensitively", () => {
    expect(
      filterScreensByQuery(items, "首页").map((s) => s.id),
    ).toEqual(["a"]);
    expect(
      filterScreensByQuery(items, "SETT").map((s) => s.id),
    ).toEqual(["b"]);
  });

  it("blank query returns everything", () => {
    expect(filterScreensByQuery(items, "  ")).toHaveLength(2);
  });
});

describe("screenDisplayName", () => {
  it("prefers title and falls back to screen_key", () => {
    expect(screenDisplayName(makeScreen({ title: "首页", screen_key: "home" }))).toBe("首页");
    expect(screenDisplayName(makeScreen({ title: "", screen_key: "home" }))).toBe("home");
  });
});
