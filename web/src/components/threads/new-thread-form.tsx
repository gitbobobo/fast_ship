import { useEffect, useMemo, useState } from "react";
import { useNavigate } from "react-router";
import { Folder, GitBranch } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import type { Environment } from "@/lib/api/threads";
import { useImageUploads } from "@/lib/hooks/use-image-uploads";
import {
  useComposerOptions,
  useCreateThread,
  useThreads,
  useWorkspaceOptions,
} from "@/lib/hooks/use-threads";
import { actionErrorText } from "@/lib/threads";
import {
  buildModelSelection,
  buildWorkspace,
  canOperate,
  chooseRef,
  createIdTracker,
  parseProviders,
  resolveModel,
  resolvePreviousWorktree,
  READ_ONLY_HINT,
  type ModelPick,
  type WorkspaceChoice,
} from "@/lib/threads-compose";
import {
  Chip,
  EffortChip,
  EnvironmentChip,
  ModelChip,
  WorkspaceModeChip,
} from "./thread-chips";
import { ThreadComposer } from "./thread-composer";

function useDebounced<T>(value: T, ms: number) {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const timer = setTimeout(() => setDebounced(value), ms);
    return () => clearTimeout(timer);
  }, [value, ms]);
  return debounced;
}

/**
 * 新建线程表单。按环境 key 渲染：换环境时项目、模型、工作区、上传引用
 * 一并重置，只有正文（由父级持有）保留。
 */
