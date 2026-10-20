import { useMemo, useState, type ReactNode } from "react";
import { Check, Loader2, Search } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { ProviderIcon } from "@/components/threads/provider-icon";
import { errorText } from "@/lib/api/client";
import {
  AI_PROVIDER_CUSTOM_ID,
  type AIProviderTestCheck,
} from "@/lib/api/ai-providers";
import {
  useAIProviderCatalog,
  useCreateAIProvider,
  useTestAIProvider,
} from "@/lib/hooks/use-ai-providers";
import { ConnectionChecks } from "./connection-checks";

/**
 * 目录选项行：自定义行与目录行共用一份 markup。
 * 用 button + role=option：Tab 可聚焦、Enter/Space 可激活；焦点在搜索框时
 * IME 组合按键不会落到按钮上，天然不误选。
 */
function CatalogOptionRow({
  iconId,
  title,
  subtitle,
  selected,
  onSelect,
}: {
  iconId: string;
  title: string;
  subtitle: ReactNode;
  selected: boolean;
  onSelect: () => void;
}) {
  return (
    <button
      type="button"
      role="option"
      aria-selected={selected}
      className="flex w-full cursor-pointer items-center gap-2.5 rounded-md px-2 py-2 text-left select-none hover:bg-accent aria-selected:bg-accent"
      onClick={onSelect}
    >
      <ProviderIcon providerInstanceId={iconId} className="size-4 shrink-0" />
      <div className="min-w-0 flex-1">
        <div className="truncate text-sm font-medium">{title}</div>
        <div className="truncate text-xs text-muted-foreground">{subtitle}</div>
      </div>
      {selected && <Check className="size-4 shrink-0" aria-hidden />}
    </button>
  );
}

/**
 * 新建提供商：搜索选择目录提供商（或「自定义」）→ 填 API Key → 测试连接 →
 * 保存。目录提供商的地址由目录决定不显示输入；custom 才显示地址与
 * 测试用模型字段。检查结果与被测目标绑定：目标字段再变，旧结果即隐藏。
 */
