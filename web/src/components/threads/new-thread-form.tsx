import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useNavigate } from "react-router";
import { Check, ChevronDown, Cpu, Folder, GitBranch, History, Laptop } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
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
import {
  buildModelSelection,
  buildWorkspace,
  canOperate,
  chooseRef,
  createIdTracker,
  effortValue,
  parseProviders,
  previousWorktreeLabel,
  resolveModel,
  resolvePreviousWorktree,
  READ_ONLY_HINT,
  type ModelPick,
  type WorkspaceChoice,
} from "@/lib/threads-compose";
import { cn } from "@/lib/utils";
import { ThreadComposer } from "./thread-composer";
import { actionErrorText } from "./thread-queue";

const CHIP =
  "inline-flex h-7 max-w-48 items-center gap-1.5 rounded-lg bg-muted/50 px-2 text-xs transition-colors hover:bg-muted focus-visible:outline-hidden focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-50";

function Chip({
  label,
  icon,
  ariaLabel,
  disabled,
  children,
  menuClassName,
}: {
  label: string;
  icon: ReactNode;
  ariaLabel: string;
  disabled?: boolean;
  children: ReactNode;
  menuClassName?: string;
}) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger aria-label={ariaLabel} disabled={disabled} className={CHIP}>
        {icon}
        <span className="truncate">{label}</span>
        <ChevronDown className="h-3 w-3 shrink-0 text-muted-foreground" />
      </DropdownMenuTrigger>
      <DropdownMenuContent className={cn("w-64", menuClassName)}>
        {children}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

/** 运行环境下拉：只读环境置灰不可选。 */
export function EnvironmentChip({
  environments,
  selected,
  onChange,
}: {
  environments: Environment[];
  selected?: Environment;
  onChange: (id: string) => void;
}) {
  return (
    <Chip
      ariaLabel="运行环境"
      icon={<Laptop className="h-3.5 w-3.5 shrink-0" />}
      label={selected ? selected.name : "选择环境"}
    >
      <DropdownMenuRadioGroup value={selected?.id ?? ""} onValueChange={onChange}>
        {environments.map((env) => (
          <DropdownMenuRadioItem
            key={env.id}
            value={env.id}
            disabled={!canOperate(env)}
          >
            <span className="truncate">{env.name}</span>
            {!canOperate(env) && (
              <span className="ml-auto text-xs text-muted-foreground">只读</span>
            )}
          </DropdownMenuRadioItem>
        ))}
      </DropdownMenuRadioGroup>
    </Chip>
  );
}

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
  const modeLabel =
    choice.kind === "worktree"
      ? "New worktree"
      : choice.kind === "existing"
        ? "Worktree"
        : "Current checkout";

  const submit = async () => {
    if (!project || !workspace) return;
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
    try {
      const result = await create.mutateAsync({
        command_id: ids.command_id,
        thread_id: ids.thread_id,
        project_id: project.id,
        message: { message_id: ids.message_id, text, attachments },
        model_selection: buildModelSelection(resolved),
        runtime_mode: "full-access",
        workspace,
      });
      tracker.reset();
      uploads.clear();
      navigate(
        `/threads/${encodeURIComponent(environment.id)}/${encodeURIComponent(result.thread_id)}`,
      );
    } catch {
      // 错误由 create.error 展示；重试沿用同一组 ID。
    }
  };

  const ready = Boolean(composer.data);
  const model = resolved.model;

  const toolbar = (
    <>
      <EnvironmentChip
        environments={environments}
        selected={environment}
        onChange={onEnvironmentChange}
      />
      <Chip
        ariaLabel="项目"
        icon={<Folder className="h-3.5 w-3.5 shrink-0" />}
        label={project?.title ?? (composer.isLoading ? "加载中…" : "无可用项目")}
        disabled={!writable || !projects?.length}
      >
        <DropdownMenuRadioGroup value={project?.id ?? ""} onValueChange={setProjectId}>
          {projects?.map((p) => (
            <DropdownMenuRadioItem key={p.id} value={p.id}>
              <span className="truncate">{p.title}</span>
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </Chip>
      <Chip
        ariaLabel="工作区"
        icon={<GitBranch className="h-3.5 w-3.5 shrink-0" />}
        label={modeLabel}
        disabled={!writable || !project || !workspaceData || !isRepo}
      >
        <DropdownMenuItem onClick={() => setChoice({ kind: "root" })}>
          Current checkout
          {choice.kind === "root" && <Check className="ml-auto" />}
        </DropdownMenuItem>
        <DropdownMenuItem
          onClick={() =>
            setChoice({
              kind: "worktree",
              baseRef: workspaceData?.ref_name ?? "",
              newBranch: choice.kind === "worktree" ? choice.newBranch : "",
            })
          }
        >
          New worktree
          {choice.kind === "worktree" && <Check className="ml-auto" />}
        </DropdownMenuItem>
        {previous && (
          <DropdownMenuItem
            onClick={() =>
              setChoice({ kind: "existing", path: previous.path, branch: previous.branch })
            }
          >
            <History />
            <span className="truncate">{previousWorktreeLabel(previous)}</span>
            {choice.kind === "existing" && choice.path === previous.path && (
              <Check className="ml-auto" />
            )}
          </DropdownMenuItem>
        )}
      </Chip>
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
            disabled={!writable}
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
          className="h-7 w-36 text-xs"
        />
      )}
      <Chip
        ariaLabel="模型"
        icon={<Cpu className="h-3.5 w-3.5 shrink-0" />}
        label={model ? model.name : "默认模型"}
        disabled={!writable || providers.length === 0}
        menuClassName="max-h-80 w-72"
      >
        {providers.map((provider) => (
          <DropdownMenuGroup key={provider.instanceId}>
            <DropdownMenuLabel>{provider.name}</DropdownMenuLabel>
            {provider.models.map((m) => (
              <DropdownMenuItem
                key={m.slug}
                onClick={() =>
                  setPick({ instanceId: provider.instanceId, model: m.slug, efforts: {} })
                }
              >
                <span className="truncate">{m.name}</span>
                {resolved.provider?.instanceId === provider.instanceId &&
                  model?.slug === m.slug && <Check className="ml-auto" />}
              </DropdownMenuItem>
            ))}
          </DropdownMenuGroup>
        ))}
      </Chip>
      {model?.descriptors.map((d) => (
        <Chip
          key={d.id}
          ariaLabel={d.label}
          icon={<span className="text-muted-foreground">{d.label}</span>}
          label={
            d.choices.find((c) => c.id === effortValue(d, resolved))?.label ?? "默认"
          }
          disabled={!writable}
        >
          <DropdownMenuRadioGroup
            value={effortValue(d, resolved) ?? ""}
            onValueChange={(value) =>
              resolved.provider &&
              model &&
              setPick({
                instanceId: resolved.provider.instanceId,
                model: model.slug,
                efforts: { ...resolved.efforts, [d.id]: value },
              })
            }
          >
            {d.choices.map((c) => (
              <DropdownMenuRadioItem key={c.id} value={c.id}>
                {c.label}
              </DropdownMenuRadioItem>
            ))}
          </DropdownMenuRadioGroup>
        </Chip>
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
        error={create.isError ? actionErrorText(create.error) : undefined}
      />
    </div>
  );
}