export function NewThreadForm({
  environment,
  environments,
  onEnvironmentChange,
  text,
  onTextChange,
}: {
  environment: Environment;
  environments: Environment[];
  onEnvironmentChange: (id: string) => void;
  text: string;
  onTextChange: (value: string) => void;
}) {
  const navigate = useNavigate();
  const writable = canOperate(environment);
  const composer = useComposerOptions(environment.id);
  const create = useCreateThread(environment.id);
  const uploads = useImageUploads(environment.id);
  const [tracker] = useState(createIdTracker);
  const [submitError, setSubmitError] = useState<unknown>(null);
  const threads = useThreads();

  const projects = composer.data?.projects;
  const [projectId, setProjectId] = useState("");
  const project = projects?.find((p) => p.id === projectId) ?? projects?.[0];

  const [pick, setPick] = useState<ModelPick | null>(null);
  const providers = useMemo(() => parseProviders(composer.data?.config), [composer.data]);
  const resolved = resolveModel(providers, project, pick);

  const [refQuery, setRefQuery] = useState("");
  const debouncedQuery = useDebounced(refQuery.trim(), 250);
  const baseOptions = useWorkspaceOptions(environment.id, project?.id ?? "");
  const refOptions = useWorkspaceOptions(environment.id, project?.id ?? "", debouncedQuery);
  const workspaceData = baseOptions.data;

  const [choiceState, setChoiceState] = useState<{
    projectId: string;
    choice: WorkspaceChoice;
  }>({ projectId: "", choice: { kind: "root" } });
  const rawChoice: WorkspaceChoice =
    choiceState.projectId === project?.id ? choiceState.choice : { kind: "root" };
  const choice: WorkspaceChoice =
    rawChoice.kind === "worktree" && !rawChoice.baseRef
      ? { ...rawChoice, baseRef: workspaceData?.ref_name ?? "" }
      : rawChoice;
  const setChoice = (next: WorkspaceChoice) =>
    setChoiceState({ projectId: project?.id ?? "", choice: next });

  const previous = resolvePreviousWorktree({
    threads: threads.data?.items ?? [],
    environmentId: environment.id,
    projectId: project?.id ?? "",
    workspace: workspaceData,
    currentWorktreePath: choice.kind === "existing" ? choice.path : undefined,
  });

  const workspace = buildWorkspace(choice);
  const isRepo = workspaceData?.is_repo !== false;

  const submit = async () => {
    if (!project || !workspace) return;
    setSubmitError(null);
    try {
      const attachments = uploads.attachmentsOf();
      const ids = tracker.idsFor(
        JSON.stringify([
          project.id,
          text,
          attachments.map((a) => a.id),
          workspace,
          buildModelSelection(resolved),
        ]),
      );
      const result = await uploads.submit(
        () =>
          create.mutateAsync({
            command_id: ids.command_id,
            thread_id: ids.thread_id,
            project_id: project.id,
            message: { message_id: ids.message_id, text, attachments },
            model_selection: buildModelSelection(resolved),
            runtime_mode: "full-access",
            workspace,
          }),
        attachments,
      );
      tracker.reset();
      navigate(
        `/threads/${encodeURIComponent(environment.id)}/${encodeURIComponent(result.thread_id)}`,
      );
    } catch (error) {
      // mutation 失败经 create.isError 落到同一错误位；提交前的异常（ID 生成等）
      // 没有 mutation 状态，须本地兜住。重试沿用同一组 ID。
      setSubmitError(error);
    }
  };

  const ready = Boolean(composer.data);
  const model = resolved.model;
  const launching = create.isPending;

  const toolbar = (
    <>
      <EnvironmentChip
        environments={environments}
        selected={environment}
        onChange={onEnvironmentChange}
        disabled={launching}
      />
      <Chip
        ariaLabel="项目"
        icon={<Folder className="h-3.5 w-3.5 shrink-0" />}
        label={project?.title ?? (composer.isLoading ? "加载中…" : "无可用项目")}
        disabled={!writable || !projects?.length || launching}
      >
        <DropdownMenuRadioGroup value={project?.id ?? ""} onValueChange={setProjectId}>
          {projects?.map((p) => (
            <DropdownMenuRadioItem key={p.id} value={p.id}>
              <span className="truncate">{p.title}</span>
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </Chip>
      <WorkspaceModeChip
        choice={choice}
        previous={previous}
        defaultBaseRef={workspaceData?.ref_name ?? ""}
        disabled={!writable || !project || !workspaceData || !isRepo || launching}
        onChange={setChoice}
      />
      {choice.kind === "root" && workspaceData?.ref_name && (
        <span
          className="inline-flex items-center gap-1 px-1 text-xs text-muted-foreground"
          title="Current checkout 使用项目主检出的当前分支"
        >
          <GitBranch className="h-3 w-3" />
          {workspaceData.ref_name}
        </span>
      )}
      {choice.kind !== "root" && (
        <>
          <Chip
            ariaLabel="分支"
            icon={<GitBranch className="h-3.5 w-3.5 shrink-0" />}
            label={
              choice.kind === "worktree"
                ? choice.baseRef
                  ? `From ${choice.baseRef}`
                  : "选择基线分支"
                : choice.branch || "worktree"
            }
            disabled={!writable || launching}
          >
            <DropdownMenuGroup>
              <DropdownMenuLabel>
                {refQuery ? `匹配「${refQuery}」` : "分支"}
              </DropdownMenuLabel>
              {(refOptions.data?.refs ?? []).map((ref) => (
                <DropdownMenuItem
                  key={`${ref.kind}:${ref.name}`}
                  onClick={() => setChoice(chooseRef(choice, ref, workspaceData?.cwd ?? ""))}
                >
                  <span className="truncate">{ref.name}</span>
                  {ref.worktree_path && (
                    <span className="ml-auto text-xs text-muted-foreground">worktree</span>
                  )}
                </DropdownMenuItem>
              ))}
            </DropdownMenuGroup>
          </Chip>
          <Input
            value={refQuery}
            onChange={(e) => setRefQuery(e.target.value)}
            placeholder="筛选分支"
            aria-label="筛选分支"
            disabled={launching}
            className="h-7 w-28 text-xs"
          />
        </>
      )}
      {choice.kind === "worktree" && (
        <Input
          value={choice.newBranch}
          onChange={(e) => setChoice({ ...choice, newBranch: e.target.value })}
          placeholder="新分支名（可选）"
          aria-label="新分支名"
          disabled={launching}
          className="h-7 w-36 text-xs"
        />
      )}
      <ModelChip
        providers={providers}
        resolved={resolved}
        disabled={!writable || providers.length === 0 || launching}
        onPick={setPick}
      />
      {model?.descriptors.map((d) => (
        <EffortChip
          key={d.id}
          descriptor={d}
          resolved={resolved}
          disabled={!writable || launching}
          onChange={setPick}
        />
      ))}
    </>
  );

  return (
    <div className="space-y-3">
      {!writable && (
        <p
          role="note"
          className="rounded-lg bg-muted/50 px-3 py-2 text-sm text-muted-foreground"
        >
          {READ_ONLY_HINT}
        </p>
      )}
      {composer.isError && (
        <div
          role="alert"
          className="flex items-center justify-between gap-2 rounded-lg bg-amber-500/10 px-3 py-2 text-sm text-amber-800 dark:text-amber-300"
        >
          <span>无法读取该环境的项目与模型配置</span>
          <Button variant="outline" size="sm" onClick={() => composer.refetch()}>
            重试
          </Button>
        </div>
      )}
      {ready && !projects?.length && (
        <p role="note" className="text-sm text-muted-foreground">
          该环境还没有项目，请先在 T3 Code 中添加项目。
        </p>
      )}
      <ThreadComposer
        environmentId={environment.id}
        text={text}
        onTextChange={onTextChange}
        uploads={uploads}
        placeholder={writable ? "描述你想让 Agent 做的事…" : READ_ONLY_HINT}
        submitLabel="发送"
        submitting={create.isPending}
        disabled={!writable}
        blocked={!writable || !ready || !project || !workspace}
        onSubmit={() => void submit()}
        toolbar={toolbar}
        error={
          create.isError || submitError !== null
            ? actionErrorText(create.error || submitError)
            : undefined
        }
      />
    </div>
  );
}
