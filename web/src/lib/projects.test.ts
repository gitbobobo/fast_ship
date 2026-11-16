import { describe, expect, it } from "vitest";
import { shelvedStatusLabel } from "./projects";

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
    repository_url: null,
    code_github_owner: "",
    code_repo_url: null,
    code_github_repo: "",
    public_github_token_id: "",
    code_github_token_id: "",
    code_integration_id: null,
    public_integration_id: null,
    shelved_at: shelved ? "2026-10-10T00:00:00Z" : null,
    shelved_until: shelved ? until : null,
    is_shelved: shelved,
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
  };
}

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
