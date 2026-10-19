import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import { MemoryRouter } from "react-router";
import { toast } from "sonner";

import ReviewsPage from "@/routes/reviews";
import ReviewProjectPage from "@/routes/reviews/$projectId";
import { renderWithRoute, withTestQueryClient } from "@/test/render";
import type { Environment } from "@/lib/api/threads";
import type {
  ReviewPullRequestRecord,
  ReviewSettings,
} from "@/lib/api/reviews";
import {
  useReviewProjects,
  useReviewRecords,
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
    useReviewSettings: vi.fn(),
    useReviewRecords: vi.fn(),
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

function baseSettings(overrides: Partial<ReviewSettings> = {}): ReviewSettings {
  return {
    project_id: "p1",
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

const prRecord: ReviewPullRequestRecord = {
  repo_full_name: "acme/alpha",
  pr_number: 12,
  pr_title: "修复登录",
  pr_url: "https://github.com/acme/alpha/pull/12",
  head_sha: "abcdef123456",
  code_status: "failure",
  visual_status: null,
  updated_at: "2026-10-20T00:00:00Z",
  rounds: [
    {
      round_number: 1,
      head_sha: "abcdef123456",
      created_at: "2026-10-20T00:00:00Z",
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
          findings_count: 3,
          summary: "发现 3 处问题",
        },
      ],
      visual_runs: [
        {
          id: "r2",
          kind: "visual",
          environment_id: "env-1",
          instance_id: "codex-1",
          model: "gpt-5",
          status: "failed",
          thread_id: "th-2",
          error_message: "截图超时",
        },
      ],
    },
  ],
};

/* ---------- mocks ---------- */

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

function renderDetail(initialEntry = "/reviews/p1") {
  return render(
    withTestQueryClient(
      <MemoryRouter initialEntries={[initialEntry]}>
        <ReviewProjectPage />
      </MemoryRouter>,
    ),
  );
}

/* ---------- 列表页 ---------- */

describe("ReviewsPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("空态提示关联仓库并链接到项目页", () => {
    vi.mocked(useReviewProjects).mockReturnValue({
      data: [],
      isLoading: false,
    } as unknown as ReturnType<typeof useReviewProjects>);

    renderWithRoute(<ReviewsPage />, {
      path: "/reviews",
      initialEntry: "/reviews",
    });

    expect(
      screen.getByText("先给项目关联 GitHub 仓库，再开启审查"),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "前往项目" })).toHaveAttribute(
      "href",
      "/projects",
    );
  });

  it("渲染项目行：开关、组合数、状态与操作链接；taken_by 只读提示", () => {
    vi.mocked(useReviewProjects).mockReturnValue({
      data: [
        {
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
        },
        {
          project_id: "p2",
          project_name: "beta",
          github_owner: "acme",
          github_repo: "beta",
          enabled: false,
          code_combination_count: 0,
          visual_combination_count: 0,
          taken_by: "alice",
          can_manage: false,
        },
      ],
      isLoading: false,
    } as unknown as ReturnType<typeof useReviewProjects>);

    renderWithRoute(<ReviewsPage />, {
      path: "/reviews",
      initialEntry: "/reviews",
    });

    expect(screen.getByText("alpha")).toBeInTheDocument();
    expect(screen.getByText("acme/alpha")).toBeInTheDocument();
    expect(screen.getByText("已开启")).toBeInTheDocument();
    expect(screen.getByText("未开启")).toBeInTheDocument();
    expect(screen.getByText("由 alice 开启")).toBeInTheDocument();
    expect(screen.getByText("代码 通过")).toBeInTheDocument();
    expect(screen.getByText("视觉 发现问题")).toBeInTheDocument();
    expect(
      screen.getAllByRole("link", { name: "设置" })[0],
    ).toHaveAttribute("href", "/reviews/p1");
    expect(
      screen.getAllByRole("link", { name: "审查记录" })[0],
    ).toHaveAttribute("href", "/reviews/p1?tab=records");
  });
});

/* ---------- 项目审查页：设置 ---------- */

describe("ReviewProjectPage 设置", () => {
  const updateSettings = vi.fn();

  beforeEach(() => {
    vi.clearAllMocks();
    updateSettings.mockResolvedValue({ data: {} });
    vi.mocked(useUpdateReviewSettings).mockReturnValue({
      mutateAsync: updateSettings,
      isPending: false,
    } as unknown as ReturnType<typeof useUpdateReviewSettings>);
    vi.mocked(useReviewRecords).mockReturnValue({
      data: [],
      isLoading: false,
    } as unknown as ReturnType<typeof useReviewRecords>);
    mockDetailDeps();
  });

  it("前置条件缺项 + enabled=false：开关不可开启但保存可用", async () => {
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
});

/* ---------- 项目审查页：审查记录 ---------- */

describe("ReviewProjectPage 审查记录", () => {
  const rerun = vi.fn();

  beforeEach(() => {
    vi.clearAllMocks();
    rerun.mockResolvedValue({
      data: { accepted: true, head_sha: "abcdef123456", round_number: 2 },
    });
    vi.mocked(useRerunReview).mockReturnValue({
      mutateAsync: rerun,
      isPending: false,
    } as unknown as ReturnType<typeof useRerunReview>);
    vi.mocked(useUpdateReviewSettings).mockReturnValue({
      mutateAsync: vi.fn(),
      isPending: false,
    } as unknown as ReturnType<typeof useUpdateReviewSettings>);
    mockDetailDeps();
    mockSettings(baseSettings());
    vi.mocked(useReviewRecords).mockReturnValue({
      data: [prRecord],
      isLoading: false,
    } as unknown as ReturnType<typeof useReviewRecords>);
  });

  it("渲染 PR 行与轮次/运行；线程链接正确", async () => {
    const user = userEvent.setup();
    renderDetail("/reviews/p1?tab=records");

    expect(await screen.findByText("#12 修复登录")).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "#12 修复登录" }),
    ).toHaveAttribute("href", "https://github.com/acme/alpha/pull/12");
    expect(screen.getByText("代码 发现问题")).toBeInTheDocument();

    await user.click(
      screen.getByRole("button", { name: "展开 PR #12" }),
    );

    expect(screen.getByText(/第 1 轮/)).toBeInTheDocument();
    expect(
      screen.getByText("claude-1 · claude-5 · reasoningEffort=high"),
    ).toBeInTheDocument();
    expect(screen.getByText("已完成")).toBeInTheDocument();
    expect(screen.getByText("3 条发现")).toBeInTheDocument();
    expect(screen.getByText("截图超时")).toBeInTheDocument();
    const threadLinks = screen.getAllByRole("link", { name: "线程" });
    expect(threadLinks[0]).toHaveAttribute("href", "/threads/env-1/th-1");
    expect(threadLinks[1]).toHaveAttribute("href", "/threads/env-1/th-2");
  });

  it("点「重新审查」调用 rerun API 并 toast", async () => {
    const user = userEvent.setup();
    renderDetail("/reviews/p1?tab=records");

    await user.click(
      screen.getByRole("button", { name: "重新审查 PR #12" }),
    );

    await waitFor(() => expect(rerun).toHaveBeenCalledWith(12));
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith(
        expect.stringContaining("第 2 轮"),
      ),
    );
  });
});
