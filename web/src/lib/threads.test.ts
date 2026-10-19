import { describe, expect, it } from "vitest";
import type { ThreadSummary } from "@/lib/api/threads";
import {
  DEFAULT_THREAD_FILTERS,
  countThreadsByEnvironment,
  filterThreads,
  formatRunMinutes,
  isDefaultThreadFilters,
  listProjects,
  projectColorIndex,
  projectInitial,
  runMinutes,
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
    settled: false,
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

  it("combines environment, project, status and case-insensitive title filters", () => {
    expect(
      filterThreads(threads, { ...DEFAULT_THREAD_FILTERS, environmentId: "e2" }).map((t) => t.id),
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

  it("filters by settled independently of status and keeps archived hidden by default", () => {
    const list = [
      thread({ id: "a", settled: true }),
      thread({ id: "b", status: "running" }),
      thread({ id: "c", status: "archived", settled: true }),
      thread({ id: "d", status: "archived" }),
    ];
    const ids = (f: Partial<typeof DEFAULT_THREAD_FILTERS>) =>
      filterThreads(list, { ...DEFAULT_THREAD_FILTERS, ...f }).map((t) => t.id);
    expect(ids({ settled: "settled" })).toEqual(["a"]);
    expect(ids({ settled: "unsettled" })).toEqual(["b"]);
    expect(ids({ settled: "settled", status: "archived" })).toEqual(["c"]);
    expect(ids({ settled: "unsettled", status: "archived" })).toEqual(["d"]);
    expect(ids({ settled: "unsettled", status: "running" })).toEqual(["b"]);
    expect(ids({ settled: "settled", status: "running" })).toEqual([]);
  });
});

describe("countThreadsByEnvironment", () => {
  const threads = [
    thread({ id: "1", title: "Login" }),
    thread({ id: "2", status: "archived", title: "Old" }),
    thread({ id: "3", environment_id: "e2", environment_name: "Linux", title: "Deploy" }),
    thread({ id: "4", environment_id: "e2", environment_name: "Linux", status: "running", title: "Ship it" }),
  ];

  it("counts matched threads per status, per environment and in total", () => {
    const counts = countThreadsByEnvironment(threads, DEFAULT_THREAD_FILTERS);
    expect(counts.all).toEqual({
      running: 1,
      waiting: 0,
      idle: 2,
      archived: 0,
      total: 3,
    });
    expect(counts.byEnvironment.get("e1")).toEqual({
      running: 0,
      waiting: 0,
      idle: 1,
      archived: 0,
      total: 1,
    });
    expect(counts.byEnvironment.get("e2")).toEqual({
      running: 1,
      waiting: 0,
      idle: 1,
      archived: 0,
      total: 2,
    });
  });

  it("keeps total consistent with the per-status buckets and the visible list", () => {
    const list = [thread({ id: "w", status: "waiting" }), ...threads];
    const counts = countThreadsByEnvironment(list, DEFAULT_THREAD_FILTERS);
    const { total, ...buckets } = counts.all;
    expect(Object.values(buckets).reduce((sum, n) => sum + n, 0)).toBe(total);
    expect(total).toBe(
      filterThreads(list, { ...DEFAULT_THREAD_FILTERS, environmentId: "" })
        .length,
    );
    expect(counts.all.waiting).toBe(1);
  });

  it("ignores the environment selection so other environments stay countable", () => {
    const filters = { ...DEFAULT_THREAD_FILTERS, environmentId: "e2" };
    const counts = countThreadsByEnvironment(threads, filters);
    expect(counts.all.total).toBe(3);
    expect(counts.byEnvironment.get("e1")?.total).toBe(1);
    expect(counts.byEnvironment.get("e2")?.total).toBe(2);
  });

  it("stacks the remaining filters (query, status) into the counts", () => {
    const byStatus = countThreadsByEnvironment(threads, {
      ...DEFAULT_THREAD_FILTERS,
      status: "running",
    });
    expect(byStatus.byEnvironment.get("e2")).toEqual({
      running: 1,
      waiting: 0,
      idle: 0,
      archived: 0,
      total: 1,
    });
    expect(byStatus.all.total).toBe(1);
    const byQuery = countThreadsByEnvironment(threads, {
      ...DEFAULT_THREAD_FILTERS,
      query: "login",
    });
    expect(byQuery.all.total).toBe(1);
    expect(byQuery.byEnvironment.get("e1")?.idle).toBe(1);
    expect(byQuery.byEnvironment.has("e2")).toBe(false);
  });

  it("does not count archived threads unless archived status is selected", () => {
    const counts = countThreadsByEnvironment(threads, DEFAULT_THREAD_FILTERS);
    expect(counts.all.archived).toBe(0);
    expect(counts.byEnvironment.get("e1")?.total).toBe(1);
    const archived = countThreadsByEnvironment(threads, {
      ...DEFAULT_THREAD_FILTERS,
      status: "archived",
    });
    expect(archived.all).toEqual({
      running: 0,
      waiting: 0,
      idle: 0,
      archived: 1,
      total: 1,
    });
    expect(archived.byEnvironment.get("e1")?.archived).toBe(1);
    expect(archived.byEnvironment.has("e2")).toBe(false);
  });

  it("counts settled threads into the environment buckets (they live in the collapsed section)", () => {
    const mixed = [
      thread({ id: "open", environment_id: "e1" }),
      thread({ id: "done-1", environment_id: "e1", settled: true }),
      thread({ id: "done-2", environment_id: "e1", settled: true }),
    ];
    const counts = countThreadsByEnvironment(mixed, DEFAULT_THREAD_FILTERS);
    // 默认视图里这 3 条都命中（2 条已完结收在折叠区），pill 数字按命中数算。
    expect(counts.byEnvironment.get("e1")).toEqual({
      running: 0,
      waiting: 0,
      idle: 3,
      archived: 0,
      total: 3,
    });
    // 选了「未完结」后已完结不再计入。
    const unsettled = countThreadsByEnvironment(mixed, {
      ...DEFAULT_THREAD_FILTERS,
      settled: "unsettled",
    });
    expect(unsettled.byEnvironment.get("e1")?.total).toBe(1);
  });

  it("folds project and settled filters into the counts", () => {
    const scoped = [
      thread({ id: "a", project_id: "p2", project_name: "Beta" }),
      thread({ id: "b" }),
      thread({ id: "c", environment_id: "e2", project_id: "p2", project_name: "Beta" }),
      thread({ id: "d", project_id: "p3", project_name: "Gamma", settled: true }),
    ];
    const byProject = countThreadsByEnvironment(scoped, {
      ...DEFAULT_THREAD_FILTERS,
      project: "e1:p1",
    });
    expect(byProject.all.total).toBe(1);
    expect(byProject.byEnvironment.get("e1")?.total).toBe(1);
    expect(byProject.byEnvironment.has("e2")).toBe(false);

    const settled = countThreadsByEnvironment(scoped, {
      ...DEFAULT_THREAD_FILTERS,
      settled: "settled",
    });
    expect(settled.all.total).toBe(1);
    expect(settled.byEnvironment.get("e1")?.total).toBe(1);
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
    expect(isDefaultThreadFilters({ ...DEFAULT_THREAD_FILTERS, settled: "settled" })).toBe(false);
  });

  it("computes run minutes and tolerates missing, invalid or future starts", () => {
    const now = Date.parse("2026-10-14T10:00:00Z");
    expect(runMinutes("2026-10-14T09:18:30Z", now)).toBe(41);
    expect(runMinutes("2026-10-14T09:59:59Z", now)).toBe(0);
    expect(runMinutes(undefined, now)).toBeUndefined();
    expect(runMinutes("not a date", now)).toBeUndefined();
    expect(runMinutes("2026-10-14T10:05:00Z", now)).toBeUndefined();
  });

  it("formats run minutes", () => {
    expect(formatRunMinutes(0)).toBe("不足 1 分钟");
    expect(formatRunMinutes(42)).toBe("42 分钟");
    expect(formatRunMinutes(60)).toBe("1 小时");
    expect(formatRunMinutes(125)).toBe("2 小时 5 分钟");
  });

  it("derives a stable project badge", () => {
    expect(projectInitial(" alpha")).toBe("A");
    expect(projectInitial("项目")).toBe("项");
    expect(projectInitial("")).toBe("?");
    expect(projectColorIndex("Alpha", 8)).toBe(projectColorIndex("Alpha", 8));
    expect(projectColorIndex("Alpha", 8)).toBeLessThan(8);
  });
});