export function CreateProviderDialog({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  if (!open) return null;
  return <CreateProviderDialogInner onClose={() => onOpenChange(false)} />;
}

function CreateProviderDialogInner({ onClose }: { onClose: () => void }) {
  const catalog = useAIProviderCatalog();
  const create = useCreateAIProvider();
  const testConnection = useTestAIProvider();

  const [query, setQuery] = useState("");
  const [catalogId, setCatalogId] = useState<string | null>(null);
  const [name, setName] = useState("");
  const [baseUrl, setBaseUrl] = useState("");
  const [model, setModel] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [tested, setTested] = useState<{
    targetKey: string;
    checks: AIProviderTestCheck[];
  } | null>(null);

  const catalogData = catalog.data;
  const q = query.trim().toLowerCase();
  const filtered = useMemo(() => {
    const providers = catalogData ?? [];
    return q
      ? providers.filter(
          (p) =>
            p.name.toLowerCase().includes(q) ||
            p.id.toLowerCase().includes(q) ||
            p.api.toLowerCase().includes(q),
        )
      : providers;
  }, [catalogData, q]);
  const providers = catalogData ?? [];
  const isCustom = catalogId === AI_PROVIDER_CUSTOM_ID;
  const selectedProvider =
    catalogId && !isCustom
      ? providers.find((p) => p.id === catalogId)
      : undefined;
  const busy = create.isPending;
  const testing = testConnection.isPending;

  // 被测目标指纹：由发给测试端点的全部字段组成，任一字段再改旧结果即失效。
  const testTargetKey = JSON.stringify([
    catalogId,
    isCustom ? baseUrl.trim() : "",
    isCustom ? model.trim() : "",
    apiKey.trim(),
  ]);
  const currentChecks = tested?.targetKey === testTargetKey ? tested.checks : null;

  const validate = (): string | null => {
    if (!catalogId) return "请先选择提供商";
    if (isCustom) {
      if (!/^https?:\/\//.test(baseUrl.trim()))
        return "API 地址必须是 http/https";
    }
    if (!apiKey.trim()) return "请填写 API Key";
    return null;
  };

  const handleTest = async () => {
    const problem = validate();
    if (problem) {
      toast.error(problem);
      return;
    }
    // 自定义提供商没有目录模型，测试连接必须指定一个模型。
    if (isCustom && !model.trim()) {
      toast.error("自定义提供商需填写用于测试的模型");
      return;
    }
    testConnection.reset();
    setTested(null);
    try {
      const result = await testConnection.mutateAsync({
        catalog_id: catalogId ?? undefined,
        ...(isCustom ? { base_url: baseUrl.trim(), model: model.trim() } : {}),
        api_key: apiKey.trim(),
      });
      setTested({ targetKey: testTargetKey, checks: result.checks });
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
      await create.mutateAsync({
        catalog_id: catalogId!,
        name: name.trim() || undefined,
        base_url: isCustom ? baseUrl.trim() : undefined,
        api_key: apiKey.trim(),
      });
      toast.success("AI 提供商已添加");
      onClose();
    } catch (err) {
      toast.error(errorText(err, "保存失败"));
    }
  };

  return (
    <Dialog open onOpenChange={(next) => !next && onClose()}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>添加 AI 提供商</DialogTitle>
          <DialogDescription>
            从内置目录选择提供商并填入 API Key；选「自定义」时手填 OpenAI
            兼容地址。
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="provider-search">提供商</Label>
            <div className="relative">
              <Search
                className="absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground"
                aria-hidden
              />
              <Input
                id="provider-search"
                className="pl-8"
                placeholder="搜索提供商（名称或地址）…"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
              />
            </div>
            <div
              role="listbox"
              aria-label="提供商目录"
              className="max-h-56 overflow-y-auto rounded-lg border p-1"
            >
              <CatalogOptionRow
                iconId={AI_PROVIDER_CUSTOM_ID}
                title="自定义"
                subtitle="手填 OpenAI 兼容 API 地址"
                selected={isCustom}
                onSelect={() => setCatalogId(AI_PROVIDER_CUSTOM_ID)}
              />
              {catalog.isLoading ? (
                <div className="space-y-1 p-1">
                  {Array.from({ length: 4 }).map((_, i) => (
                    <Skeleton key={i} className="h-10 rounded-md" />
                  ))}
                </div>
              ) : (
                filtered.map((p) => (
                  <CatalogOptionRow
                    key={p.id}
                    iconId={p.id}
                    title={p.name}
                    subtitle={`${p.api} · ${p.models.length} 个模型`}
                    selected={catalogId === p.id}
                    onSelect={() => setCatalogId(p.id)}
                  />
                ))
              )}
              {catalog.isError && (
                <div
                  data-testid="provider-catalog-error"
                  className="flex items-center justify-between gap-2 px-2 py-3"
                >
                  <p className="text-xs text-destructive">目录加载失败</p>
                  <Button
                    variant="outline"
                    size="xs"
                    onClick={() => void catalog.refetch()}
                  >
                    重试
                  </Button>
                </div>
              )}
              {/* 「没有匹配」只在目录成功返回且过滤为空时出现。 */}
              {catalog.data && filtered.length === 0 && (
                <p className="px-2 py-6 text-center text-xs text-muted-foreground">
                  没有匹配的提供商
                </p>
              )}
            </div>
          </div>

          {catalogId && (
            <>
              <div className="space-y-1.5">
                <Label htmlFor="provider-name">名称（可选）</Label>
                <Input
                  id="provider-name"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder={isCustom ? "自定义" : selectedProvider?.name}
                />
              </div>
              {isCustom && (
                <>
                  <div className="space-y-1.5">
                    <Label htmlFor="provider-base-url">API 地址</Label>
                    <Input
                      id="provider-base-url"
                      value={baseUrl}
                      onChange={(e) => setBaseUrl(e.target.value)}
                      placeholder="https://api.example.com/v1"
                    />
                  </div>
                  <div className="space-y-1.5">
                    <Label htmlFor="provider-model">模型（用于测试连接）</Label>
                    <Input
                      id="provider-model"
                      value={model}
                      onChange={(e) => setModel(e.target.value)}
                      placeholder="例如：gpt-4o-mini"
                    />
                  </div>
                </>
              )}
              <div className="space-y-1.5">
                <Label htmlFor="provider-api-key">API Key</Label>
                <Input
                  id="provider-api-key"
                  type="password"
                  autoComplete="off"
                  value={apiKey}
                  onChange={(e) => setApiKey(e.target.value)}
                  placeholder="sk-…"
                />
                <p className="text-xs text-muted-foreground">
                  API Key 仅在后端加密存储，不会回显到网页。
                </p>
              </div>
            </>
          )}

          {testConnection.isError && (
            <p role="alert" className="text-xs text-destructive">
              {errorText(testConnection.error, "连接测试失败")}
            </p>
          )}
          {currentChecks && currentChecks.length > 0 && (
            <ConnectionChecks
              checks={currentChecks}
              data-testid="provider-connection-checks"
            />
          )}
        </div>

        <DialogFooter className="gap-2">
          <Button
            type="button"
            variant="outline"
            disabled={busy || testing || !catalogId}
            onClick={() => void handleTest()}
          >
            {testing && <Loader2 className="mr-1 h-3.5 w-3.5 animate-spin" />}
            测试连接
          </Button>
          <Button
            type="button"
            disabled={busy || testing || !catalogId}
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
