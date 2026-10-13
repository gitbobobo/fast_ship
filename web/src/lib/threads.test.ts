import { describe, expect, it } from "vitest";
import type { ThreadSummary } from "@/lib/api/threads";
import {
  DEFAULT_THREAD_FILTERS,
  filterThreads,
  isDefaultThreadFilters,
  listProjects,
  sortThreads,
} from "@/lib/threads";

function thread(over: Partial<ThreadSummary>): ThreadSummary {
  return {
    id: "t",
    environment_id: "e1",
    environment_name: "Mac",
    project_id: "p1",
    project_name: "Alpha",
    title: "Fix login",
    status: "idle",
    branch: "main",
    provider: "codex",
    model: "gpt",
    updated_at: "2026-10-01T10:00:00Z",
    pull_request_count: 0,
    ...over,
  };
}

describe("sortThreads", () => {
  it("puts waiting threads first and the rest by updated_at desc", () => {
    const sorted = sortThreads([
      thread({ id: "old", updated_at: "2026-10-01T00:00:00Z" }),
      thread({ id: "wait-old", status: "waiting", updated_at: "2026-09-01T00:00:00Z" }),
      thread({ id: "new", status: "running", updated_at: "2026-10-02T00:00:00Z" }),
      thread({ id: "wait-new", status: "waiting", updated_at: "2026-09-02T00:00:00Z" }),
    ]);
    expect(sorted.map((t) => t.id)).toEqual(["wait-new", "wait-old", "new", "old"]);
  });
});

describe("filterThreads", () => {
  const threads = [
    thread({ id: "1", title: "Fix Login bug" }),
    thread({ id: "2", status: "archived", title: "Old work" }),
    thread({
      id: "3",
      environment_id: "e2",
      project_id: "p2",
      project_name: "Beta",
      status: "running",
      title: "Deploy",
    }),
  ];

  it("hides archived threads by default", () => {
    expect(filterThreads(threads, DEFAULT_THREAD_FILTERS).map((t) => t.id)).toEqual(["1", "3"]);
  });

  it("shows only archived when the archived status is chosen", () => {
    expect(
      filterThreads(threads, { ...DEFAULT_THREAD_FILTERS, status: "archived" }).map((t) => t.id),
    ).toEqual(["2"]);
  });

  it("combines multi-environment, project, status and case-insensitive title filters", () => {
    expect(
      filterThreads(threads, { ...DEFAULT_THREAD_FILTERS, environmentIds: ["e1", "e2"] }),
    ).toHaveLength(2);
    expect(
      filterThreads(threads, { ...DEFAULT_THREAD_FILTERS, environmentIds: ["e2"] }).map((t) => t.id),
    ).toEqual(["3"]);
    expect(
      filterThreads(threads, {
        ...DEFAULT_THREAD_FILTERS,
        project: "e2:p2",
        status: "running",
      }).map((t) => t.id),
    ).toEqual(["3"]);
    expect(
      filterThreads(threads, { ...DEFAULT_THREAD_FILTERS, query: " login " }).map((t) => t.id),
    ).toEqual(["1"]);
  });
});

describe("helpers", () => {
  it("lists projects by environment+project id and disambiguates same names", () => {
    const threads = [
      thread({}),
      thread({ id: "x", environment_id: "e2", environment_name: "Linux" }),
      thread({ id: "y", project_id: "p9", project_name: "Beta" }),
      thread({}),
    ];
    expect(listProjects(threads)).toEqual([
      { key: "e2:p1", label: "Alpha · Linux" },
      { key: "e1:p1", label: "Alpha · Mac" },
      { key: "e1:p9", label: "Beta" },
    ]);
  });

  it("filters same-named projects of different environments separately and survives renames", () => {
    const threads = [
      thread({ id: "mac" }),
      thread({ id: "linux", environment_id: "e2", environment_name: "Linux" }),
    ];
    const filters = { ...DEFAULT_THREAD_FILTERS, project: "e2:p1" };
    expect(filterThreads(threads, filters).map((t) => t.id)).toEqual(["linux"]);
    const renamed = threads.map((t) => ({ ...t, project_name: "Renamed" }));
    expect(filterThreads(renamed, filters).map((t) => t.id)).toEqual(["linux"]);
  });

  it("detects default filters", () => {
    expect(isDefaultThreadFilters(DEFAULT_THREAD_FILTERS)).toBe(true);
    expect(isDefaultThreadFilters({ ...DEFAULT_THREAD_FILTERS, query: "x" })).toBe(false);
  });
});
