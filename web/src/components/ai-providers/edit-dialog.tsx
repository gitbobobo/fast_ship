import { useState } from "react";
import { Loader2 } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { errorText } from "@/lib/api/client";
import {
  AI_PROVIDER_CUSTOM_ID,
  type AIProvider,
  type AIProviderTestCheck,
} from "@/lib/api/ai-providers";
import {
  useAIProviderCatalog,
  useUpdateAIProvider,
  useTestAIProvider,
} from "@/lib/hooks/use-ai-providers";
import { ConnectionChecks } from "./connection-checks";

/**
 * 编辑提供商：名称、custom 的地址与轮换密钥。catalog_id 与目录提供商的
 * base_url 不可改；填了新密钥才允许测试连接（密钥不回填，没法测已存值）。
 * 检查结果与被测目标绑定：目标字段再变，旧结果即隐藏。
 */
export function EditProviderDialog({
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
  const isCustom = provider.catalog_id === AI_PROVIDER_CUSTOM_ID;

  const [name, setName] = useState(provider.name);
  const [baseUrl, setBaseUrl] = useState(provider.base_url);
  const [model, setModel] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [tested, setTested] = useState<{
    targetKey: string;
    checks: AIProviderTestCheck[];
  } | null>(null);

  const busy = update.isPending;
  const testing = testConnection.isPending;
  // 密钥不回填：已存密钥不在手里，只有填了新密钥才能跑测试连接。
  const canTest =
    apiKey.trim().length > 0 && (!isCustom || model.trim().length > 0);

  // 被测目标指纹：catalog_id 固定，其余取自当前表单；任一字段再改旧结果失效。
  const testTargetKey = JSON.stringify([
    provider.catalog_id,
    isCustom ? baseUrl.trim() : "",
    isCustom ? model.trim() : "",
    apiKey.trim(),
  ]);
  const currentChecks = tested?.targetKey === testTargetKey ? tested.checks : null;

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
    setTested(null);
    try {
      const result = await testConnection.mutateAsync({
        catalog_id: provider.catalog_id,
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
