import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router";
import { toast } from "sonner";

import ReviewsPage from "@/routes/reviews";
import ReviewConfigPage from "@/routes/reviews/config";
import ReviewProjectPage from "@/routes/reviews/$projectId";
import { renderWithRoute, withTestQueryClient } from "@/test/render";
import type { Environment } from "@/lib/api/threads";
import type {
  ReviewProjectSummary,
  ReviewPullRequestActivity,
  ReviewPullRequestActivityResult,
  ReviewPullRequestRecord,
  ReviewSettings,
} from "@/lib/api/reviews";
import {
  useReviewProjects,
  useReviewPullRequestDetail,
  useReviewPullRequests,
  useReviewSettings,
  useRerunReview,
  useUpdateReviewSettings,
} from "@/lib/hooks/use-reviews";
import { useProject } from "@/lib/hooks/use-projects";
import {
  useComposerOptions,
  useEnvironments,
} from "@/lib/hooks/use-threads";
import { useProjectT3Bindings } from "@/lib/hooks/use-t3-bindings";

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

vi.mock("@/lib/hooks/use-reviews", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("@/lib/hooks/use-reviews")>();
  return {
    ...actual,
    useReviewProjects: vi.fn(),
    useReviewPullRequests: vi.fn(),
    useReviewPullRequestDetail: vi.fn(),
    useReviewSettings: vi.fn(),
    useUpdateReviewSettings: vi.fn(),
    useRerunReview: vi.fn(),
  };
});

vi.mock("@/lib/hooks/use-projects", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("@/lib/hooks/use-projects")>();
  return { ...actual, useProject: vi.fn() };
});

vi.mock("@/lib/hooks/use-threads", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("@/lib/hooks/use-threads")>();
  return {
    ...actual,
    useEnvironments: vi.fn(),
    useComposerOptions: vi.fn(),
  };
});

vi.mock("@/lib/hooks/use-t3-bindings", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("@/lib/hooks/use-t3-bindings")>();
  return { ...actual, useProjectT3Bindings: vi.fn() };
});

/* ---------- fixtures ---------- */

const NOW = new Date("2026-10-22T10:00:00Z").getTime();

const environments: Environment[] = [
  {
    id: "env-1",
    name: "开发机",
    address: "",
    status: "ok",
    scopes: [],
    access: "operate",
    updated_at: "2026-10-01T00:00:00Z",
  },
];

function projectSummary(
  overrides: Partial<ReviewProjectSummary> = {},
): ReviewProjectSummary {
  return {
    project_id: "p1",
    project_name: "alpha",
    github_owner: "acme",
    github_repo: "alpha",
    enabled: true,
    code_combination_count: 2,
    visual_combination_count: 1,
    code_status: "success",
    visual_status: "failure",
    last_review_at: "2026-10-20T00:00:00Z",
    can_manage: true,
    ...overrides,
  };
}

function activityItem(
  overrides: Partial<ReviewPullRequestActivity> = {},
): ReviewPullRequestActivity {
  return {
    project_id: "p1",
    project_name: "alpha",
    repo_full_name: "acme/alpha",
    pr_number: 12,
    pr_title: "修复登录",
    pr_url: "https://github.com/acme/alpha/pull/12",
    group: "in_progress",
    stage: "reviewing",
    debounce_fire_at: null,
    round_number: 2,
    head_sha: "abcdef123456",
    updated_at: "2026-10-22T09:00:00Z",
    started_at: "2026-10-22T09:58:30Z",
    stages: [],
    findings: { P0: 0, P1: 0, P2: 0, blocker: 0, worth_fixing: 0 },
    issues: [],
    can_rerun: true,
    ...overrides,
  };
}

function activityResult(
  items: ReviewPullRequestActivity[],
  total?: number,
): ReviewPullRequestActivityResult {
  return {
    items,
    counts: {
      in_progress: items.filter((i) => i.group === "in_progress").length,
      needs_action: items.filter((i) => i.group === "needs_action").length,
      recently_passed: items.filter((i) => i.group === "recently_passed")
        .length,
    },
    total: total ?? items.length,
  };
}

