import { describe, expect, it } from "vitest";
import {
  getLocationScrollKey,
  getSavedScroll,
  resetScrollPositions,
  setSavedScroll,
} from "./scroll-positions";

describe("scroll positions", () => {
  it("stores offsets by key and resets them", () => {
    setSavedScroll("issues", 240);
    setSavedScroll("projects", 80);

    expect(getSavedScroll("issues")).toBe(240);
    expect(getSavedScroll("projects")).toBe(80);
    expect(getSavedScroll("missing")).toBe(0);

    resetScrollPositions();
    expect(getSavedScroll("issues")).toBe(0);
  });

  it("includes pathname and search in the layout key", () => {
    expect(getLocationScrollKey("/issues", "?project=p1")).toBe(
      "main:/issues?project=p1",
    );
    expect(getLocationScrollKey("/projects")).not.toBe(
      getLocationScrollKey("/issues"),
    );
  });
});
