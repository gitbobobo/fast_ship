import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type {
  ComposerOptions,
  Environment,
  ThreadSummary,
  WorkspaceOptions,
} from "@/lib/api/threads";
import type { ProjectT3Binding } from "@/lib/api/t3-bindings";
import { ApiRequestError } from "@/lib/api/client";
import { LaunchIssueThreadDialog } from "@/components/issues/launch-issue-thread-dialog";
import { makeEnvironment } from "@/test/threads";
import { withTestQueryClient } from "@/test/render";

const {
  promptsRef,
  promptSettingsRef,
  environmentsRef,
  threadsRef,
  bindingsRef,
  composerRef,
  workspaceRef,
  createMock,
  updatePromptsMock,
  navigateMock,
  copyMock,
  toastSuccessMock,
  toastErrorMock,
} = vi.hoisted(() => ({
  promptsRef: { current: [] as IssuePrompt[] },
  promptSettingsRef: { current: undefined as IssuePromptSettings | undefined },
  environmentsRef: { current: [] as Environment[] },
  threadsRef: { current: { items: [] as ThreadSummary[] } },
  bindingsRef: { current: [] as ProjectT3Binding[] },
  composerRef: { current: undefined as ComposerOptions | undefined },
  workspaceRef: { current: undefined as WorkspaceOptions | undefined },
  createMock: vi.fn(),
  updatePromptsMock: vi.fn(),
  navigateMock: vi.fn(),
  copyMock: vi.fn(),
  toastSuccessMock: vi.fn(),
  toastErrorMock: vi.fn(),
}));

vi.mock("react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("react-router")>()),
  useNavigate: () => navigateMock,
}));

vi.mock("sonner", () => ({
  toast: { success: toastSuccessMock, error: toastErrorMock },
}));

vi.mock("@/lib/copy", () => ({
  copyWithToast: (...args: unknown[]) => copyMock(...args),
}));

// useCreateThread / useUpdateIssuePrompts 用真实 useMutation，让
// isPending/isError/error 与组件渲染节奏一致（错误展示、防重复点击依赖它）。
vi.mock("@/lib/hooks/use-issue-prompt", async () => {
  const { useMutation } = await import("@tanstack/react-query");
  return {
    useIssuePromptList: () => promptsRef.current,
    useIssuePrompts: () => ({ data: promptSettingsRef.current }),
    useUpdateIssuePrompts: () =>
      useMutation({ mutationFn: (p: IssuePrompt[]) => updatePromptsMock(p) }),
  };
});

vi.mock("@/lib/hooks/use-threads", async () => {
  const { useMutation } = await import("@tanstack/react-query");
  return {
    useEnvironments: () => ({
      data: environmentsRef.current,
      isLoading: false,
      isError: false,
      refetch: vi.fn(),
    }),
    useThreads: () => ({ data: threadsRef.current }),
    useComposerOptions: () => ({
      data: composerRef.current,
      isLoading: false,
      isError: false,
    }),
    useWorkspaceOptions: () => ({ data: workspaceRef.current }),
    useCreateThread: () =>
      useMutation({ mutationFn: (input: unknown) => createMock(input) }),
  };
});

vi.mock("@/lib/hooks/use-t3-bindings", () => ({
  useProjectT3Bindings: () => ({ data: bindingsRef.current }),
}));

const ISSUE = { id: "issue-1", reference: "INT-1", title: "高优任务" };

function makeBinding(
  environmentId: string,
  t3ProjectId = `t3-${environmentId}`,
): ProjectT3Binding {
  return {
    project_id: "proj-1",
    environment_id: environmentId,
    environment_name: environmentId,
    environment_status: "ok",
    environment_access: "operate",
    t3_project_id: t3ProjectId,
    t3_project_title: "Alpha",
    workspace_root: "/w/alpha",
  };
}

const COMPOSER: ComposerOptions = {
  projects: [
    {
      id: "t3-e1",
      title: "Alpha",
      workspace_root: "/w/alpha",
      default_model_selection: { instance_id: "codex", model: "gpt-5" },
    },
    {
      id: "t3-e2",
      title: "Beta",
      workspace_root: "/w/beta",
      default_model_selection: { instance_id: "codex", model: "gpt-5" },
    },
  ],
  config: {
    providers: [
      {
        instanceId: "codex",
        displayName: "Codex",
        enabled: true,
        installed: true,
        models: [
          { slug: "gpt-5", name: "GPT-5", isDefault: true, capabilities: {} },
        ],
      },
    ],
  },
  settings: {},
};

const WORKSPACE: WorkspaceOptions = {
  project_id: "t3-e1",
  cwd: "/w/alpha",
  is_repo: true,
  ref_name: "main",
  refs: [],
};

