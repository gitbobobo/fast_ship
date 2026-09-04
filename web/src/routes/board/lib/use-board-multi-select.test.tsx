import { act, fireEvent, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useBoardMultiSelect } from "./use-board-multi-select";

function makeIssue(id: string): Issue {
  return {
    id,
    project_id: "project-1",
    source: "internal",
    sequence_number: 1,
    reference: `#${id}`,
    state: "open",
    state_reason: "",
    title: `问题 ${id}`,
    body: "",
    body_html: "",
    author: { login: "alice", avatar_url: "" },
    unread_comments_count: 0,
    created_at: "2026-08-22T00:00:00Z",
    updated_at: "2026-08-22T00:00:00Z",
    internal_meta: {
      workflow_status: "todo",
      checklist_total: 0,
      checklist_done: 0,
      labels: [],
    },
  };
}

const todoIssues = ["t1", "t2", "t3", "t4"].map(makeIssue);
const doneIssues = ["d1", "d2"].map(makeIssue);

describe("useBoardMultiSelect", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  function setup(projectId = "project-1") {
    const hook = renderHook((id: string) => useBoardMultiSelect(id), {
      initialProps: projectId,
    });

    const registerColumns = () => {
      act(() => {
        hook.result.current.handleColumnIssuesChange("todo", todoIssues);
        hook.result.current.handleColumnIssuesChange("done", doneIssues);
      });
    };

    const enterMode = () => {
      act(() => {
        hook.result.current.toggleMode();
      });
    };

    const selectIssue = (issue: Issue, shiftKey = false) => {
      act(() => {
        hook.result.current.selectIssue(issue, { shiftKey });
      });
    };

    const selectedIds = () =>
      Array.from(hook.result.current.selectedIssueIds).sort();

    return { hook, registerColumns, enterMode, selectIssue, selectedIds };
  }

  it("starts outside the mode with an empty selection", () => {
    const { hook } = setup();

    expect(hook.result.current.multiSelectMode).toBe(false);
    expect(hook.result.current.selectPreview).toBe(false);
    expect(hook.result.current.selectedCount).toBe(0);
    expect(hook.result.current.selectedIssueIds.size).toBe(0);
  });

  it("toggleMode enters and exits the mode clearing selection and anchor", () => {
    const { hook, registerColumns, enterMode, selectIssue, selectedIds } =
      setup();

    enterMode();
    expect(hook.result.current.multiSelectMode).toBe(true);

    registerColumns();
    selectIssue(todoIssues[0]);
    expect(selectedIds()).toEqual(["t1"]);

    act(() => {
      hook.result.current.toggleMode();
    });
    expect(hook.result.current.multiSelectMode).toBe(false);
    expect(hook.result.current.selectedCount).toBe(0);

    // 锚点也被清空：重新进入后 Shift 退化为普通切换
    enterMode();
    selectIssue(todoIssues[2], true);
    expect(selectedIds()).toEqual(["t3"]);
  });

  it("selectIssue outside the mode enters with the issue selected and anchored", () => {
    const { hook, registerColumns, selectIssue, selectedIds } = setup();

    registerColumns();
    selectIssue(todoIssues[0]);

    expect(hook.result.current.multiSelectMode).toBe(true);
    expect(selectedIds()).toEqual(["t1"]);

    // 锚点是 t1：Shift 点 t3 应选中 t1..t3，而不是退化为只切换 t3
    selectIssue(todoIssues[2], true);
    expect(selectedIds()).toEqual(["t1", "t2", "t3"]);
  });

  it("shift-selects a range within the same column when the anchor is above", () => {
    const { registerColumns, enterMode, selectIssue, selectedIds } = setup();

    registerColumns();
    enterMode();
    selectIssue(todoIssues[0]);
    selectIssue(todoIssues[2], true);

    expect(selectedIds()).toEqual(["t1", "t2", "t3"]);
  });

  it("shift-selects a range within the same column when the anchor is below", () => {
    const { registerColumns, enterMode, selectIssue, selectedIds } = setup();

    registerColumns();
    enterMode();
    selectIssue(todoIssues[3]);
    selectIssue(todoIssues[1], true);

    expect(selectedIds()).toEqual(["t2", "t3", "t4"]);
  });

  it("shift range only adds ids and never clears other columns", () => {
    const { registerColumns, enterMode, selectIssue, selectedIds } = setup();

    registerColumns();
    enterMode();
    selectIssue(todoIssues[0]);
    selectIssue(doneIssues[0]);
    expect(selectedIds()).toEqual(["d1", "t1"]);

    // 范围选不清其他列
    selectIssue(todoIssues[1], true);
    expect(selectedIds()).toEqual(["d1", "t1", "t2"]);

    // 范围选不反向取消范围外的勾选：t1 在范围外仍保留
    selectIssue(todoIssues[2], true);
    expect(selectedIds()).toEqual(["d1", "t1", "t2", "t3"]);

    // 普通点击取消勾选
    selectIssue(todoIssues[1]);
    expect(selectedIds()).toEqual(["d1", "t1", "t3"]);
  });

  it("degenerates to a plain toggle when shift-clicking across columns", () => {
    const { registerColumns, enterMode, selectIssue, selectedIds } = setup();

    registerColumns();
    enterMode();
    selectIssue(todoIssues[0]);

    selectIssue(doneIssues[1], true);

    // 不是范围选（否则 d1 也会被选中），只是切换 d2
    expect(selectedIds()).toEqual(["d2", "t1"]);
  });

  it("degenerates to a plain toggle when there is no anchor", () => {
    const { registerColumns, enterMode, selectIssue, selectedIds } = setup();

    registerColumns();
    enterMode();
    selectIssue(todoIssues[2], true);

    expect(selectedIds()).toEqual(["t3"]);
  });

  it("degenerates to a plain toggle when the anchor issue disappeared from the column", () => {
    const { hook, registerColumns, enterMode, selectIssue, selectedIds } =
      setup();

    registerColumns();
    enterMode();
    selectIssue(todoIssues[0]);

    act(() => {
      hook.result.current.handleColumnIssuesChange("todo", [
        todoIssues[1],
        todoIssues[2],
      ]);
    });

    selectIssue(todoIssues[2], true);

    expect(selectedIds()).toEqual(["t3"]);
  });

  it("prunes only disappeared ids of the changed column and never auto-selects new ids", () => {
    const { hook, registerColumns, enterMode, selectIssue, selectedIds } =
      setup();

    registerColumns();
    enterMode();
    selectIssue(todoIssues[0]);
    selectIssue(doneIssues[0]);
    expect(selectedIds()).toEqual(["d1", "t1"]);

    const newIssue = makeIssue("t9");
    act(() => {
      hook.result.current.handleColumnIssuesChange("todo", [
        todoIssues[1],
        newIssue,
      ]);
    });

    expect(selectedIds()).toEqual(["d1"]);

    // 翻页新增的 id 不自动勾选
    const pageTwoIssue = makeIssue("t10");
    act(() => {
      hook.result.current.handleColumnIssuesChange("done", [
        ...doneIssues,
        pageTwoIssue,
      ]);
    });
    expect(selectedIds()).toEqual(["d1"]);
    expect(hook.result.current.selectedIssueIds.has("d1")).toBe(true);
    expect(hook.result.current.selectedIssueIds.has("t10")).toBe(false);
  });

  it("clearSelection clears the selection but keeps the mode", () => {
    const { hook, registerColumns, enterMode, selectIssue, selectedIds } =
      setup();

    registerColumns();
    enterMode();
    selectIssue(todoIssues[0]);

    act(() => {
      hook.result.current.clearSelection();
    });

    expect(hook.result.current.multiSelectMode).toBe(true);
    expect(selectedIds()).toEqual([]);
  });

  it("exit leaves the mode and clears everything", () => {
    const { hook, registerColumns, enterMode, selectIssue, selectedIds } =
      setup();

    registerColumns();
    enterMode();
    selectIssue(todoIssues[0]);

    act(() => {
      hook.result.current.exit();
    });

    expect(hook.result.current.multiSelectMode).toBe(false);
    expect(selectedIds()).toEqual([]);
  });

  it("exits when activeProjectId changes but not on the first render", () => {
    const { hook, registerColumns, enterMode, selectIssue, selectedIds } =
      setup();

    registerColumns();
    enterMode();
    selectIssue(todoIssues[0]);
    expect(hook.result.current.multiSelectMode).toBe(true);

    // 同一项目重复渲染不退出
    hook.rerender("project-1");
    expect(hook.result.current.multiSelectMode).toBe(true);

    hook.rerender("project-2");
    expect(hook.result.current.multiSelectMode).toBe(false);
    expect(selectedIds()).toEqual([]);
  });

  it("returns selected issues ordered by column order and loaded order", () => {
    const unsetIssues = [makeIssue("u1")];
    const inProgressIssues = [makeIssue("p1"), makeIssue("p2")];
    const { hook, enterMode, selectIssue } = setup();

    act(() => {
      hook.result.current.handleColumnIssuesChange("unset", unsetIssues);
      hook.result.current.handleColumnIssuesChange("todo", todoIssues);
      hook.result.current.handleColumnIssuesChange(
        "in_progress",
        inProgressIssues,
      );
      hook.result.current.handleColumnIssuesChange("done", doneIssues);
    });

    enterMode();
    // 选中的顺序故意打乱：done -> todo -> unset -> in_progress
    selectIssue(doneIssues[0]);
    selectIssue(todoIssues[1]);
    selectIssue(unsetIssues[0]);
    selectIssue(inProgressIssues[1]);

    expect(hook.result.current.getOrderedSelectedIssues().map((i) => i.id)).toEqual([
      "u1",
      "t2",
      "p2",
      "d1",
    ]);
  });

  it("exits on Escape keydown only while in the mode", () => {
    const { hook, enterMode } = setup();

    // 模式外 Esc 不生效（监听未挂载）
    fireEvent.keyDown(window, { key: "Escape" });
    expect(hook.result.current.multiSelectMode).toBe(false);

    enterMode();
    fireEvent.keyDown(window, { key: "a" });
    expect(hook.result.current.multiSelectMode).toBe(true);

    fireEvent.keyDown(window, { key: "Escape" });
    expect(hook.result.current.multiSelectMode).toBe(false);
  });

  it("turns on selectPreview while Control or Meta is held outside the mode", () => {
    const { hook } = setup();

    fireEvent.keyDown(window, { key: "Control", ctrlKey: true });
    expect(hook.result.current.selectPreview).toBe(true);

    fireEvent.keyUp(window, { key: "Control", ctrlKey: false });
    expect(hook.result.current.selectPreview).toBe(false);

    fireEvent.keyDown(window, { key: "Meta", metaKey: true });
    expect(hook.result.current.selectPreview).toBe(true);
  });

  it("does not preview selection when already in the mode", () => {
    const { hook, enterMode } = setup();

    enterMode();
    fireEvent.keyDown(window, { key: "Control", ctrlKey: true });
    expect(hook.result.current.selectPreview).toBe(false);
    expect(hook.result.current.multiSelectMode).toBe(true);
  });

  it("clears selectPreview when the window blurs", () => {
    const { hook } = setup();

    fireEvent.keyDown(window, { key: "Control", ctrlKey: true });
    expect(hook.result.current.selectPreview).toBe(true);

    fireEvent.blur(window);
    expect(hook.result.current.selectPreview).toBe(false);
  });

  it("ignores Escape events that were already prevented", () => {
    const { hook, enterMode } = setup();

    enterMode();

    const event = new KeyboardEvent("keydown", {
      key: "Escape",
      cancelable: true,
    });
    event.preventDefault();
    fireEvent(window, event);

    expect(hook.result.current.multiSelectMode).toBe(true);
  });
});
