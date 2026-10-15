import { useMemo, useState } from "react";
import { useNavigate } from "react-router";
import { toast } from "sonner";
import {
  isLaunchable,
  joinEnvironmentsWithBindings,
  pickAutoEnvironmentRow,
  preferenceModelPick,
  resolveEnvironmentChoice,
} from "@/lib/issue-launch";
import { useProjectT3Bindings } from "@/lib/hooks/use-t3-bindings";
import {
  useComposerOptions,
  useCreateThread,
  useEnvironments,
  useThreads,
  useWorkspaceOptions,
} from "@/lib/hooks/use-threads";
import { actionErrorText } from "@/lib/threads";
import {
  buildModelSelection,
  createIdTracker,
  parseProviders,
  resolveModel,
  type ModelPick,
} from "@/lib/threads-compose";

/** 环境偏好来源：Issue 模板偏好或调用方自带的等价配置 */
export type ThreadLaunchPreferences = IssuePromptPreferences;

export interface ThreadLaunchOptions {
  projectId: string;
  /** 启动线程时发送的完整提示词 */
  promptText: string;
  /** 偏好作用域：换环境/换模板回落该作用域默认值时用 */
  scope: string;
  preferences?: ThreadLaunchPreferences | null;
}

export type WorkspaceKind = "worktree" | "root";

/**
 * 「启动线程」的共享机制：环境/绑定/负载选择、模型与工作区解析、
 * 创建 mutation 与错误兜底。launch-issue-thread-dialog 与截图标注的
 * 启动对话框共用；UI 细节（模板选择、偏好持久化）留在各自的弹框里。
 */