function baseSettings(overrides: Partial<ReviewSettings> = {}): ReviewSettings {
  return {
    project_id: "p1",
    repo_full_name: "acme/alpha",
    enabled: true,
    trigger_opened: true,
    trigger_synchronize: true,
    trigger_ready_for_review: false,
    trigger_reopened: false,
    code_combinations: [
      {
        environment_id: "env-1",
        instance_id: "claude-1",
        model: "claude-5",
        options: [{ id: "reasoningEffort", value: "high" }],
        environment_name: "开发机",
        t3_project_id: "t3-1",
      },
    ],
    visual_combinations: [],
    prerequisites: [
      { code: "github_app", label: "GitHub App", missing: false },
      { code: "environment_binding", label: "T3 环境绑定", missing: false },
    ],
    can_manage: true,
    ...overrides,
  };
}

const composerOptions = {
  config: {
    providers: [
      {
        instanceId: "claude-1",
        displayName: "Claude",
        models: [
          {
            slug: "claude-5",
            name: "Claude 5",
            isDefault: true,
            capabilities: {
              optionDescriptors: [
                {
                  type: "select",
                  id: "reasoningEffort",
                  label: "推理强度",
                  options: [
                    { id: "high", label: "高", isDefault: true },
                    { id: "low", label: "低" },
                  ],
                },
              ],
            },
          },
        ],
      },
    ],
  },
  projects: [],
  settings: {},
};

const prDetail: ReviewPullRequestRecord = {
  repo_full_name: "acme/alpha",
  pr_number: 12,
  pr_title: "修复登录",
  pr_url: "https://github.com/acme/alpha/pull/12",
  head_sha: "abcdef123456",
  code_status: "failure",
  visual_status: null,
  updated_at: "2026-10-20T00:00:00Z",
  can_rerun: true,
  rounds: [
    {
      round_number: 2,
      head_sha: "abcdef123456",
      trigger_action: "synchronize",
      created_at: "2026-10-22T09:00:00Z",
      code_runs: [
        {
          id: "r1",
          kind: "code",
          environment_id: "env-1",
          environment_name: "开发机",
          instance_id: "claude-1",
          model: "claude-5",
          options: [{ id: "reasoningEffort", value: "high" }],
          status: "completed",
          thread_id: "th-1",
          findings_count: 1,
          summary: "发现 1 处问题",
          findings: [
            {
              level: "P1",
              file: "src/login.ts",
              line: 42,
              body: "空指针风险",
              fix: "加空值判断",
            },
          ],
        },
      ],
      visual_runs: [
        {
          id: "r2",
          kind: "visual",
          environment_id: "env-1",
          instance_id: "codex-1",
          model: "gpt-5",
          status: "completed",
          thread_id: "th-2",
          findings: [
            {
              level: "blocker",
              screenshot: "首页.png",
              dimension: "布局",
              body: "按钮错位",
              fix: "调整 flex",
            },
          ],
          unmeasurable: [{ screenshot: "设置页.png", reason: "弹窗遮挡" }],
        },
      ],
    },
    {
      round_number: 1,
      head_sha: "bbbbbb123456",
      trigger_action: "opened",
      created_at: "2026-10-21T09:00:00Z",
      code_runs: [
        {
          id: "r0",
          kind: "code",
          environment_id: "env-1",
          instance_id: "claude-1",
          model: "claude-5",
          status: "completed",
          thread_id: "th-0",
        },
      ],
      visual_runs: [],
    },
  ],
};

/* ---------- mocks ---------- */

function mockActivity(
  result: ReviewPullRequestActivityResult | undefined,
  isLoading = false,
) {
  vi.mocked(useReviewPullRequests).mockReturnValue({
    data: result,
    isLoading,
  } as unknown as ReturnType<typeof useReviewPullRequests>);
}

function mockProjects(projects: ReviewProjectSummary[] | undefined) {
  vi.mocked(useReviewProjects).mockReturnValue({
    data: projects,
    isLoading: false,
  } as unknown as ReturnType<typeof useReviewProjects>);
}

