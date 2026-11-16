import { describe, expect, it } from "vitest";
import { projectSwitchPath } from "@/lib/project-nav";

describe("projectSwitchPath", () => {
  it("keeps the same list section when switching projects", () => {
    expect(projectSwitchPath("/projects/A/issues", "B")).toBe(
      "/projects/B/issues",
    );
    expect(projectSwitchPath("/projects/A/board", "B")).toBe(
      "/projects/B/board",
    );
    expect(projectSwitchPath("/projects/A/versions", "B")).toBe(
      "/projects/B/versions",
    );
    expect(projectSwitchPath("/projects/A/documents", "B")).toBe(
      "/projects/B/documents",
    );
    expect(projectSwitchPath("/projects/A/screenshots", "B")).toBe(
      "/projects/B/screenshots",
    );
    expect(projectSwitchPath("/projects/A/logs", "B")).toBe(
      "/projects/B/logs",
    );
  });

  it("drops detail/sub paths and lands on the matching list page", () => {
    // 详情与子页无法延续，落到 B 的对应列表页
    expect(projectSwitchPath("/projects/A/issues/new", "B")).toBe(
      "/projects/B/issues",
    );
    expect(projectSwitchPath("/projects/A/issues/i-1", "B")).toBe(
      "/projects/B/issues",
    );
    expect(projectSwitchPath("/projects/A/issues/i-1/edit", "B")).toBe(
      "/projects/B/issues",
    );
    expect(projectSwitchPath("/projects/A/versions/new", "B")).toBe(
      "/projects/B/versions",
    );
    expect(projectSwitchPath("/projects/A/versions/v-1", "B")).toBe(
      "/projects/B/versions",
    );
    expect(projectSwitchPath("/projects/A/logs/run-9", "B")).toBe(
      "/projects/B/logs",
    );
  });

  it("returns null outside project routes or for unknown sections", () => {
    // 全局页 / 旧版单项目路由
    expect(projectSwitchPath("/dashboard", "B")).toBeNull();
    expect(projectSwitchPath("/issues", "B")).toBeNull();
    expect(projectSwitchPath("/logs/run-1", "B")).toBeNull();
    // /projects 本身与未知 section 没有可延续的项目上下文
    expect(projectSwitchPath("/projects", "B")).toBeNull();
    expect(projectSwitchPath("/projects/A", "B")).toBeNull();
    expect(projectSwitchPath("/projects/A/unknown", "B")).toBeNull();
  });

  it("returns null without a target project id and encodes the id", () => {
    expect(projectSwitchPath("/projects/A/issues", "")).toBeNull();
    expect(projectSwitchPath("/projects/a b/issues", "p/1")).toBe(
      `/projects/${encodeURIComponent("p/1")}/issues`,
    );
  });

  it("normalizes section casing the same way matchPath matches routes", () => {
    // matchPath 默认大小写不敏感，切换目标也按同样口径归一，避免切换器静默无效
    expect(projectSwitchPath("/projects/A/ISSUES", "B")).toBe(
      "/projects/B/issues",
    );
  });
});
