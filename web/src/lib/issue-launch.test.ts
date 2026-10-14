import { describe, expect, it } from "vitest";

import type { Environment, ThreadSummary } from "@/lib/api/threads";
import type { ProjectT3Binding } from "@/lib/api/t3-bindings";
import type { ProviderChoice } from "@/lib/threads-compose";
import { makeEnvironment } from "@/test/threads";
import {
  bindingInvalidReason,
  countBusyThreads,
  describeIssuePromptPreferences,
  isLaunchable,
  joinEnvironmentsWithBindings,
  pickAutoEnvironmentRow,
  preferenceModelPick,
  resolveEnvironmentChoice,
  unlaunchableReason,
} from "./issue-launch";

function makeBinding(environmentId: string): ProjectT3Binding {
  return {
    project_id: "proj-1",
    environment_id: environmentId,
    environment_name: "env",
    environment_status: "ok",
    environment_access: "operate",
    t3_project_id: `t3-${environmentId}`,
    t3_project_title: "Alpha",
    workspace_root: "/w/alpha",
  };
}

function makeThread(
  environmentId: string,
  status: ThreadSummary["status"],
): ThreadSummary {
  return {
    id: `${environmentId}-${status}-${Math.random()}`,
    environment_id: environmentId,
    environment_name: environmentId,
    project_id: "p1",
    project_name: "Alpha",
    title: "t",
    status,
    settled: false,
    branch: "main",
    provider: "codex",
    model: "gpt-5",
    updated_at: "2026-10-01T00:00:00Z",
    pull_request_count: 0,
  };
}

const environments: Environment[] = [
  makeEnvironment({ id: "e1", name: "Mac" }),
  makeEnvironment({ id: "e2", name: "Linux" }),
  makeEnvironment({ id: "e3", name: "Nas" }),
];

describe("joinEnvironmentsWithBindings", () => {
  it("keeps environment order and attaches at most one binding per environment", () => {
    const rows = joinEnvironmentsWithBindings(environments, [
      makeBinding("e2"),
      makeBinding("e1"),
    ]);

    expect(rows.map((r) => r.environment.id)).toEqual(["e1", "e2", "e3"]);
    expect(rows[0].binding?.t3_project_id).toBe("t3-e1");
    expect(rows[1].binding?.t3_project_id).toBe("t3-e2");
    expect(rows[2].binding).toBeUndefined();
  });
});

describe("isLaunchable", () => {
  const row = (env: Partial<Environment>, bound = true) => ({
    environment: makeEnvironment(env),
    binding: bound ? makeBinding("e1") : undefined,
  });

  it("requires a binding, ok status and operate access", () => {
    expect(isLaunchable(row({}, true))).toBe(true);
    expect(isLaunchable(row({}, false))).toBe(false);
    expect(isLaunchable(row({ status: "unreachable" }, true))).toBe(false);
    expect(isLaunchable(row({ status: "degraded" }, true))).toBe(false);
    expect(
      isLaunchable(
        row({ access: "read-only", scopes: ["orchestration:read"] }, true),
      ),
    ).toBe(false);
  });
});

describe("countBusyThreads + pickAutoEnvironmentRow", () => {
  it("counts only running and waiting threads of that environment", () => {
    const threads = [
      makeThread("e1", "running"),
      makeThread("e1", "waiting"),
      makeThread("e1", "idle"),
      makeThread("e1", "archived"),
      makeThread("e2", "running"),
    ];
    expect(countBusyThreads(threads, "e1")).toBe(2);
    expect(countBusyThreads(threads, "e2")).toBe(1);
    expect(countBusyThreads(threads, "e3")).toBe(0);
  });

  it("picks the least busy launchable candidate, ties keep list order", () => {
    const rows = joinEnvironmentsWithBindings(environments, [
      makeBinding("e1"),
      makeBinding("e2"),
      makeBinding("e3"),
    ]);
    const threads = [
      makeThread("e1", "running"),
      makeThread("e1", "running"),
      makeThread("e2", "waiting"),
    ];

    // e3（0 忙碌）胜出
    expect(pickAutoEnvironmentRow(rows, threads)?.environment.id).toBe("e3");
    // e2 与 e3 同忙时取列表靠前的 e2
    const tied = [makeThread("e1", "running"), makeThread("e1", "running")];
    expect(pickAutoEnvironmentRow(rows, tied)?.environment.id).toBe("e2");
  });

  it("skips unbound and unusable environments", () => {
    const rows = joinEnvironmentsWithBindings(
      [
        makeEnvironment({ id: "e1", status: "unreachable" }),
        makeEnvironment({ id: "e2" }),
        makeEnvironment({ id: "e3", access: "read-only" }),
      ],
      [makeBinding("e1"), makeBinding("e3")],
    );
    expect(pickAutoEnvironmentRow(rows, [])).toBeUndefined();
  });
});

describe("unlaunchableReason", () => {
  it("describes read-only and unhealthy bound environments", () => {
    expect(
      unlaunchableReason({
        environment: makeEnvironment({ access: "read-only" }),
        binding: makeBinding("e1"),
      }),
    ).toBe("只读");
    expect(
      unlaunchableReason({
        environment: makeEnvironment({ status: "unreachable" }),
        binding: makeBinding("e1"),
      }),
    ).toBe("不可达");
  });
});