function mockDetailDeps() {
  vi.mocked(useProject).mockReturnValue({
    data: {
      id: "p1",
      name: "alpha",
      github_owner: "acme",
      github_repo: "alpha",
    },
  } as unknown as ReturnType<typeof useProject>);
  vi.mocked(useEnvironments).mockReturnValue({
    data: environments,
  } as unknown as ReturnType<typeof useEnvironments>);
  vi.mocked(useProjectT3Bindings).mockReturnValue({
    data: [
      {
        project_id: "p1",
        environment_id: "env-1",
        t3_project_id: "t3-1",
      },
    ],
    isLoading: false,
    isError: false,
  } as unknown as ReturnType<typeof useProjectT3Bindings>);
  vi.mocked(useComposerOptions).mockReturnValue({
    data: composerOptions,
    isLoading: false,
    isError: false,
  } as unknown as ReturnType<typeof useComposerOptions>);
}

function mockSettings(settings: ReviewSettings | undefined) {
  vi.mocked(useReviewSettings).mockReturnValue({
    data: settings,
    isLoading: false,
  } as unknown as ReturnType<typeof useReviewSettings>);
}

function renderReviewsPage(initialEntry = "/reviews") {
  return renderWithRoute(<ReviewsPage />, {
    path: "/reviews",
    initialEntry,
  });
}

function renderDetail(initialEntry = "/reviews/p1") {
  return render(
    withTestQueryClient(
      <MemoryRouter initialEntries={[initialEntry]}>
        <ReviewProjectPage />
      </MemoryRouter>,
    ),
  );
}

/* ---------- /reviews PR 动态 ---------- */

