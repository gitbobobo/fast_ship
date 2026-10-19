import { useMemo, useState } from "react";
import { Check, Loader2, Pencil, Plus, Search, Trash2 } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { HeaderActions } from "@/components/layout/header-actions";
import { ProviderIcon } from "@/components/threads/provider-icon";
import { SettingsPageShell } from "@/routes/settings/layout";
import { ApiRequestError } from "@/lib/api/client";
import type {
  AIProvider,
  AIProviderTestCheck,
  CatalogProvider,
} from "@/lib/api/ai-providers";
import {
  useAIProviders,
  useAIProviderCatalog,
  useCreateAIProvider,
  useUpdateAIProvider,
  useDeleteAIProvider,
  useTestAIProvider,
} from "@/lib/hooks/use-ai-providers";
import { formatDate } from "@/lib/utils/format";

/** 自定义提供商的目录 id 哨兵值，与契约一致。 */
const CUSTOM_ID = "custom";

function errorText(err: unknown, fallback: string) {
  return err instanceof ApiRequestError && err.message ? err.message : fallback;
}

function TestCheckRow({ check }: { check: AIProviderTestCheck }) {
  return (
    <li className="flex items-start gap-2 text-sm">
      <span
        className={
          check.passed
            ? "text-emerald-600 dark:text-emerald-400"
            : "text-destructive"
        }
        aria-label={check.passed ? "通过" : "未通过"}
      >
        {check.passed ? "✓" : "✕"}
      </span>
      <span>
        {check.name}
        {check.detail && (
          <span className="block text-xs text-muted-foreground">
            {check.detail}
          </span>
        )}
      </span>
    </li>
  );
}

function TestCheckList({ checks }: { checks: AIProviderTestCheck[] }) {
  return (
    <ul
      data-testid="provider-connection-checks"
      className="space-y-1.5 rounded-lg border bg-muted/30 px-3 py-2.5"
    >
      {checks.map((check) => (
        <TestCheckRow key={check.name} check={check} />
      ))}
    </ul>
  );
}

/** 目录行：图标 + 名称 + API 地址与模型数；选中态右侧打勾。 */
function CatalogRow({
  provider,
  selected,
  onSelect,
}: {
  provider: CatalogProvider;
  selected: boolean;
  onSelect: () => void;
}) {
  return (
    <div
      role="option"
      aria-selected={selected}
      className="flex cursor-pointer items-center gap-2.5 rounded-md px-2 py-2 select-none hover:bg-accent aria-selected:bg-accent"
      onClick={onSelect}
    >
      <ProviderIcon providerInstanceId={provider.id} className="size-4" />
      <div className="min-w-0 flex-1">
        <div className="truncate text-sm font-medium">{provider.name}</div>
        <div className="truncate text-xs text-muted-foreground">
          {provider.api} · {provider.models.length} 个模型
        </div>
      </div>
      {selected && <Check className="size-4 shrink-0" aria-hidden />}
    </div>
  );
}

/**
 * 新建提供商：搜索选择目录提供商（或「自定义」）→ 填 API Key → 测试连接 →
 * 保存。目录提供商的地址由目录决定不显示输入；custom 才显示地址与
 * 测试用模型字段。
 */