describe("preferenceModelPick", () => {
  const providers: ProviderChoice[] = [
    {
      instanceId: "codex",
      name: "Codex",
      models: [
        { slug: "gpt-5", name: "GPT-5", isDefault: true, descriptors: [] },
      ],
    },
  ];

  it("maps a stored selection when the provider and model exist", () => {
    expect(
      preferenceModelPick(
        {
          instance_id: "codex",
          model: "gpt-5",
          options: [
            { id: "effort", value: "high" },
            { id: "fast", value: true },
          ],
        },
        providers,
      ),
    ).toEqual({
      instanceId: "codex",
      model: "gpt-5",
      // 布尔选项不是推理强度，不进 efforts
      efforts: { effort: "high" },
    });
  });

  it("returns null when the instance or model is unavailable", () => {
    expect(
      preferenceModelPick(
        { instance_id: "other", model: "gpt-5" },
        providers,
      ),
    ).toBeNull();
    expect(
      preferenceModelPick(
        { instance_id: "codex", model: "o3" },
        providers,
      ),
    ).toBeNull();
    expect(preferenceModelPick(undefined, providers)).toBeNull();
  });
});

describe("bindingInvalidReason", () => {
  const binding = makeBinding("e1");

  it("reports environment trouble before looking at discovery", () => {
    expect(
      bindingInvalidReason(
        binding,
        makeEnvironment({ status: "unreachable" }),
        undefined,
      ),
    ).toBe("环境不可达");
  });

  it("returns null when discovery has not delivered a row for the environment", () => {
    // 没拉到发现数据不等于绑定失效，避免误标红
    expect(
      bindingInvalidReason(binding, makeEnvironment(), undefined),
    ).toBeNull();
  });

  it("does not flag bindings on archive-only discovery failure", () => {
    // 归档快照失败是部分失败，活跃项目照常返回；绑定的项目可能只在
    // 归档面，没拉到不下「已删除」结论
    expect(
      bindingInvalidReason(binding, makeEnvironment(), {
        environment_id: "e1",
        name: "Mac",
        status: "ok",
        access: "operate",
        error: "daemon 超时",
      }),
    ).toBeNull();
  });

  it("keeps a bound project valid when the active list still contains it", () => {
    expect(
      bindingInvalidReason(binding, makeEnvironment(), {
        environment_id: "e1",
        name: "Mac",
        status: "ok",
        access: "operate",
        error: "归档读取失败",
        projects: [
          { id: "t3-e1", title: "Alpha", workspace_root: "/w/alpha" },
        ],
      }),
    ).toBeNull();
  });

  it("flags a snapshot drift: missing project, renamed title, moved root", () => {
    const ok = {
      environment_id: "e1",
      name: "Mac",
      status: "ok" as const,
      access: "operate" as const,
      projects: [
        { id: "t3-e1", title: "Alpha", workspace_root: "/w/alpha" },
      ],
    };
    expect(
      bindingInvalidReason(binding, makeEnvironment(), ok),
    ).toBeNull();
    expect(
      bindingInvalidReason(binding, makeEnvironment(), {
        ...ok,
        projects: [],
      }),
    ).toBe("绑定的 T3 项目已不存在");
    expect(
      bindingInvalidReason(binding, makeEnvironment(), {
        ...ok,
        projects: [{ id: "t3-e1", title: "Renamed", workspace_root: "/w/alpha" }],
      }),
    ).toBe("T3 项目已改名");
    expect(
      bindingInvalidReason(binding, makeEnvironment(), {
        ...ok,
        projects: [{ id: "t3-e1", title: "Alpha", workspace_root: "/w/moved" }],
      }),
    ).toBe("T3 项目工作区已变化");
  });
});

describe("resolveEnvironmentChoice", () => {
  const rows = joinEnvironmentsWithBindings(environments, [
    makeBinding("e1"),
    makeBinding("e2"),
  ]);

  it("defaults to auto without a preference", () => {
    expect(resolveEnvironmentChoice(undefined, rows)).toEqual({
      choice: "auto",
      preferredMissing: false,
    });
    expect(resolveEnvironmentChoice("auto", rows)).toEqual({
      choice: "auto",
      preferredMissing: false,
    });
  });

  it("honours a still-launchable preferred environment", () => {
    expect(resolveEnvironmentChoice("e2", rows)).toEqual({
      choice: "e2",
      preferredMissing: false,
    });
  });

  it("falls back to auto and flags a bound-but-not-launchable preference", () => {
    const readonlyRows = joinEnvironmentsWithBindings(
      [makeEnvironment({ id: "e1", access: "read-only" })],
      [makeBinding("e1")],
    );
    expect(resolveEnvironmentChoice("e1", readonlyRows)).toEqual({
      choice: "auto",
      preferredMissing: true,
    });
  });

  it("falls back to auto quietly when the preferred environment has no binding", () => {
    expect(resolveEnvironmentChoice("e3", rows)).toEqual({
      choice: "auto",
      preferredMissing: false,
    });
  });
});

describe("describeIssuePromptPreferences", () => {
  it("joins the present parts, resolving environment ids to names", () => {
    const name = (id: string) => (id === "e1" ? "Mac" : undefined);
    expect(
      describeIssuePromptPreferences(
        {
          environment: "e1",
          workspace: "root",
          model_selection: { instance_id: "codex", model: "gpt-5" },
        },
        name,
      ),
    ).toBe("环境 Mac · 工作区 Current checkout · 模型 gpt-5");
    expect(describeIssuePromptPreferences({ environment: "auto" })).toBe(
      "环境 自动",
    );
    expect(
      describeIssuePromptPreferences({ workspace: "worktree" }),
    ).toBe("工作区 New worktree");
    expect(describeIssuePromptPreferences({})).toBe("");
  });
});
