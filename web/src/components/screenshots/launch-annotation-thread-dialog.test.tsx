import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, vi } from "vitest";
import { LaunchAnnotationThreadDialog } from "./launch-annotation-thread-dialog";
import { useThreadLaunch } from "@/lib/hooks/use-thread-launch";

vi.mock("@/lib/hooks/use-thread-launch", () => ({
  useThreadLaunch: vi.fn(),
}));

function mockLaunchState(overrides: Record<string, unknown> = {}) {
  return {
    environmentsQuery: { isLoading: false, isError: false },
    environments: [],
    rows: [],
    threads: [],
    autoRow: null,
    envChoice: "auto",
    setEnvPick: vi.fn(),
    selectedRow: null,
    environment: { id: "env-1", name: "本机" },
    binding: { t3_project_id: "t3-1" },
    workspace: "worktree",
    setWorkspace: vi.fn(),
    isRepo: true,
    refName: "main",
    composer: { isLoading: false, isError: false, data: { projects: [], config: "" } },
    workspaceOptions: { data: { is_repo: true, ref_name: "main" } },
    providers: [],
    resolved: { model: null },
    setPick: vi.fn(),
    create: { isError: false, isPending: false, error: null },
    launchError: null,
    setLaunchError: vi.fn(),
    launching: false,
    hasBinding: true,
    bindingStale: false,
    canLaunch: true,
    bindSettingsTo: "",
    goBind: vi.fn(),
    goPairSettings: vi.fn(),
    launch: vi.fn(async () => null),
    launchErrorText: () => "",
    ...overrides,
  };
}

function renderDialog() {
  return render(
    <LaunchAnnotationThreadDialog
      open
      onOpenChange={() => {}}
      projectId="proj-1"
      promptText="提示词"
      openCount={3}
    />,
  );
}

// m3 回归：⌘/Ctrl+Enter 与按钮共用状态门禁——确认步骤未过或不可启动时，
// 快捷键不得绕过二次确认直接 launch。
describe("LaunchAnnotationThreadDialog keyboard shortcut", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  // 快捷键处理挂在弹框内容容器上；键盘事件从标题冒泡过去等价于容器内聚焦
  function fireShortcut() {
    fireEvent.keyDown(screen.getByRole("heading", { name: "启动线程处理标注意见" }), {
      ctrlKey: true,
      key: "Enter",
    });
  }

  it("does not launch via the shortcut while still on the confirm step", async () => {
    const launch = vi.fn(async () => null);
    vi.mocked(useThreadLaunch).mockReturnValue(
      mockLaunchState({ launch }) as unknown as ReturnType<typeof useThreadLaunch>,
    );
    renderDialog();

    fireShortcut();
    expect(launch).not.toHaveBeenCalled();

    // 确认后快捷键放行
    await userEvent.click(screen.getByRole("button", { name: /继续/ }));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: /启动/ })).toBeInTheDocument(),
    );
    fireShortcut();
    await waitFor(() => expect(launch).toHaveBeenCalledTimes(1));
  });

  it("does not launch via the shortcut when canLaunch is false", async () => {
    const launch = vi.fn(async () => null);
    vi.mocked(useThreadLaunch).mockReturnValue(
      mockLaunchState({
        launch,
        canLaunch: false,
        environment: null,
        binding: null,
      }) as unknown as ReturnType<typeof useThreadLaunch>,
    );
    renderDialog();

    await userEvent.click(screen.getByRole("button", { name: /继续/ }));
    fireShortcut();
    expect(launch).not.toHaveBeenCalled();
  });
});