export function useThreadLaunch({
  projectId,
  promptText,
  scope,
  preferences,
}: ThreadLaunchOptions) {
  const navigate = useNavigate();
  const environmentsQuery = useEnvironments();
  const bindingsQuery = useProjectT3Bindings(projectId);
  const threadsQuery = useThreads();

  const rows = useMemo(
    () =>
      joinEnvironmentsWithBindings(
        environmentsQuery.data ?? [],
        bindingsQuery.data ?? [],
      ),
    [environmentsQuery.data, bindingsQuery.data],
  );
  const threads = threadsQuery.data?.items ?? [];
  const autoRow = pickAutoEnvironmentRow(rows, threads);

  // 环境默认值 = 偏好（仍可启动时）→ 自动负载；用户显式选择覆盖二者。
  const prefEnv = resolveEnvironmentChoice(preferences?.environment, rows);
  const [envPick, setEnvPick] = useState<string | null>(null);
  const envChoice = envPick ?? prefEnv.choice;
  const selectedRow =
    envChoice === "auto"
      ? autoRow
      : rows.find((row) => row.environment.id === envChoice);
  const launchableRow =
    selectedRow && isLaunchable(selectedRow) ? selectedRow : undefined;
  const environment = launchableRow?.environment;
  const binding = launchableRow?.binding;

  // 模型/工作区选择按「外部 scope + 实际选中环境」隔离：外部 scope（模板偏好
  // 等）之外必须并入 environment.id，否则换环境会把上一环境的模型/工作区
  // 选择带过去（标注弹框的外部 scope 是空串，Issue 弹框的是偏好环境而非
  // 实际选中环境）；自动负载也落到 autoRow 的具体环境上
  const pickScope = `${scope || "default"}@${environment?.id ?? "auto"}`;
  const [pickState, setPickState] = useState<{
    scope: string;
    value: ModelPick | null;
  }>({ scope: pickScope, value: null });
  const pick = pickState.scope === pickScope ? pickState.value : null;
  const setPick = (value: ModelPick | null) => setPickState({ scope: pickScope, value });
  const [workspaceState, setWorkspaceState] = useState<{
    scope: string;
    value: WorkspaceKind;
  } | null>(null);
  const workspaceKind =
    (workspaceState?.scope === pickScope ? workspaceState.value : undefined) ??
    preferences?.workspace ??
    "worktree";
  const setWorkspace = (value: WorkspaceKind) =>
    setWorkspaceState({ scope: pickScope, value });

  const composer = useComposerOptions(environment?.id ?? "");
  const workspaceOptions = useWorkspaceOptions(
    environment?.id ?? "",
    binding?.t3_project_id ?? "",
  );
  const t3Project = composer.data?.projects.find(
    (p) => p.id === binding?.t3_project_id,
  );
  const providers = useMemo(
    () => parseProviders(composer.data?.config),
    [composer.data],
  );
  const prefPick = useMemo(
    () => preferenceModelPick(preferences?.model_selection, providers),
    [preferences, providers],
  );
  // 偏好模型在目标环境不存在（含快照缺失该实例）时回落项目默认并提示。
  const modelPrefMissed = Boolean(
    preferences?.model_selection && composer.data && !prefPick,
  );
  const resolved = resolveModel(providers, t3Project, pick ?? prefPick);

  const isRepo = workspaceOptions.data?.is_repo !== false;
  const workspace: WorkspaceKind =
    workspaceKind === "worktree" && isRepo ? "worktree" : "root";
  const refName = workspaceOptions.data?.ref_name;

  const create = useCreateThread(environment?.id ?? "");
  const [tracker] = useState(createIdTracker);
  const [launchError, setLaunchError] = useState<unknown>(null);
  const launching = create.isPending;

  const environments = environmentsQuery.data;
  const hasBinding = rows.some((row) => row.binding);
  const bindingStale = Boolean(
    environment && binding && composer.data && !t3Project,
  );
  const canLaunch =
    Boolean(environment && binding) &&
    !bindingStale &&
    !composer.isError &&
    !launching;

  const bindSettingsTo = `/projects?edit=${encodeURIComponent(projectId)}&section=t3`;
  const goBind = (onClose: () => void) => {
    onClose();
    navigate(bindSettingsTo);
  };
  /** 还没有任何环境时引导去设置页配对（与「去绑定」不同目的地）。 */
  const goPairSettings = (onClose: () => void) => {
    onClose();
    navigate("/settings/environments");
  };

  const launch = async (
    onClose: () => void,
    onSuccess?: (result: { thread_id: string }) => void,
  ) => {
    if (!environment || !binding || launching) return null;
    setLaunchError(null);
    // 新 worktree 不传 base_ref：由服务端取 T3 项目主检出的当前分支。
    const workspaceInput =
      workspace === "worktree"
        ? ({ type: "worktree" } as const)
        : ({ type: "root" } as const);
    const modelSelection = buildModelSelection(resolved);
    try {
      const ids = tracker.idsFor(
        JSON.stringify([
          environment.id,
          binding.t3_project_id,
          promptText,
          workspaceInput,
          modelSelection,
        ]),
      );
      const result = await create.mutateAsync({
        command_id: ids.command_id,
        thread_id: ids.thread_id,
        project_id: binding.t3_project_id,
        message: { message_id: ids.message_id, text: promptText },
        model_selection: modelSelection,
        runtime_mode: "full-access",
        workspace: workspaceInput,
      });
      tracker.reset();
      onSuccess?.(result);
      onClose();
      toast.success(`已在 ${environment.name} 启动线程`, {
        action: {
          label: "查看",
          onClick: () =>
            navigate(
              `/threads/${encodeURIComponent(environment.id)}/${encodeURIComponent(result.thread_id)}`,
            ),
        },
      });
      return result;
    } catch (error) {
      // mutation 失败经 create.isError 展示在弹框内；提交前的异常（ID 生成等）
      // 没有 mutation 状态，须本地兜住。重试沿用同一组 ID，不会重复建线程。
      setLaunchError(error);
      return null;
    }
  };

  return {
    environmentsQuery,
    environments,
    rows,
    threads,
    autoRow,
    prefEnv,
    envChoice,
    setEnvPick,
    selectedRow,
    environment,
    binding,
    workspace,
    setWorkspace,
    isRepo,
    refName,
    composer,
    workspaceOptions,
    providers,
    prefPick,
    modelPrefMissed,
    resolved,
    setPick,
    create,
    launchError,
    setLaunchError,
    launching,
    hasBinding,
    bindingStale,
    canLaunch,
    bindSettingsTo,
    goBind,
    goPairSettings,
    launch,
    launchErrorText: () =>
      actionErrorText(create.error ?? launchError, "启动失败，请重试"),
  };
}