function CreateProviderDialog({
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
  const [checks, setChecks] = useState<AIProviderTestCheck[] | null>(null);

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
  const isCustom = catalogId === CUSTOM_ID;
  const selectedProvider =
    catalogId && !isCustom
      ? providers.find((p) => p.id === catalogId)
      : undefined;
  const busy = create.isPending;
  const testing = testConnection.isPending;

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
    setChecks(null);
    try {
      const result = await testConnection.mutateAsync({
        catalog_id: catalogId ?? undefined,
        ...(isCustom ? { base_url: baseUrl.trim(), model: model.trim() } : {}),
        api_key: apiKey.trim(),
      });
      setChecks(result.checks);
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
              <div
                role="option"
                aria-selected={isCustom}
                className="flex cursor-pointer items-center gap-2.5 rounded-md px-2 py-2 select-none hover:bg-accent aria-selected:bg-accent"
                onClick={() => setCatalogId(CUSTOM_ID)}
              >
                <ProviderIcon providerInstanceId="custom" className="size-4" />
                <div className="min-w-0 flex-1">
                  <div className="truncate text-sm font-medium">自定义</div>
                  <div className="truncate text-xs text-muted-foreground">
                    手填 OpenAI 兼容 API 地址
                  </div>
                </div>
                {isCustom && (
                  <Check className="size-4 shrink-0" aria-hidden />
                )}
              </div>
              {catalog.isLoading ? (
                <div className="space-y-1 p-1">
                  {Array.from({ length: 4 }).map((_, i) => (
                    <Skeleton key={i} className="h-10 rounded-md" />
                  ))}
                </div>
              ) : (
                filtered.map((p) => (
                  <CatalogRow
                    key={p.id}
                    provider={p}
                    selected={catalogId === p.id}
                    onSelect={() => setCatalogId(p.id)}
                  />
                ))
              )}
              {!catalog.isLoading && filtered.length === 0 && (
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
          {checks && <TestCheckList checks={checks} />}
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

/**
 * 编辑提供商：名称、custom 的地址与轮换密钥。catalog_id 与目录提供商的
 * base_url 不可改；填了新密钥才允许测试连接（密钥不回填，没法测已存值）。
 */
function EditProviderDialog({
  provider,
  open,
  onOpenChange,
}: {
  provider: AIProvider | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  if (!open || !provider) return null;
  return (
    <EditProviderDialogInner
      key={provider.id}
      provider={provider}
      onClose={() => onOpenChange(false)}
    />
  );
}

function EditProviderDialogInner({
  provider,
  onClose,
}: {
  provider: AIProvider;
  onClose: () => void;
}) {
  const catalog = useAIProviderCatalog();
  const update = useUpdateAIProvider(provider.id);
  const testConnection = useTestAIProvider();

  const catalogProvider = catalog.data?.find(
    (p) => p.id === provider.catalog_id,
  );
  const isCustom = provider.catalog_id === CUSTOM_ID;

  const [name, setName] = useState(provider.name);
  const [baseUrl, setBaseUrl] = useState(provider.base_url);
  const [model, setModel] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [checks, setChecks] = useState<AIProviderTestCheck[] | null>(null);

  const busy = update.isPending;
  const testing = testConnection.isPending;
  // 密钥不回填：已存密钥不在手里，只有填了新密钥才能跑测试连接。
  const canTest = apiKey.trim().length > 0 && (!isCustom || model.trim().length > 0);

  const handleTest = async () => {
    if (!apiKey.trim()) {
      toast.error("请输入要测试的 API Key");
      return;
    }
    if (isCustom && !model.trim()) {
      toast.error("自定义提供商需填写用于测试的模型");
      return;
    }
    testConnection.reset();
    setChecks(null);
    try {
      const result = await testConnection.mutateAsync({
        catalog_id: provider.catalog_id,
        ...(isCustom ? { base_url: baseUrl.trim(), model: model.trim() } : {}),
        api_key: apiKey.trim(),
      });
      setChecks(result.checks);
      if (!result.ok) toast.error("连接测试未全部通过");
    } catch (err) {
      toast.error(errorText(err, "连接测试失败"));
    }
  };

  const handleSubmit = async () => {
    if (!name.trim()) {
      toast.error("请填写名称");
      return;
    }
    if (isCustom && !/^https?:\/\//.test(baseUrl.trim())) {
      toast.error("API 地址必须是 http/https");
      return;
    }
    try {
      await update.mutateAsync({
        name: name.trim(),
        ...(isCustom ? { base_url: baseUrl.trim() } : {}),
        ...(apiKey.trim() ? { api_key: apiKey.trim() } : {}),
      });
      toast.success(
        apiKey.trim()
          ? "提供商已更新，新密钥对引用它的机器人下个运行生效"
          : "提供商已更新",
      );
      onClose();
    } catch (err) {
      toast.error(errorText(err, "保存失败"));
    }
  };

  return (
    <Dialog open onOpenChange={(next) => !next && onClose()}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>编辑 AI 提供商</DialogTitle>
          <DialogDescription>
            {isCustom
              ? "自定义提供商可改地址与密钥。"
              : `${catalogProvider?.name ?? provider.catalog_id} 的地址由内置目录决定；更新密钥后所有引用它的机器人下个运行生效。`}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="edit-provider-name">名称</Label>
            <Input
              id="edit-provider-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </div>
          {isCustom ? (
            <>
              <div className="space-y-1.5">
                <Label htmlFor="edit-provider-base-url">API 地址</Label>
                <Input
                  id="edit-provider-base-url"
                  value={baseUrl}
                  onChange={(e) => setBaseUrl(e.target.value)}
                  placeholder="https://api.example.com/v1"
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="edit-provider-model">模型（用于测试连接）</Label>
                <Input
                  id="edit-provider-model"
                  value={model}
                  onChange={(e) => setModel(e.target.value)}
                  placeholder="例如：gpt-4o-mini"
                />
              </div>
            </>
          ) : (
            <p className="rounded-lg bg-muted/40 px-3 py-2 text-xs text-muted-foreground">
              API 地址：{provider.base_url}
            </p>
          )}
          <div className="space-y-1.5">
            <Label htmlFor="edit-provider-api-key">API Key</Label>
            <Input
              id="edit-provider-api-key"
              type="password"
              autoComplete="off"
              value={apiKey}
              onChange={(e) => setApiKey(e.target.value)}
              placeholder="留空保留已配置的密钥"
            />
            <p className="text-xs text-muted-foreground">
              已配置（末四位 {provider.api_key_last4}），不会回显明文。
            </p>
          </div>

          {testConnection.isError && (
            <p role="alert" className="text-xs text-destructive">
              {errorText(testConnection.error, "连接测试失败")}
            </p>
          )}
          {checks && <TestCheckList checks={checks} />}
        </div>

        <DialogFooter className="gap-2">
          <Button
            type="button"
            variant="outline"
            disabled={busy || testing || !canTest}
            title={apiKey.trim() ? undefined : "输入新密钥后可测试连接"}
            onClick={() => void handleTest()}
          >
            {testing && <Loader2 className="mr-1 h-3.5 w-3.5 animate-spin" />}
            测试连接
          </Button>
          <Button
            type="button"
            disabled={busy || testing}
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

/** 名称单元格：提供商名 + 目录名（或「自定义」）标注来源。 */
function ProviderNameCell({ provider }: { provider: AIProvider }) {
  const catalog = useAIProviderCatalog();
  const catalogName =
    provider.catalog_id === CUSTOM_ID
      ? "自定义"
      : (catalog.data?.find((p) => p.id === provider.catalog_id)?.name ??
        provider.catalog_id);
  return (
    <div className="flex items-center gap-2.5">
      <ProviderIcon
        providerInstanceId={provider.catalog_id}
        className="size-4"
      />
      <div className="min-w-0">
        <div className="truncate font-medium">{provider.name}</div>
        <div className="truncate text-xs text-muted-foreground">
          {catalogName}
        </div>
      </div>
    </div>
  );
}

export default function AIProvidersPage() {
  const { data: providers, isLoading } = useAIProviders();
  const deleteProvider = useDeleteAIProvider();

  const [createOpen, setCreateOpen] = useState(false);
  const [editing, setEditing] = useState<AIProvider | null>(null);

  const handleDelete = async (provider: AIProvider) => {
    try {
      await deleteProvider.mutateAsync(provider.id);
      toast.success("AI 提供商已删除");
    } catch (err) {
      toast.error(
        err instanceof ApiRequestError && err.status === 409
          ? err.message
          : errorText(err, "删除失败"),
      );
    }
  };

  return (
    <SettingsPageShell
      wide
      actions={
        <HeaderActions
          primary={
            <Button size="sm" onClick={() => setCreateOpen(true)}>
              <Plus className="mr-1.5 h-3.5 w-3.5" />
              添加提供商
            </Button>
          }
        />
      }
    >
      <div className="space-y-6">
        <div>
          <h2 className="text-lg font-medium">AI 提供商</h2>
          <p className="text-sm text-muted-foreground">
            统一管理 OpenAI 兼容接口的地址与密钥，机器人按引用使用；
            更新密钥后所有引用它的机器人下个运行生效。
          </p>
        </div>

        <CreateProviderDialog open={createOpen} onOpenChange={setCreateOpen} />
        <EditProviderDialog
          provider={editing}
          open={editing !== null}
          onOpenChange={(open) => {
            if (!open) setEditing(null);
          }}
        />

        {isLoading ? (
          <div className="space-y-3">
            {Array.from({ length: 3 }).map((_, i) => (
              <Skeleton key={i} className="h-12 rounded" />
            ))}
          </div>
        ) : !providers?.length ? (
          <div className="rounded-lg border bg-muted/40 py-12 text-center">
            <p className="text-sm text-muted-foreground">
              还没有配置 AI 提供商
            </p>
            <p className="mt-1 text-xs text-muted-foreground">
              点击右上角按钮添加，之后新建机器人只需选模型
            </p>
          </div>
        ) : (
          <div className="overflow-hidden rounded-lg border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>名称</TableHead>
                  <TableHead>API 地址</TableHead>
                  <TableHead>API Key</TableHead>
                  <TableHead>引用机器人</TableHead>
                  <TableHead>创建时间</TableHead>
                  <TableHead className="text-right">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {providers.map((provider) => (
                  <TableRow key={provider.id}>
                    <TableCell className="whitespace-normal">
                      <ProviderNameCell provider={provider} />
                    </TableCell>
                    <TableCell className="max-w-56 font-mono text-sm">
                      <span
                        className="block truncate"
                        title={provider.base_url}
                      >
                        {provider.base_url}
                      </span>
                    </TableCell>
                    <TableCell className="font-mono text-sm">
                      ···{provider.api_key_last4}
                    </TableCell>
                    <TableCell className="text-sm">
                      {provider.bot_count > 0 ? (
                        `${provider.bot_count} 个机器人`
                      ) : (
                        <span className="text-xs text-muted-foreground">
                          未被引用
                        </span>
                      )}
                    </TableCell>
                    <TableCell className="text-sm">
                      {formatDate(provider.created_at)}
                    </TableCell>
                    <TableCell className="text-right">
                      <div className="flex justify-end gap-1">
                        <Button
                          variant="ghost"
                          size="sm"
                          aria-label={`编辑 ${provider.name}`}
                          onClick={() => setEditing(provider)}
                        >
                          <Pencil className="h-4 w-4" />
                        </Button>
                        <AlertDialog>
                          <AlertDialogTrigger
                            render={
                              <Button
                                variant="ghost"
                                size="sm"
                                aria-label={`删除 ${provider.name}`}
                              />
                            }
                          >
                            <Trash2 className="h-4 w-4" />
                          </AlertDialogTrigger>
                          <AlertDialogContent>
                            <AlertDialogHeader>
                              <AlertDialogTitle>
                                删除 AI 提供商？
                              </AlertDialogTitle>
                              <AlertDialogDescription>
                                {provider.bot_count > 0
                                  ? `该提供商仍被 ${provider.bot_count} 个机器人引用，删除会被拒绝。请先给这些机器人换用其它提供商。`
                                  : "删除后无法恢复。"}
                              </AlertDialogDescription>
                            </AlertDialogHeader>
                            <AlertDialogFooter>
                              <AlertDialogCancel>取消</AlertDialogCancel>
                              <AlertDialogAction
                                onClick={() => void handleDelete(provider)}
                              >
                                确认删除
                              </AlertDialogAction>
                            </AlertDialogFooter>
                          </AlertDialogContent>
                        </AlertDialog>
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </div>
    </SettingsPageShell>
  );
}