describe("ReviewsPage PR 动态", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useRerunReview).mockReturnValue({
      mutateAsync: vi.fn(),
      isPending: false,
    } as unknown as ReturnType<typeof useRerunReview>);
    vi.mocked(useReviewPullRequestDetail).mockReturnValue({
      data: undefined,
      isLoading: true,
    } as unknown as ReturnType<typeof useReviewPullRequestDetail>);
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("渲染三组：进行中进度文案、需要处理发现与原因、最近通过默认折叠", async () => {
    const user = userEvent.setup();
    mockProjects([
      projectSummary(),
      projectSummary({ project_id: "p2", project_name: "beta" }),
    ]);
    mockActivity(
      activityResult([
        activityItem({
          stages: [
            { kind: "code", total: 2, completed: 1, status: "running" },
            { kind: "visual", total: 2, completed: 0, status: "pending" },
          ],
        }),
        activityItem({
          pr_number: 34,
          pr_title: "改坏样式",
          group: "needs_action",
          stage: null,
          findings: { P0: 0, P1: 1, P2: 2, blocker: 1, worth_fixing: 0 },
          issues: [
            {
              kind: "code",
              combination: "claude-5 · claude-1",
              reason: "派发失败",
              message: "环境无响应",
            },
          ],
        }),
        activityItem({
          pr_number: 56,
          pr_title: "通过的改动",
          group: "recently_passed",
          stage: null,
        }),
      ]),
    );

    renderReviewsPage();

    expect(screen.getByText("进行中")).toBeInTheDocument();
    expect(screen.getByText("需要处理")).toBeInTheDocument();
    expect(
      screen.getByText("代码审查 1/2 完成 → 视觉审查 等待代码审查"),
    ).toBeInTheDocument();
    expect(screen.getByText("审查中")).toBeInTheDocument();

    expect(screen.getByText("P1 ×1，P2 ×2，视觉问题 ×1")).toBeInTheDocument();
    expect(
      screen.getByText("claude-5 · claude-1 · 派发失败：环境无响应"),
    ).toBeInTheDocument();

    // 最近通过默认折叠：标题不出现，展开后可见。
    expect(screen.queryByText("通过的改动")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "最近通过" }));
    expect(screen.getByText("通过的改动")).toBeInTheDocument();
  });

  it("防抖行显示阶段徽标与倒计时，审查中行显示已用时长（固定时间）", () => {
    vi.useFakeTimers();
    vi.setSystemTime(NOW);
    mockProjects([projectSummary()]);
    mockActivity(
      activityResult([
        activityItem({
          pr_number: 12,
          pr_title: "等待防抖",
          stage: "debouncing",
          debounce_fire_at: "2026-10-22T10:00:42Z",
        }),
        activityItem({
          pr_number: 34,
          pr_title: "正在审查",
          stage: "reviewing",
          started_at: "2026-10-22T09:58:30Z",
        }),
      ]),
    );

    renderReviewsPage();

    expect(screen.getByText("防抖中")).toBeInTheDocument();
    expect(screen.getByText("约 42 秒后开始")).toBeInTheDocument();
    expect(screen.getByText("已用 1 分 30 秒")).toBeInTheDocument();
  });

  it("没有项目开启：引导去配置页", () => {
    mockProjects([]);
    mockActivity(activityResult([]));

    renderReviewsPage();

    expect(screen.getByText("还没有项目开启审查")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "前往配置" })).toHaveAttribute(
      "href",
      "/reviews/config",
    );
  });

  it("已开启但无 PR：提示推送或打开 PR", () => {
    mockProjects([projectSummary()]);
    mockActivity(activityResult([]));

    renderReviewsPage();

    expect(
      screen.getByText("推送或打开 PR 后会出现在这里"),
    ).toBeInTheDocument();
  });

  it("右上角配置链接显示已开启项目数", () => {
    mockProjects([
      projectSummary(),
      projectSummary({ project_id: "p2", project_name: "beta" }),
      projectSummary({
        project_id: "p3",
        project_name: "gamma",
        enabled: false,
      }),
    ]);
    mockActivity(activityResult([]));

    renderReviewsPage();

    expect(
      screen.getByRole("link", { name: /配置（2 个项目已开启）/ }),
    ).toHaveAttribute("href", "/reviews/config");
  });

  it("项目筛选写入 URL 并传给查询", async () => {
    const user = userEvent.setup();
    mockProjects([
      projectSummary(),
      projectSummary({ project_id: "p2", project_name: "beta" }),
    ]);
    mockActivity(activityResult([]));

    renderReviewsPage();

    await user.click(screen.getByRole("combobox"));
    await user.click(await screen.findByRole("option", { name: "alpha" }));

    expect(vi.mocked(useReviewPullRequests)).toHaveBeenLastCalledWith(
      "p1",
      50,
      0,
    );
  });

  it("超过一页时按 offset 翻页，limit 固定不超过服务端上限", async () => {
    const user = userEvent.setup();
    mockProjects([projectSummary()]);
    mockActivity(activityResult([activityItem()], 120));

    renderReviewsPage();

    expect(vi.mocked(useReviewPullRequests)).toHaveBeenLastCalledWith(
      undefined,
      50,
      0,
    );
    expect(screen.getByText(/第 1–1 条 \/ 共 120 条/)).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "下一页" }));
    expect(vi.mocked(useReviewPullRequests)).toHaveBeenLastCalledWith(
      undefined,
      50,
      50,
    );
    expect(vi.mocked(useReviewPullRequests)).not.toHaveBeenLastCalledWith(
      undefined,
      250,
    );
  });

  it("代码完成、视觉段 pending 显示「等待视觉审查衔接」", () => {
    mockProjects([projectSummary()]);
    mockActivity(
      activityResult([
        activityItem({
          stages: [
            { kind: "code", total: 1, completed: 1, status: "completed" },
            { kind: "visual", total: 1, completed: 0, status: "pending" },
          ],
        }),
      ]),
    );

    renderReviewsPage();

    expect(
      screen.getByText(
        "代码审查 已完成 → 视觉审查 等待视觉审查衔接",
      ),
    ).toBeInTheDocument();
  });

  it("点行打开抽屉并渲染详情；进行中项详情 live 轮询", async () => {
    const user = userEvent.setup();
    mockProjects([projectSummary()]);
    mockActivity(activityResult([activityItem()]));
    vi.mocked(useReviewPullRequestDetail).mockReturnValue({
      data: prDetail,
      isLoading: false,
    } as unknown as ReturnType<typeof useReviewPullRequestDetail>);

    renderReviewsPage();

    await user.click(screen.getByRole("button", { name: /修复登录/ }));

    const sheet = await screen.findByRole("dialog", {
      name: /#12 修复登录/,
    });
    expect(
      within(sheet).getByRole("link", { name: /在 GitHub 打开/ }),
    ).toHaveAttribute("href", "https://github.com/acme/alpha/pull/12");

    // 最新一轮展开：轮次、SHA、触发方式、run、发现与无法判断。
    expect(within(sheet).getByText(/第 2 轮/)).toBeInTheDocument();
    expect(within(sheet).getByText(/推送/)).toBeInTheDocument();
    expect(
      within(sheet).getByText("claude-1 · claude-5 · reasoningEffort=high"),
    ).toBeInTheDocument();
    expect(within(sheet).getByText("P1")).toBeInTheDocument();
    expect(within(sheet).getByText("src/login.ts:42")).toBeInTheDocument();
    expect(within(sheet).getByText("空指针风险")).toBeInTheDocument();
    expect(
      within(sheet).getByText("建议：加空值判断"),
    ).toBeInTheDocument();
    expect(within(sheet).getByText("blocker")).toBeInTheDocument();
    expect(within(sheet).getByText("首页.png · 布局")).toBeInTheDocument();
    expect(within(sheet).getByText("无法判断")).toBeInTheDocument();
    expect(within(sheet).getByText("设置页.png")).toBeInTheDocument();
    expect(within(sheet).getByText("弹窗遮挡")).toBeInTheDocument();
    const threadLinks = within(sheet).getAllByRole("link", { name: "线程" });
    expect(threadLinks[0]).toHaveAttribute("href", "/threads/env-1/th-1");
    expect(threadLinks[1]).toHaveAttribute("href", "/threads/env-1/th-2");

    // 历史轮折叠成一行灰字，点开展开（该轮的 run 行此前不可见）。
    const history = within(sheet).getByRole("button", {
      name: /第 1 轮 · bbbbbb1 · 历史轮次/,
    });
    expect(
      within(sheet).queryByText("claude-1 · claude-5", { exact: true }),
    ).not.toBeInTheDocument();
    await user.click(history);
    expect(
      within(sheet).getByText("claude-1 · claude-5", { exact: true }),
    ).toBeInTheDocument();
    expect(
      within(sheet).getByText(/第 1 轮 · .*打开/),
    ).toBeInTheDocument();

    // 进行中项打开详情时 live=true（10s 轮询）。
    expect(
      vi.mocked(useReviewPullRequestDetail),
    ).toHaveBeenLastCalledWith("p1", 12);
  });

  it("需要处理项打开抽屉时详情同样轮询（不依赖列表分组快照）", async () => {
    const user = userEvent.setup();
    mockProjects([projectSummary()]);
    mockActivity(
      activityResult([
        activityItem({
          pr_number: 34,
          pr_title: "改坏样式",
          group: "needs_action",
          stage: null,
          issues: [
            {
              kind: "code",
              combination: "",
              reason: "超时",
              message: "超过 30 分钟",
            },
          ],
        }),
      ]),
    );
    vi.mocked(useReviewPullRequestDetail).mockReturnValue({
      data: { ...prDetail, pr_number: 34, pr_title: "改坏样式" },
      isLoading: false,
    } as unknown as ReturnType<typeof useReviewPullRequestDetail>);

    renderReviewsPage();

    await user.click(screen.getByRole("button", { name: /改坏样式/ }));
    await screen.findByRole("dialog");

    expect(
      vi.mocked(useReviewPullRequestDetail),
    ).toHaveBeenLastCalledWith("p1", 34);
  });

  it("点「重新审查」调用 rerun 并 toast", async () => {
    const user = userEvent.setup();
    const rerun = vi.fn().mockResolvedValue({
      data: { accepted: true, head_sha: "x", round_number: 3 },
    });
    vi.mocked(useRerunReview).mockReturnValue({
      mutateAsync: rerun,
      isPending: false,
    } as unknown as ReturnType<typeof useRerunReview>);
    mockProjects([projectSummary()]);
    mockActivity(
      activityResult([
        activityItem({
          group: "needs_action",
          stage: null,
          issues: [
            { kind: "code", reason: "超时", message: "超过 30 分钟" },
          ],
        }),
      ]),
    );

    renderReviewsPage();

    await user.click(
      screen.getByRole("button", { name: "重新审查 PR #12" }),
    );

    await waitFor(() =>
      expect(rerun).toHaveBeenCalledWith({
        prNumber: 12,
        repoFullName: "acme/alpha",
      }),
    );
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith(
        expect.stringContaining("第 3 轮"),
      ),
    );
  });
});

