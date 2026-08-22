import { describe, expect, it } from "vitest";
import { DEFAULT_BOARD_COLUMN_FILTER } from "@/routes/board/components/board-column-filter";
import {
  getColumnScrollKey,
} from "./board-scroll";

describe("board scroll state", () => {
  it("keeps column keys distinct by project, column and filter", () => {
    expect(
      getColumnScrollKey("p1", "todo", DEFAULT_BOARD_COLUMN_FILTER),
    ).not.toBe(getColumnScrollKey("p1", "done", DEFAULT_BOARD_COLUMN_FILTER));
    expect(
      getColumnScrollKey("p1", "todo", DEFAULT_BOARD_COLUMN_FILTER),
    ).not.toBe(
      getColumnScrollKey("p1", "todo", { label: "bug", source: "all" }),
    );
  });

});
