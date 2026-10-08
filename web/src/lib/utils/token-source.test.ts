import { describe, expect, it } from "vitest";
import {
  accessTokenSourceProjects,
  parsePrTokenSourceValue,
  prTokenSourceOptions,
} from "@/lib/utils/token-source";

function project(
  partial: Partial<Project> & Pick<Project, "id" | "name">,
): Project {
  return {
    user_id: "u1",
    description: "",
    github_owner: "",
    github_repo: "",
    has_github_token: false,
    has_github_pr_token: false,
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
    ...partial,
  };
}

describe("accessTokenSourceProjects", () => {
  it("只保留配置了 Access Token 的项目", () => {
    const items = [
      project({ id: "p1", name: "with-token", has_github_token: true }),
      project({ id: "p2", name: "no-token" }),
      project({ id: "p3", name: "pr-only", has_github_pr_token: true }),
    ];
    expect(accessTokenSourceProjects(items).map((p) => p.id)).toEqual(["p1"]);
  });

  it("编辑态排除当前项目自身", () => {
    const items = [
      project({ id: "p1", name: "self", has_github_token: true }),
      project({ id: "p2", name: "other", has_github_token: true }),
    ];
    expect(
      accessTokenSourceProjects(items, "p1").map((p) => p.id),
    ).toEqual(["p2"]);
  });
});

describe("prTokenSourceOptions", () => {
  it("双凭证项目出两行：Access Token 行带仓库 slug，PR Token 行不带", () => {
    const items = [
      project({
        id: "p1",
        name: "my-app",
        github_owner: "owner",
        github_repo: "repo",
        has_github_token: true,
        has_github_pr_token: true,
      }),
    ];
    expect(prTokenSourceOptions(items)).toEqual([
      {
        value: "p1:access",
        projectId: "p1",
        kind: "access",
        label: "my-app (owner/repo) · Access Token",
      },
      {
        value: "p1:pr",
        projectId: "p1",
        kind: "pr",
        label: "my-app · PR Token",
      },
    ]);
  });

  it("仅 Access Token 的项目出一行；无仓库时不带 slug", () => {
    const items = [
      project({ id: "p1", name: "my-app", has_github_token: true }),
    ];
    expect(prTokenSourceOptions(items)).toEqual([
      {
        value: "p1:access",
        projectId: "p1",
        kind: "access",
        label: "my-app · Access Token",
      },
    ]);
  });

  it("仅 PR Token 的项目出一行，即使有仓库也不带 slug", () => {
    const items = [
      project({
        id: "p1",
        name: "my-app",
        github_owner: "owner",
        github_repo: "repo",
        has_github_pr_token: true,
      }),
    ];
    expect(prTokenSourceOptions(items)).toEqual([
      {
        value: "p1:pr",
        projectId: "p1",
        kind: "pr",
        label: "my-app · PR Token",
      },
    ]);
  });

  it("无凭证项目不出行", () => {
    const items = [
      project({ id: "p1", name: "empty", github_owner: "o", github_repo: "r" }),
    ];
    expect(prTokenSourceOptions(items)).toEqual([]);
  });

  it("编辑态排除当前项目自身", () => {
    const items = [
      project({ id: "p1", name: "self", has_github_token: true }),
      project({ id: "p2", name: "other", has_github_pr_token: true }),
    ];
    expect(
      prTokenSourceOptions(items, "p1").map((o) => o.value),
    ).toEqual(["p2:pr"]);
  });
});

describe("parsePrTokenSourceValue", () => {
  it.each([
    ["p1:access", { projectId: "p1", kind: "access" }],
    ["p1:pr", { projectId: "p1", kind: "pr" }],
    ["uuid-with-dash:access", { projectId: "uuid-with-dash", kind: "access" }],
  ])("解析复合值 %s", (value, expected) => {
    expect(parsePrTokenSourceValue(value)).toEqual(expected);
  });

  it.each(["", "p1", "p1:other", ":access"])("非法值 %s 返回 null", (value) => {
    expect(parsePrTokenSourceValue(value)).toBeNull();
  });
});