/* ---------- /reviews/config 配置页 ---------- */

describe("ReviewConfigPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("已开启在上，未开启折叠为「其余 N 个项目未开启 · 展开」", async () => {
    const user = userEvent.setup();
    mockProjects([
      projectSummary(),
      projectSummary({
        project_id: "p2",
        project_name: "beta",
        github_repo: "beta",
        enabled: false,
        taken_by: "alice",
        can_manage: false,
      }),
      projectSummary({
        project_id: "p3",
        project_name: "gamma",
        github_repo: "gamma",
        enabled: false,
      }),
    ]);

    renderWithRoute(<ReviewConfigPage />, {
      path: "/reviews/config",
      initialEntry: "/reviews/config",
    });

    // 已开启行直接可见；未开启折叠不可见。
    expect(screen.getByText("alpha")).toBeInTheDocument();
    expect(screen.getByText("acme/alpha")).toBeInTheDocument();
    expect(screen.queryByText("beta")).not.toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "设置" }),
    ).toHaveAttribute("href", "/reviews/p1");

    await user.click(
      screen.getByRole("button", { name: /其余 2 个项目未开启 · 展开/ }),
    );

    expect(screen.getByText("beta")).toBeInTheDocument();
    expect(screen.getByText("gamma")).toBeInTheDocument();
    expect(screen.getByText("由 alice 开启")).toBeInTheDocument();
  });

  it("空态提示关联仓库并链接到项目页", () => {
    mockProjects([]);

    renderWithRoute(<ReviewConfigPage />, {
      path: "/reviews/config",
      initialEntry: "/reviews/config",
    });

    expect(
      screen.getByText("先给项目关联 GitHub 仓库，再开启审查"),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "前往项目" })).toHaveAttribute(
      "href",
      "/projects",
    );
  });

  it("顶部返回审查首页", () => {
    mockProjects([projectSummary()]);

    renderWithRoute(<ReviewConfigPage />, {
      path: "/reviews/config",
      initialEntry: "/reviews/config",
    });

    expect(screen.getByRole("link", { name: "返回" })).toHaveAttribute(
      "href",
      "/reviews",
    );
  });
});

