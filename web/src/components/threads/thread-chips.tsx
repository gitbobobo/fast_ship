import type { ReactNode } from "react";
import { Check, ChevronDown, Cpu, GitBranch, History, Laptop } from "lucide-react";
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
import type { Environment } from "@/lib/api/threads";
import {
  canOperate,
  effortValue,
  previousWorktreeLabel,
  type EffortDescriptor,
  type ModelPick,
  type PreviousWorktree,
  type ProviderChoice,
  type ResolvedModel,
  type WorkspaceChoice,
} from "@/lib/threads-compose";
import { cn } from "@/lib/utils";

const CHIP =
  "inline-flex h-7 max-w-48 items-center gap-1.5 rounded-lg bg-muted/50 px-2 text-xs transition-colors hover:bg-muted focus-visible:outline-hidden focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-50";

export function Chip({
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
  disabled,
}: {
  environments: Environment[];
  selected?: Environment;
  onChange: (id: string) => void;
  disabled?: boolean;
}) {
  return (
    <Chip
      ariaLabel="运行环境"
      icon={<Laptop className="h-3.5 w-3.5 shrink-0" />}
      label={selected ? selected.name : "选择环境"}
      disabled={disabled}
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

/** 工作区模式下拉：Current checkout / New worktree / 上次的 worktree。 */
export function WorkspaceModeChip({
  choice,
  previous,
  defaultBaseRef = "",
  disabled,
  onChange,
}: {
  choice: WorkspaceChoice;
  previous?: PreviousWorktree | null;
  /** 选 New worktree 时的默认基线（通常是主检出当前分支）。 */
  defaultBaseRef?: string;
  disabled?: boolean;
  onChange: (choice: WorkspaceChoice) => void;
}) {
  const label =
    choice.kind === "worktree"
      ? "New worktree"
      : choice.kind === "existing"
        ? "Worktree"
        : "Current checkout";
  return (
    <Chip
      ariaLabel="工作区"
      icon={<GitBranch className="h-3.5 w-3.5 shrink-0" />}
      label={label}
      disabled={disabled}
    >
      <DropdownMenuItem onClick={() => onChange({ kind: "root" })}>
        Current checkout
        {choice.kind === "root" && <Check className="ml-auto" />}
      </DropdownMenuItem>
      <DropdownMenuItem
        onClick={() =>
          onChange({
            kind: "worktree",
            baseRef: defaultBaseRef,
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
            onChange({ kind: "existing", path: previous.path, branch: previous.branch })
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
  );
}

/** 模型下拉：按实例分组列出模型，选中后重置推理强度等选项。 */
export function ModelChip({
  providers,
  resolved,
  disabled,
  onPick,
}: {
  providers: ProviderChoice[];
  resolved: ResolvedModel;
  disabled?: boolean;
  onPick: (pick: ModelPick) => void;
}) {
  return (
    <Chip
      ariaLabel="模型"
      icon={<Cpu className="h-3.5 w-3.5 shrink-0" />}
      label={resolved.model ? resolved.model.name : "默认模型"}
      disabled={disabled}
      menuClassName="max-h-80 w-72"
    >
      {providers.map((provider) => (
        <DropdownMenuGroup key={provider.instanceId}>
          <DropdownMenuLabel>{provider.name}</DropdownMenuLabel>
          {provider.models.map((model) => (
            <DropdownMenuItem
              key={model.slug}
              onClick={() =>
                onPick({ instanceId: provider.instanceId, model: model.slug, efforts: {} })
              }
            >
              <span className="truncate">{model.name}</span>
              {resolved.provider?.instanceId === provider.instanceId &&
                resolved.model?.slug === model.slug && (
                  <Check className="ml-auto" />
                )}
            </DropdownMenuItem>
          ))}
        </DropdownMenuGroup>
      ))}
    </Chip>
  );
}

/** 单个推理强度/选项下拉（模型描述符驱动的 effort chip）。 */
export function EffortChip({
  descriptor,
  resolved,
  disabled,
  onChange,
}: {
  descriptor: EffortDescriptor;
  resolved: ResolvedModel;
  disabled?: boolean;
  onChange: (pick: ModelPick) => void;
}) {
  const { provider, model } = resolved;
  return (
    <Chip
      ariaLabel={descriptor.label}
      icon={<span className="text-muted-foreground">{descriptor.label}</span>}
      label={
        descriptor.choices.find((c) => c.id === effortValue(descriptor, resolved))
          ?.label ?? "默认"
      }
      disabled={disabled}
    >
      <DropdownMenuRadioGroup
        value={effortValue(descriptor, resolved) ?? ""}
        onValueChange={(value) =>
          provider &&
          model &&
          onChange({
            instanceId: provider.instanceId,
            model: model.slug,
            efforts: { ...resolved.efforts, [descriptor.id]: value },
          })
        }
      >
        {descriptor.choices.map((choice) => (
          <DropdownMenuRadioItem key={choice.id} value={choice.id}>
            {choice.label}
          </DropdownMenuRadioItem>
        ))}
      </DropdownMenuRadioGroup>
    </Chip>
  );
}
