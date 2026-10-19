import { useMemo } from "react";
import { Trash2 } from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  OptionSelect,
  withMissingOption,
} from "@/components/ui/option-select";
import { ModelPicker } from "@/components/threads/model-picker";
import { EffortChip } from "@/components/threads/thread-chips";
import type { Environment } from "@/lib/api/threads";
import type { ReviewCombination } from "@/lib/api/reviews";
import { useComposerOptions } from "@/lib/hooks/use-threads";
import { useProjectT3Bindings } from "@/lib/hooks/use-t3-bindings";
import {
  buildModelSelection,
  parseProviders,
  resolveModel,
  type ModelPick,
} from "@/lib/threads-compose";

// 单个审查组合行（INT-135）：环境 + harness/模型（ModelPicker 的 provider
// rail 即实例）+ 每个选项描述符一个 EffortChip。环境只能选已通过
// project_t3_bindings 绑定该项目的环境；切换环境清空下游模型选择。

export function CombinationRow({
  value,
  onChange,
  onRemove,
  environments,
  projectId,
  disabled,
}: {
  value: ReviewCombination;
  onChange: (next: ReviewCombination) => void;
  onRemove: () => void;
  /** 当前用户的全部环境；行内按项目绑定过滤后展示。 */
  environments: Environment[];
  projectId: string;
  disabled?: boolean;
}) {
  const {
    data: bindings,
    isLoading: bindingsLoading,
    isError: bindingsError,
  } = useProjectT3Bindings(projectId);
  const {
    data: composer,
    isLoading: composerLoading,
    isError: composerError,
  } = useComposerOptions(value.environment_id);

  // 只展示已绑定该项目的 T3 环境；未绑定的环境不能选。
  const boundIds = useMemo(
    () => new Set((bindings ?? []).map((b) => b.environment_id)),
    [bindings],
  );
  const boundEnvs = useMemo(
    () => environments.filter((env) => boundIds.has(env.id)),
    [environments, boundIds],
  );

  const providers = useMemo(
    () => parseProviders(composer?.config),
    [composer?.config],
  );

  const pick: ModelPick | null =
    value.instance_id && value.model
      ? {
          instanceId: value.instance_id,
          model: value.model,
          efforts: Object.fromEntries(
            (value.options ?? []).map((o) => [o.id, String(o.value)]),
          ),
        }
      : null;
  const resolved = resolveModel(providers, undefined, pick);
  // 已保存的模型在当前 providers 里找不到（换环境或快照过期）时标记失效。
  const modelMissing = Boolean(
    pick &&
      !providers.some(
        (p) =>
          p.instanceId === pick.instanceId &&
          p.models.some((m) => m.slug === pick.model),
      ),
  );

  // ModelPicker/EffortChip 给出的都是合法 pick；resolve 后按 canonical
  // {id,value} 数组写回 options。
  const applyPick = (next: ModelPick) => {
    const sel = buildModelSelection(resolveModel(providers, undefined, next));
    if (!sel) return;
    onChange({
      environment_id: value.environment_id,
      instance_id: sel.instance_id,
      model: sel.model,
      options: sel.options,
    });
  };

  const envMissingLabel = () => {
    if (bindingsLoading) return "加载中…";
    if (bindingsError) return "加载失败，请重试";
    const name =
      environments.find((env) => env.id === value.environment_id)?.name ??
      value.environment_name ??
      value.environment_id;
    return `${name}（已失效）`;
  };

  return (
    <div className="flex flex-wrap items-center gap-2 rounded-lg border px-3 py-2">
      <OptionSelect
        aria-label="环境"
        value={value.environment_id}
        onValueChange={(next) =>
          onChange({
            environment_id: next ?? "",
            instance_id: "",
            model: "",
            options: undefined,
          })
        }
        options={withMissingOption(
          // 只读环境（access=read-only）不可选：审查运行需要 operate 权限，
          // 但已保存组合若绑在只读环境上仍要显示出来。
          boundEnvs.map((env) => ({
            value: env.id,
            label:
              env.access === "read-only" ? `${env.name}（只读）` : env.name,
            disabled: env.access === "read-only",
          })),
          value.environment_id,
          envMissingLabel(),
        )}
        placeholder="选择环境"
        disabled={disabled}
        className="w-44"
      />
      <ModelPicker
        providers={providers}
        resolved={resolved}
        onPick={applyPick}
        disabled={
          disabled ||
          !value.environment_id ||
          (providers.length === 0 && !pick)
        }
        ariaLabel="模型"
        triggerLabel={
          !value.environment_id
            ? "先选环境"
            : composerLoading
              ? "加载中…"
              : composerError
                ? "加载失败，请重试"
                : modelMissing
                  ? `${value.model}（已失效）`
                  : pick
                    ? undefined
                    : "选择模型"
        }
      />
      {/* 只有显式选中且未失效的模型才展示选项 chips：pick 为空或失效时
          resolved 回落到 provider 默认模型， chips 写在回落模型上会悄悄改掉组合。 */}
      {pick &&
        !modelMissing &&
        resolved.model?.descriptors.map((d) => (
          <EffortChip
            key={d.id}
            descriptor={d}
            resolved={resolved}
            disabled={disabled}
            onChange={applyPick}
          />
        ))}
      <Button
        variant="ghost"
        size="icon-sm"
        aria-label="删除组合"
        disabled={disabled}
        onClick={onRemove}
        className="ml-auto"
      >
        <Trash2 className="h-4 w-4" />
      </Button>
    </div>
  );
}