function renderDialog(reason?: string) {
  const onOpenChange = vi.fn();
  render(
    withTestQueryClient(
      <MemoryRouter>
        <LaunchIssueThreadDialog
          open={true}
          onOpenChange={onOpenChange}
          projectId="proj-1"
          issue={ISSUE}
          reason={reason}
        />
      </MemoryRouter>,
    ),
  );
  return { onOpenChange };
}

const launchButton = () => screen.getByRole("button", { name: /启动/ });
const envChip = () => screen.getByRole("button", { name: "运行环境" });

describe("LaunchIssueThreadDialog", () => {
  beforeEach(() => {
    promptsRef.current = [
      { id: "default", name: "默认", content: "请处理此问题" },
    ];
    promptSettingsRef.current = { prompts: promptsRef.current };
    environmentsRef.current = [makeEnvironment({ id: "e1", name: "Mac" })];
    threadsRef.current = { items: [] };
    bindingsRef.current = [makeBinding("e1")];
    composerRef.current = COMPOSER;
    workspaceRef.current = WORKSPACE;
    createMock.mockReset();
    createMock.mockResolvedValue({ thread_id: "t-new", resumed: false });
    updatePromptsMock.mockReset();
    updatePromptsMock.mockResolvedValue({});
    navigateMock.mockReset();
    copyMock.mockReset();
    toastSuccessMock.mockReset();
    toastErrorMock.mockReset();
  });

  it("auto-selects the least busy launchable environment", async () => {
    environmentsRef.current = [
      makeEnvironment({ id: "e1", name: "Mac" }),
      makeEnvironment({ id: "e2", name: "Linux" }),
    ];
    bindingsRef.current = [makeBinding("e1"), makeBinding("e2", "t3-e2")];
    threadsRef.current = {
      items: [
        {
          id: "t1",
          environment_id: "e1",
          environment_name: "Mac",
          project_id: "t3-e1",
          project_name: "Alpha",
          title: "t",
          status: "running",
          settled: false,
          branch: "main",
          provider: "codex",
          model: "gpt-5",
          updated_at: "2026-10-01T00:00:00Z",
          pull_request_count: 0,
        },
      ],
    };
    renderDialog();

    expect(await screen.findByText("启动线程 · INT-1")).toBeInTheDocument();
    // e1 有 1 个 running，e2 空闲 → 自动落到 e2
    expect(envChip().textContent).toContain("自动 → Linux");
    expect(envChip().textContent).toContain("0 运行中");
  });

  it("prefers the template-preference environment over the auto pick", async () => {
    environmentsRef.current = [
      makeEnvironment({ id: "e1", name: "Mac" }),
      makeEnvironment({ id: "e2", name: "Linux" }),
    ];
    bindingsRef.current = [makeBinding("e1"), makeBinding("e2", "t3-e2")];
    promptsRef.current = [
      {
        id: "default",
        name: "默认",
        content: "请处理此问题",
        preferences: { environment: "e1", workspace: "root" },
      },
    ];
    promptSettingsRef.current = { prompts: promptsRef.current };

    renderDialog();

    expect(await screen.findByText("启动线程 · INT-1")).toBeInTheDocument();
    expect(envChip().textContent).toContain("Mac");
    expect(envChip().textContent).not.toContain("自动");
    expect(
      screen.getByRole("button", { name: "工作区" }).textContent,
    ).toContain("Current checkout");
  });

  it("falls back to auto with a note when the preferred environment is not launchable", async () => {
    environmentsRef.current = [
      makeEnvironment({ id: "e1", name: "Mac", access: "read-only" }),
      makeEnvironment({ id: "e2", name: "Linux" }),
    ];
    bindingsRef.current = [makeBinding("e1"), makeBinding("e2", "t3-e2")];
    promptsRef.current = [
      {
        id: "default",
        name: "默认",
        content: "请处理此问题",
        preferences: { environment: "e1" },
      },
    ];
    promptSettingsRef.current = { prompts: promptsRef.current };

    renderDialog();

    expect(await screen.findByText("启动线程 · INT-1")).toBeInTheDocument();
    expect(envChip().textContent).toContain("自动 → Linux");
    expect(
      screen.getByText("模板偏好的环境当前不可启动，已回落自动选择。"),
    ).toBeInTheDocument();
  });

  it("greys out unbound environments and offers the bind entry", async () => {
    environmentsRef.current = [
      makeEnvironment({ id: "e1", name: "Mac" }),
      makeEnvironment({ id: "e2", name: "Linux" }),
    ];
    const { onOpenChange } = renderDialog();

    fireEvent.click(envChip());
    const unbound = await screen.findByRole("menuitem", { name: /Linux/ });
    expect(unbound).toHaveAttribute("aria-disabled", "true");
    expect(unbound.textContent).toContain("未绑定");

    fireEvent.click(screen.getByRole("menuitem", { name: /去绑定/ }));
    expect(navigateMock).toHaveBeenCalledWith(
      "/projects?edit=proj-1&section=t3",
    );
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it("shows the pairing empty state when no environments exist", async () => {
    environmentsRef.current = [];
    bindingsRef.current = [];
    const { onOpenChange } = renderDialog();

    expect(await screen.findByText("还没有连接 T3 环境")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "去设置配对" }));
    expect(navigateMock).toHaveBeenCalledWith("/settings/environments");
    expect(onOpenChange).toHaveBeenCalledWith(false);
    // 空状态下复制仍可用
    expect(copyMock).not.toHaveBeenCalled();
  });

  it("keeps copy usable while disabling launch when nothing is bound", async () => {
    bindingsRef.current = [];
    renderDialog();

    expect(
      await screen.findByText("该项目还没有绑定 T3 项目"),
    ).toBeInTheDocument();
    expect(launchButton()).toBeDisabled();

    fireEvent.click(screen.getByRole("button", { name: "复制提示词" }));
    await waitFor(() => expect(copyMock).toHaveBeenCalledTimes(1));
    expect(copyMock.mock.calls[0][0]).toContain("问题ID：issue-1");
    expect(createMock).not.toHaveBeenCalled();
  });

  it("launches with worktree defaults and offers a view link in the toast", async () => {
    const { onOpenChange } = renderDialog("先做这个能解锁后续工作");

    fireEvent.click(await screen.findByRole("button", { name: /启动 ⌘↵/ }));

    await waitFor(() => expect(createMock).toHaveBeenCalledTimes(1));
    const input = createMock.mock.calls[0][0] as Record<string, unknown>;
    expect(input.project_id).toBe("t3-e1");
    expect(input.runtime_mode).toBe("full-access");
    // 新 worktree 不传 base_ref，由服务端取主检出分支
    expect(input.workspace).toEqual({ type: "worktree" });
    expect(input.model_selection).toEqual({
      instance_id: "codex",
      model: "gpt-5",
    });
    const message = input.message as { message_id: string; text: string };
    expect(message.text).toContain("> 项目ID：proj-1");
    expect(message.text).toContain("> 问题ID：issue-1");
    expect(message.text).toContain("> 推荐理由：先做这个能解锁后续工作");
    expect(input.thread_id).toEqual(expect.any(String));
    expect(input.command_id).toEqual(expect.any(String));

    expect(onOpenChange).toHaveBeenCalledWith(false);
    expect(toastSuccessMock).toHaveBeenCalledWith(
      "已在 Mac 启动线程",
      expect.objectContaining({ action: expect.any(Object) }),
    );
    // toast 上的「查看」跳到新线程
    const action = toastSuccessMock.mock.calls[0][1].action;
    action.onClick();
    expect(navigateMock).toHaveBeenCalledWith("/threads/e1/t-new");
  });

  it("retries a failed launch with the same id group and shows the error inline", async () => {
    createMock
      .mockRejectedValueOnce(new ApiRequestError(502, 50202, "环境不可达"))
      .mockResolvedValue({ thread_id: "t-new", resumed: false });
    renderDialog();

    fireEvent.click(await screen.findByRole("button", { name: /启动 ⌘↵/ }));
    expect(
      await screen.findByRole("alert"),
    ).toHaveTextContent("环境不可达");
    expect(toastSuccessMock).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: /启动 ⌘↵/ }));
    await waitFor(() => expect(createMock).toHaveBeenCalledTimes(2));

    const first = createMock.mock.calls[0][0] as {
      command_id: string;
      message: { message_id: string };
      thread_id: string;
    };
    const second = createMock.mock.calls[1][0] as typeof first;
    // createIdTracker：同一份内容重试复用同一组 ID，服务端不会重复建线程
    expect(second.thread_id).toBe(first.thread_id);
    expect(second.command_id).toBe(first.command_id);
    expect(second.message.message_id).toBe(first.message.message_id);

    await waitFor(() =>
      expect(toastSuccessMock).toHaveBeenCalledWith(
        "已在 Mac 启动线程",
        expect.anything(),
      ),
    );
  });

  it("persists the chosen defaults back to the template when opted in", async () => {
    renderDialog();
    fireEvent.click(
      await screen.findByRole("checkbox", { name: /保存为此模板的默认/ }),
    );
    fireEvent.click(screen.getByRole("button", { name: /启动 ⌘↵/ }));

    await waitFor(() => expect(updatePromptsMock).toHaveBeenCalledTimes(1));
    const saved = updatePromptsMock.mock.calls[0][0] as IssuePrompt[];
    expect(saved[0].preferences).toEqual({
      environment: "auto",
      workspace: "worktree",
      model_selection: { instance_id: "codex", model: "gpt-5" },
    });
  });
});
