import { describe, expect, it } from "vitest";
import { getActiveProjectId, shelvedStatusLabel } from "./projects";

function makeProject(
  id: string,
  opts: { shelved?: boolean; until?: string | null } = {},
): Project {
  const { shelved = false, until = null } = opts;
  return {
    id,
    name: `项目-${id}`,
    description: "",
    github_owner: "",
    github_repo: "",
    code_github_owner: "",
    code_github_repo: "",
    public_github_token_id: "",
    code_github_token_id: "",
    shelved_at: shelved ? "2026-10-10T00:00:00Z" : null,
    shelved_until: shelved ? until : null,
    is_shelved: shelved,
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
  };
}

describe("getActiveProjectId", () => {
  const projects = [
    makeProject("a"),
    makeProject("b", { shelved: true }),
    makeProject("c"),
  ];

  it("已选项在列表中直接沿用，即使是搁置项目", () => {
    expect(getActiveProjectId(projects, "b", null)).toBe("b");
  });

  it("已选项不在列表中时回落到 URL 指定项目，含搁置项目", () => {
    expect(getActiveProjectId(projects, "ghost", "b")).toBe("b");
    expect(getActiveProjectId(projects, "ghost", "c")).toBe("c");
  });

  it("两个指定都未命中时选第一个未搁置项目", () => {
    expect(getActiveProjectId(projects, "ghost", "ghost2")).toBe("a");
    const shelvedFirst = [
      makeProject("x", { shelved: true }),
      makeProject("y"),
      makeProject("z"),
    ];
    expect(getActiveProjectId(shelvedFirst, "", null)).toBe("y");
  });

  it("全部项目搁置时回落到列表首项", () => {
    const allShelved = [
      makeProject("x", { shelved: true }),
      makeProject("y", { shelved: true }),
    ];
    expect(getActiveProjectId(allShelved, "", null)).toBe("x");
  });

  it("空列表返回空串", () => {
    expect(getActiveProjectId([], "", null)).toBe("");
  });
});

describe("shelvedStatusLabel", () => {
  it("未搁置返回 null", () => {
    expect(shelvedStatusLabel(makeProject("a"))).toBeNull();
  });

  it("无限期搁置显示「已搁置」", () => {
    expect(
      shelvedStatusLabel(makeProject("a", { shelved: true })),
    ).toBe("已搁置");
  });

  it("有截止日期显示「搁置至 YYYY-MM-DD」", () => {
    const label = shelvedStatusLabel(
      makeProject("a", {
        shelved: true,
        // UTC 23 点在不晚于 +23 时区里都是同一天，本地日期为 11-01
        until: "2026-11-01T12:00:00Z",
      }),
    );
    expect(label).toMatch(/^搁置至 \d{4}-\d{2}-\d{2}$/);
  });
});
