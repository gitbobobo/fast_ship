import { describe, expect, it } from "vitest";
import {
  SCREENSHOT_NONE,
  canonicalizeScreenshotLocale,
  decodeDimensionFilter,
  deriveDimensionOptions,
  dimensionLabel,
  encodeDimensionFilter,
  filterAnnotationsByDimensions,
  filterScreensByDimensions,
  filterScreensByQuery,
  matchesDimension,
  matchesLocaleDimension,
  screenDisplayName,
  toggleDimensionValue,
} from "./screenshots";

function makeScreen(
  overrides: Partial<ScreenshotScreen> = {},
): ScreenshotScreen {
  return {
    id: overrides.id ?? "s-1",
    project_id: "p-1",
    screen_key: "home",
    title: "",
    platform: null,
    locale: null,
    theme: null,
    mime_type: "image/png",
    file_size: 1,
    width: 0,
    height: 0,
    uploaded_by: "bobo",
    uploaded_at: "2026-10-01T00:00:00Z",
    created_at: "2026-10-01T00:00:00Z",
    content_url: "/api/screenshot-screens/s-1/content",
    ...overrides,
  };
}

function makeAnnotation(
  overrides: Partial<ScreenshotAnnotation> = {},
): ScreenshotAnnotation {
  return {
    id: overrides.id ?? "a-1",
    project_id: "p-1",
    screen_id: "s-1",
    issue_id: null,
    issue_reference: null,
    issue_title: null,
    screen_key: "home",
    screen_title: "",
    screen_platform: null,
    screen_locale: null,
    screen_theme: null,
    image_url: "",
    image_width: 0,
    image_height: 0,
    outdated: false,
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

describe("dimension filter URL codec", () => {
  it("round-trips comma separated values", () => {
    const values = ["ios", "android", SCREENSHOT_NONE];
    expect(decodeDimensionFilter(encodeDimensionFilter(values))).toEqual(
      values,
    );
  });

  it("decodes null and drops blank fragments", () => {
    expect(decodeDimensionFilter(null)).toEqual([]);
    expect(decodeDimensionFilter("ios, , android")).toEqual([
      "ios",
      "android",
    ]);
  });

  it("encodes empty selection as empty string (no URL param)", () => {
    expect(encodeDimensionFilter([])).toBe("");
  });
});

describe("deriveDimensionOptions", () => {
  it("counts per value with the untagged bucket and sorts by count desc", () => {
    const screens = [
      makeScreen({ id: "1", platform: "ios" }),
      makeScreen({ id: "2", platform: "ios" }),
      makeScreen({ id: "3", platform: "web" }),
      makeScreen({ id: "4", platform: null }),
    ];
    expect(deriveDimensionOptions(screens, "platform")).toEqual([
      { value: "ios", count: 2 },
      { value: SCREENSHOT_NONE, count: 1 },
      { value: "web", count: 1 },
    ]);
  });

  it("returns empty options for no screens", () => {
    expect(deriveDimensionOptions([], "theme")).toEqual([]);
  });
});

describe("matchesDimension", () => {
  it("empty filter passes everything", () => {
    expect(matchesDimension("ios", [])).toBe(true);
    expect(matchesDimension(null, [])).toBe(true);
  });

  it("matches values or the untagged bucket", () => {
    expect(matchesDimension("ios", ["ios", "web"])).toBe(true);
    expect(matchesDimension("android", ["ios", "web"])).toBe(false);
    expect(matchesDimension(null, [SCREENSHOT_NONE])).toBe(true);
    expect(matchesDimension("ios", [SCREENSHOT_NONE])).toBe(false);
  });
});

describe("toggleDimensionValue", () => {
  it("adds and removes without duplicates", () => {
    expect(toggleDimensionValue([], "ios")).toEqual(["ios"]);
    expect(toggleDimensionValue(["ios", "web"], "ios")).toEqual(["web"]);
  });
});

describe("filterScreensByDimensions", () => {
  const screens = [
    makeScreen({ id: "1", platform: "ios", theme: "dark" }),
    makeScreen({ id: "2", platform: "ios", theme: null }),
    makeScreen({ id: "3", platform: "web", locale: "zh-CN" }),
  ];

  it("filters by each dimension independently (OR within, AND across)", () => {
    expect(
      filterScreensByDimensions(screens, {
        platform: ["ios"],
        locale: [],
        theme: [],
      }).map((s) => s.id),
    ).toEqual(["1", "2"]);
    expect(
      filterScreensByDimensions(screens, {
        platform: ["ios"],
        locale: [],
        theme: [SCREENSHOT_NONE],
      }).map((s) => s.id),
    ).toEqual(["2"]);
    expect(
      filterScreensByDimensions(screens, {
        platform: ["ios", "web"],
        locale: ["zh-CN"],
        theme: [],
      }).map((s) => s.id),
    ).toEqual(["3"]);
  });

  it("empty filters return everything", () => {
    expect(
      filterScreensByDimensions(screens, {
        platform: [],
        locale: [],
        theme: [],
      }),
    ).toHaveLength(3);
  });

  it("matches URL locale=en-us against stored en-US", () => {
    const localized = [
      makeScreen({ id: "en", locale: "en-US" }),
      makeScreen({ id: "zh", locale: "zh-CN" }),
    ];
    expect(
      filterScreensByDimensions(localized, {
        platform: [],
        locale: ["en-us"],
        theme: [],
      }).map((s) => s.id),
    ).toEqual(["en"]);
  });
});

describe("filterScreensByQuery", () => {
  const screens = [
    makeScreen({ id: "a", screen_key: "home", title: "首页" }),
    makeScreen({ id: "b", screen_key: "settings", title: "" }),
  ];

  it("matches title and screen_key case-insensitively", () => {
    expect(filterScreensByQuery(screens, "首页").map((s) => s.id)).toEqual([
      "a",
    ]);
    expect(filterScreensByQuery(screens, "SETT").map((s) => s.id)).toEqual([
      "b",
    ]);
  });

  it("blank query returns everything", () => {
    expect(filterScreensByQuery(screens, "  ")).toHaveLength(2);
  });
});

describe("filterAnnotationsByDimensions", () => {
  const screens = [
    makeScreen({ id: "s1", platform: "ios" }),
    makeScreen({ id: "s2", platform: null }),
  ];
  const byId = new Map(screens.map((s) => [s.id, s]));
  const annotations = [
    makeAnnotation({ id: "a1", screen_id: "s1" }),
    makeAnnotation({ id: "a2", screen_id: "s2" }),
    makeAnnotation({ id: "a3", screen_id: "s-missing" }),
  ];

  it("follows the owning screen's dimensions and drops orphans", () => {
    expect(
      filterAnnotationsByDimensions(annotations, byId, {
        platform: ["ios"],
        locale: [],
        theme: [],
      }).map((a) => a.id),
    ).toEqual(["a1"]);
    expect(
      filterAnnotationsByDimensions(annotations, byId, {
        platform: [SCREENSHOT_NONE],
        locale: [],
        theme: [],
      }).map((a) => a.id),
    ).toEqual(["a2"]);
    expect(
      filterAnnotationsByDimensions(annotations, byId, {
        platform: [],
        locale: [],
        theme: [],
      }).map((a) => a.id),
    ).toEqual(["a1", "a2"]);
  });

  it("matches annotations on screens whose locale differs only by BCP 47 case", () => {
    const localized = [
      makeScreen({ id: "s-en", locale: "en-US" }),
      makeScreen({ id: "s-zh", locale: "zh-CN" }),
    ];
    const byLocale = new Map(localized.map((s) => [s.id, s]));
    const items = [
      makeAnnotation({ id: "a-en", screen_id: "s-en" }),
      makeAnnotation({ id: "a-zh", screen_id: "s-zh" }),
    ];
    expect(
      filterAnnotationsByDimensions(items, byLocale, {
        platform: [],
        locale: ["en-us"],
        theme: [],
      }).map((a) => a.id),
    ).toEqual(["a-en"]);
  });
});

describe("canonicalizeScreenshotLocale", () => {
  it("folds BCP 47 case the way the server stores tags", () => {
    expect(canonicalizeScreenshotLocale("en-us")).toBe("en-US");
    expect(canonicalizeScreenshotLocale("ZH-hans-cn")).toBe("zh-Hans-CN");
    expect(canonicalizeScreenshotLocale("__none__")).toBe("__none__");
  });
});

describe("matchesLocaleDimension", () => {
  it("treats en-us and en-US as the same locale", () => {
    expect(matchesLocaleDimension("en-US", ["en-us"])).toBe(true);
    expect(matchesLocaleDimension("en-US", ["zh-CN"])).toBe(false);
    expect(matchesLocaleDimension(null, ["__none__"])).toBe(true);
  });
});

describe("labels", () => {
  it("labels the untagged bucket", () => {
    expect(dimensionLabel(SCREENSHOT_NONE)).toBe("未标注");
    expect(dimensionLabel("ios")).toBe("ios");
  });

  it("screenDisplayName prefers title", () => {
    expect(
      screenDisplayName(makeScreen({ title: "首页", screen_key: "home" })),
    ).toBe("首页");
    expect(screenDisplayName(makeScreen({ title: "", screen_key: "home" }))).toBe(
      "home",
    );
  });
});
