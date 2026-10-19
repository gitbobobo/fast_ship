import { useMemo, useState } from "react";
import { Link } from "react-router";
import { Loader2 } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { OptionSelect } from "@/components/ui/option-select";
import { Skeleton } from "@/components/ui/skeleton";
import { ApiRequestError } from "@/lib/api/client";
import type { Bot, BotConnectionCheck } from "@/lib/api/bots";
import { resolveModel, type ModelPick } from "@/lib/threads-compose";
import {
  botProviderChoices,
  hasCatalogModels,
} from "@/lib/bots/provider-choices";
import { ModelPicker } from "@/components/threads/model-picker";
import { ProviderIcon } from "@/components/threads/provider-icon";
import {
  useAIProviders,
  useAIProviderCatalog,
} from "@/lib/hooks/use-ai-providers";
import { CONTEXT_SOURCE_LABELS } from "./bot-metrics";
import {
  useCreateBot,
  useTestBotConnection,
  useUpdateBot,
} from "@/lib/hooks/use-bots";

/**
 * 人设占位文案：默认说话方式由服务端系统自动注入（篇幅跟信息量匹配、先给
 * 结论、只在确实需要时用标题和表格），人设是可选的追加补充，可以覆盖默认
 * 语气。新建时不预填——预填会让每个机器人背上与默认人设重复的文案。
 */
export const PERSONA_PLACEHOLDER =
  "留空使用默认说话方式（篇幅跟信息量匹配、先给结论、只在需要时用标题和表格）。需要补充时写这个机器人负责什么、语气偏好与边界…";

function errorText(err: unknown, fallback: string) {
  return err instanceof ApiRequestError && err.message ? err.message : fallback;
}

/** 一次「测试连接」随行的模型能力，连同它当时测试的目标一起记录：
 * 目标（提供商 + 模型）再变，这份能力就不再代表当前表单。 */
interface TestedCapability {
  provider_id: string;
  model: string;
  context_window?: number;
  context_source?: string;
  context_detail?: string;
}

/** 表单上展示的窗口能力：测试返回、已存值或目录规格，各带来源标注。 */
interface ShownCapability {
  context_window?: number;
  context_source?: string;
  context_detail?: string;
}

function CheckResult({ check }: { check: BotConnectionCheck }) {
  return (
    <li className="flex items-start gap-2 text-sm">
      <span
        className={check.passed ? "text-emerald-600 dark:text-emerald-400" : "text-destructive"}
        aria-label={check.passed ? "通过" : "未通过"}
      >
        {check.passed ? "✓" : "✕"}
      </span>
      <span>
        {check.name}
        {check.detail && (
          <span className="block text-xs text-muted-foreground">{check.detail}</span>
        )}
      </span>
    </li>
  );
}

/**
 * 创建/编辑机器人：名称 + 人设 + 模型。模型统一走 ModelPicker——
 * 已配置的 AI 提供商映射成 picker 的 providers（instanceId =
 * ai_providers.id），rail 按提供商分组，目录里没模型清单的提供商
 * （自定义）回落到手填模型名输入框。零提供商时给空态并链到设置页。
 * 地址与密钥不再按机器人配置，测试连接与保存都以 provider_id 为准。
 * 窗口能力展示跟随当前表单目标：测试返回 > 已存能力 > 目录规格，
 * 目标变了即隐藏旧展示。关闭即卸载：未保存的表单与测试结果不保留。
 */
export function BotFormDialog({
  open,
  editing,
  onClose,
  onSaved,
}: {
  open: boolean;
  editing: Bot | null;
  onClose: () => void;
  /** 创建/编辑成功后回调（父组件负责跳转或刷新提示）。 */
  onSaved?: (bot: Bot) => void;
}) {
  if (!open) return null;
  return (
    <BotFormDialogInner
      key={editing?.id ?? "new"}
      editing={editing}
      onClose={onClose}
      onSaved={onSaved}
    />
  );
}

