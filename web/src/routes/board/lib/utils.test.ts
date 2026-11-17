import { describe, expect, it } from "vitest";
import { COLUMNS, getBoardWorkflowStatusParam } from "./utils";

describe("board workflow status param", () => {
  it("未设置列与列表 unset 过滤同源：statusValue 为 ''，编码为 unset", () => {
    const unsetColumn = COLUMNS.find((column) => column.id === "unset");
    expect(unsetColumn?.statusValue).toBe("");
    expect(getBoardWorkflowStatusParam("")).toBe("unset");
  });

  it("其余列同名透传", () => {
    expect(getBoardWorkflowStatusParam("todo")).toBe("todo");
    expect(getBoardWorkflowStatusParam("in_progress")).toBe("in_progress");
    expect(getBoardWorkflowStatusParam("done")).toBe("done");
  });

  it("未知取值返回 null，由调用方决定兜底", () => {
    expect(getBoardWorkflowStatusParam("bogus")).toBe(null);
  });
});