/* ---------- /reviews/:projectId 设置页 ---------- */

describe("ReviewProjectPage 设置", () => {
  const updateSettings = vi.fn();

  beforeEach(() => {
    vi.clearAllMocks();
    updateSettings.mockResolvedValue({ data: {} });
    vi.mocked(useUpdateReviewSettings).mockReturnValue({
      mutateAsync: updateSettings,
      isPending: false,
    } as unknown as ReturnType<typeof useUpdateReviewSettings>);
    mockDetailDeps();
  });

  it("设置页展示实际审查的仓库名", () => {
    mockSettings(baseSettings({ repo_full_name: "gitbobobo/musiver" }));

    renderDetail();

    expect(screen.getByText(/审查仓库：/)).toBeInTheDocument();
    expect(screen.getByText("gitbobobo/musiver")).toBeInTheDocument();
  });

  it("前置条件全部满足时收成一行", () => {
    mockSettings(baseSettings());

    renderDetail();

    expect(screen.getByText("✓ 前置条件已满足")).toBeInTheDocument();
    // 逐条前置项不展开。
    expect(screen.queryByText("GitHub App")).not.toBeInTheDocument();
    expect(screen.queryByText("T3 环境绑定")).not.toBeInTheDocument();
  });

  it("前置条件缺项 + enabled=false：展开标红，开关不可开启但保存可用", async () => {
    const user = userEvent.setup();
    mockSettings(
      baseSettings({
        enabled: false,
        prerequisites: [
          {
            code: "github_token",
            label: "GitHub Token",
            missing: true,
            detail: "未配置",
          },
        ],
      }),
    );

    renderDetail();

    expect(screen.getByText(/GitHub Token/)).toBeInTheDocument();
    expect(screen.getByText(/未配置/)).toBeInTheDocument();
    expect(
      screen.getByText("前置条件未全部满足，无法开启审查。"),
    ).toBeInTheDocument();
    // Base UI Switch 以 aria-disabled 表达禁用（非 disabled 属性）。
    expect(screen.getByRole("switch")).toHaveAttribute(
      "aria-disabled",
      "true",
    );

    // enabled=false 时保存不被前置条件拦截（关闭/调组合不依赖前置）。
    const save = screen.getByRole("button", { name: "保存" });
    expect(save).not.toBeDisabled();
    await user.click(save);
    await waitFor(() => expect(updateSettings).toHaveBeenCalledTimes(1));
    expect(updateSettings.mock.calls[0][0]).toMatchObject({
      enabled: false,
    });
  });

  it("前置条件缺项 + enabled=true：保存禁用并说明原因", () => {
    mockSettings(
      baseSettings({
        enabled: true,
        prerequisites: [
          {
            code: "github_token",
            label: "GitHub Token",
            missing: true,
            detail: "未配置",
          },
        ],
      }),
    );

    renderDetail();

    expect(screen.getByRole("button", { name: "保存" })).toBeDisabled();
    expect(
      screen.getAllByText("前置条件未全部满足，无法开启审查。").length,
    ).toBeGreaterThan(0);
  });

  it("组合重复：保存时提示且不调用 updateSettings", async () => {
    const user = userEvent.setup();
    const combo = {
      environment_id: "env-1",
      instance_id: "claude-1",
      model: "claude-5",
      options: [{ id: "reasoningEffort", value: "high" }],
    };
    mockSettings(
      baseSettings({ code_combinations: [combo, { ...combo }] }),
    );

    renderDetail();

    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(
        expect.stringContaining("重复"),
      ),
    );
    expect(updateSettings).not.toHaveBeenCalled();
  });

  it("保存调用 updateSettings：字段齐全且裁掉仅响应字段", async () => {
    const user = userEvent.setup();
    mockSettings(baseSettings());

    renderDetail();

    // 组合行渲染出模型描述符 chip（证明模型解析成功）。
    expect(
      screen.getByRole("button", { name: "推理强度" }),
    ).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() => expect(updateSettings).toHaveBeenCalledTimes(1));
    const input = updateSettings.mock.calls[0][0];
    expect(input).toMatchObject({
      enabled: true,
      trigger_opened: true,
      trigger_synchronize: true,
      trigger_ready_for_review: false,
      trigger_reopened: false,
    });
    expect(input.code_combinations).toEqual([
      {
        environment_id: "env-1",
        instance_id: "claude-1",
        model: "claude-5",
        options: [{ id: "reasoningEffort", value: "high" }],
      },
    ]);
    expect(input.code_combinations[0]).not.toHaveProperty("environment_name");
    expect(input.code_combinations[0]).not.toHaveProperty("t3_project_id");
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith("审查设置已保存"),
    );
  });

  it("不再有「审查记录」tab", () => {
    mockSettings(baseSettings());

    renderDetail();

    expect(
      screen.queryByRole("tab", { name: "审查记录" }),
    ).not.toBeInTheDocument();
    expect(screen.queryByText("审查记录")).not.toBeInTheDocument();
  });
});