function BotFormDialogInner({
  editing,
  onClose,
  onSaved,
}: {
  editing: Bot | null;
  onClose: () => void;
  onSaved?: (bot: Bot) => void;
}) {
  const create = useCreateBot();
  const update = useUpdateBot(editing?.id ?? "");
  const testConnection = useTestBotConnection();
  const providersQuery = useAIProviders();
  const catalogQuery = useAIProviderCatalog();

  const [name, setName] = useState(editing?.name ?? "");
  const [persona, setPersona] = useState(
    editing ? editing.persona_prompt : "",
  );
  // 模型选择的唯一事实来源是 pick；编辑时回填已存的 provider+model，
  // 模型已从目录下架时触发器标注「已失效」，不改选就保留原值。
  const [pick, setPick] = useState<ModelPick | null>(() =>
    editing
      ? { instanceId: editing.provider_id, model: editing.model, efforts: {} }
      : null,
  );
  // 提供商选择：picker 选中会同步过来；只有存在「手填模型」的提供商时
  // 才渲染独立的提供商下拉，否则提供商隐含在模型选择里。
  const [providerId, setProviderId] = useState(editing?.provider_id ?? "");
  const [customModel, setCustomModel] = useState(editing?.model ?? "");
  const [checks, setChecks] = useState<BotConnectionCheck[] | null>(null);
  const [tested, setTested] = useState<TestedCapability | null>(null);

  const aiProviders = providersQuery.data;
  const catalogById = useMemo(
    () =>
      new Map((catalogQuery.data ?? []).map((entry) => [entry.id, entry])),
    [catalogQuery.data],
  );

  // picker 只收目录里有模型清单的提供商；把当前提供商排最前，
  // 让尚未 pick 时 resolveModel 的回落落在它身上。
  const pickerProviders = useMemo(() => {
    const base = botProviderChoices(aiProviders ?? [], catalogById);
    const index = base.findIndex((p) => p.instanceId === providerId);
    return index > 0
      ? [base[index], ...base.slice(0, index), ...base.slice(index + 1)]
      : base;
  }, [aiProviders, catalogById, providerId]);

  const resolved = resolveModel(pickerProviders, undefined, pick);
  const pickValid =
    pick != null &&
    pickerProviders.some(
      (p) =>
        p.instanceId === pick.instanceId &&
        p.models.some((m) => m.slug === pick.model),
    );

  // 生效提供商：显式选择 > picker 选择 > picker 回落（第一个可选项）。
  const provider =
    (providerId ? aiProviders?.find((p) => p.id === providerId) : undefined) ??
    (pick
      ? aiProviders?.find((p) => p.id === pick.instanceId)
      : undefined) ??
    aiProviders?.find((p) => p.id === resolved.provider?.instanceId) ??
    aiProviders?.[0];

  const pickableIds = useMemo(
    () => new Set(pickerProviders.map((p) => p.instanceId)),
    [pickerProviders],
  );
  const customProviders = useMemo(
    () => (aiProviders ?? []).filter((p) => !pickableIds.has(p.id)),
    [aiProviders, pickableIds],
  );
  const isCustom = provider != null && !hasCatalogModels(provider, catalogById);

  // 提交口径：custom 提供商用手填模型；目录提供商用 pick（含已失效原值）
  // 或 picker 回落模型。
  const submitProviderId = provider?.id ?? "";
  const submitModel = isCustom
    ? customModel.trim()
    : pick && pick.instanceId === submitProviderId
      ? pick.model
      : resolved.provider?.instanceId === submitProviderId
        ? (resolved.model?.slug ?? "")
        : "";

  // 能力展示只跟随当前表单目标：测试结果属于它测试时的提供商+模型，
  // 已存能力属于已保存的提供商+模型，目录规格按选中模型实时查。
  let shownCapability: ShownCapability | null = null;
  if (
    tested &&
    tested.provider_id === submitProviderId &&
    tested.model === submitModel
  ) {
    shownCapability = tested;
  } else if (
    editing &&
    (editing.context_window ?? 0) > 0 &&
    editing.provider_id === submitProviderId &&
    editing.model === submitModel
  ) {
    shownCapability = {
      context_window: editing.context_window,
      context_source: editing.context_source,
      context_detail: editing.context_detail,
    };
  } else if (provider && !isCustom && submitModel) {
    const spec = catalogById
      .get(provider.catalog_id)
      ?.models.find((m) => m.id === submitModel);
    if (spec?.context_window) {
      shownCapability = {
        context_window: spec.context_window,
        context_source: "catalog",
      };
    }
  }

  const loading =
    providersQuery.isLoading ||
    (catalogQuery.isLoading &&
      (aiProviders ?? []).some((p) => p.catalog_id !== "custom"));
  const noProviders = !loading && (aiProviders?.length ?? 0) === 0;

  const busy = create.isPending || update.isPending;
  const testing = testConnection.isPending;

  const validate = (): string | null => {
    if (!name.trim()) return "请填写名称";
    if (!provider) return "请先在设置页配置 AI 提供商";
    if (!submitModel) return isCustom ? "请填写模型名称" : "请选择模型";
    return null;
  };

  const handleTest = async () => {
    if (!provider || !submitModel) {
      toast.error("测试前请先选择模型");
      return;
    }
    testConnection.reset();
    setChecks(null);
    try {
      const result = await testConnection.mutateAsync({
        ...(editing ? { bot_id: editing.id } : {}),
        provider_id: provider.id,
        model: submitModel,
      });
      setChecks(result.checks);
      // 记录本次测试返回的能力与目标：弹窗只展示「属于当前表单目标」的能力。
      setTested({
        provider_id: provider.id,
        model: submitModel,
        context_window: result.context_window,
        context_source: result.context_source,
        context_detail: result.context_detail,
      });
      if (!result.ok) toast.error("连接测试未全部通过");
    } catch (err) {
      toast.error(errorText(err, "连接测试失败"));
    }
  };

  const handleSubmit = async () => {
    const problem = validate();
    if (problem) {
      toast.error(problem);
      return;
    }
    try {
      const payload = {
        name: name.trim(),
        persona_prompt: persona.trim(),
        provider_id: submitProviderId,
        model: submitModel,
      };
      if (editing) {
        const saved = await update.mutateAsync(payload);
        toast.success("机器人已更新，配置对下个运行生效");
        onSaved?.(saved);
      } else {
        const saved = await create.mutateAsync(payload);
        toast.success("机器人已创建");
        onSaved?.(saved);
      }
      onClose();
    } catch (err) {
      toast.error(errorText(err, "保存失败"));
    }
  };

  return (
    <Dialog open onOpenChange={(next) => !next && onClose()}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{editing ? "编辑机器人" : "新建机器人"}</DialogTitle>
          <DialogDescription>
            内置智能体使用统一配置的 AI 提供商对话，通过白名单工具操作
            Fast Ship。
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="bot-name">名称</Label>
            <Input
              id="bot-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="例如：发布助手"
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="bot-persona">人设提示词</Label>
            <Textarea
              id="bot-persona"
              value={persona}
              onChange={(e) => setPersona(e.target.value)}
              placeholder={PERSONA_PLACEHOLDER}
              className="min-h-20"
            />
          </div>

          {loading ? (
            <Skeleton className="h-9 rounded-lg" />
          ) : noProviders ? (
            <div
              data-testid="bot-no-providers"
              className="rounded-lg border bg-muted/40 px-4 py-6 text-center"
            >
              <p className="text-sm text-muted-foreground">
                还没有配置 AI 提供商
              </p>
              <p className="mt-1 text-xs text-muted-foreground">
                请先在「
                <Link
                  to="/settings/ai-providers"
                  className="text-primary hover:underline"
                >
                  设置 → AI 提供商
                </Link>
                」添加提供商与 API Key，再回来新建机器人。
              </p>
            </div>
          ) : (
            <>
              {customProviders.length > 0 && (
                <div className="space-y-1.5">
                  <Label htmlFor="bot-provider">提供商</Label>
                  <OptionSelect
                    id="bot-provider"
                    aria-label="提供商"
                    options={(aiProviders ?? []).map((p) => ({
                      value: p.id,
                      label: (
                        <span className="flex items-center gap-2">
                          <ProviderIcon
                            providerInstanceId={p.catalog_id}
                            className="size-3.5"
                          />
                          {p.name}
                          {!pickableIds.has(p.id) && (
                            <span className="text-xs text-muted-foreground">
                              自定义
                            </span>
                          )}
                        </span>
                      ),
                    }))}
                    value={provider?.id ?? null}
                    placeholder="选择提供商"
                    onValueChange={(next) => {
                      setProviderId(next ?? "");
                      // 换提供商后旧 pick 失效，resolved 回落到新提供商。
                      setPick(null);
                    }}
                  />
                </div>
              )}
              <div className="space-y-1.5">
                <Label htmlFor="bot-model">模型</Label>
                {isCustom ? (
                  <Input
                    id="bot-model"
                    value={customModel}
                    onChange={(e) => setCustomModel(e.target.value)}
                    placeholder="例如：gpt-4o-mini"
                  />
                ) : (
                  <ModelPicker
                    providers={pickerProviders}
                    resolved={resolved}
                    ariaLabel="模型"
                    triggerLabel={
                      pick &&
                      !pickValid &&
                      pick.instanceId === provider?.id
                        ? `${pick.model}（已失效）`
                        : undefined
                    }
                    onPick={(next) => {
                      setPick(next);
                      setProviderId(next.instanceId);
                    }}
                  />
                )}
                {shownCapability != null &&
                  (shownCapability.context_window ?? 0) > 0 && (
                    <p
                      data-testid="bot-context-window"
                      className="text-xs text-muted-foreground"
                    >
                      上下文窗口{" "}
                      {(
                        shownCapability.context_window ?? 0
                      ).toLocaleString("en-US")}{" "}
                      tokens
                      {shownCapability.context_source &&
                        ` · 来源：${CONTEXT_SOURCE_LABELS[shownCapability.context_source] ?? shownCapability.context_source}`}
                      {shownCapability.context_detail &&
                        `（${shownCapability.context_detail}）`}
                    </p>
                  )}
              </div>
            </>
          )}

          {testConnection.isError && (
            <p role="alert" className="text-xs text-destructive">
              {errorText(testConnection.error, "连接测试失败")}
            </p>
          )}
          {checks && (
            <ul
              data-testid="bot-connection-checks"
              className="space-y-1.5 rounded-lg border bg-muted/30 px-3 py-2.5"
            >
              {checks.map((check) => (
                <CheckResult key={check.name} check={check} />
              ))}
            </ul>
          )}
        </div>

        <DialogFooter className="gap-2">
          <Button
            type="button"
            variant="outline"
            disabled={busy || testing || !provider}
            onClick={() => void handleTest()}
          >
            {testing && <Loader2 className="mr-1 h-3.5 w-3.5 animate-spin" />}
            测试连接
          </Button>
          <Button
            type="button"
            disabled={busy || testing || !provider}
            onClick={() => void handleSubmit()}
          >
            {busy && <Loader2 className="mr-1 h-3.5 w-3.5 animate-spin" />}
            保存
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
