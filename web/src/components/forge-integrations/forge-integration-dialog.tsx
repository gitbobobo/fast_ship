import { useState } from "react";
import { useForm, useWatch } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod/v4";
import { Loader2 } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
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
  useCreateForgeIntegration,
  useUpdateForgeIntegration,
} from "@/lib/hooks/use-forge-integrations";

const forgeFormSchema = z.object({
  name: z.string().trim().min(1, "请输入名称"),
  base_url: z
    .string()
    .trim()
    .min(1, "请输入 Forgejo 实例地址")
    .regex(/^https?:\/\/\S+$/, "地址需以 http:// 或 https:// 开头"),
  api_base_url: z.string(),
  token: z.string(),
  webhook_secret: z.string(),
  ca_cert_pem: z.string(),
  allow_private_network: z.boolean(),
});

type ForgeFormInput = z.infer<typeof forgeFormSchema>;

/**
 * 新建（integration 为 null）与编辑共用一份表单。凭证只写不回显：编辑时
 * token/webhook_secret 留空=保留，CA 走「清空已存证书」勾选，api_base_url
 * 空串提交=重置为默认推导，对应契约三态语义。
 */
export function ForgeIntegrationDialog({
  integration,
  open,
  onOpenChange,
}: {
  integration: ForgeIntegration | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  if (!open) return null;
  return (
    <ForgeIntegrationDialogInner
      key={integration?.id ?? "create"}
      integration={integration}
      onClose={() => onOpenChange(false)}
    />
  );
}

function ForgeIntegrationDialogInner({
  integration,
  onClose,
}: {
  integration: ForgeIntegration | null;
  onClose: () => void;
}) {
  const isEdit = integration !== null;
  const createIntegration = useCreateForgeIntegration();
  const updateIntegration = useUpdateForgeIntegration();
  // 「清空已存证书」是表单外状态：勾选后提交 ca_cert_pem: ""，与留空保留区分。
  const [clearCaCert, setClearCaCert] = useState(false);

  // api_base_url 推导态不预填展开值——展开值留在占位符里跟随 base_url 动态
  // 展示，预填会让「跟随推导」与「显式钉死」同形，管理员分不清当前语义。
  // 钉死态预填钉死值，原样回发被下面的提交守卫收成 null=保留。
  const apiBasePrefill = integration?.api_base_url_derived
    ? ""
    : (integration?.api_base_url ?? "");

  const {
    register,
    handleSubmit,
    control,
    setValue,
    setError,
    formState: { errors, isSubmitting },
  } = useForm<ForgeFormInput>({
    resolver: zodResolver(forgeFormSchema),
    defaultValues: {
      name: integration?.name ?? "",
      base_url: integration?.base_url ?? "",
      api_base_url: apiBasePrefill,
      token: "",
      webhook_secret: "",
      ca_cert_pem: "",
      allow_private_network: integration?.allow_private_network ?? false,
    },
  });

  // useWatch 而非 watch()：后者是 React Compiler 不兼容的 API（lint 禁用）。
  const baseUrl = (useWatch({ control, name: "base_url" }) ?? "").trim();
  const allowPrivateNetwork = useWatch({
    control,
    name: "allow_private_network",
  });
  const derivedApiBase = `${baseUrl || "https://forgejo.example.com"}/api/v1`;

  const buildCreatePayload = (
    values: ForgeFormInput,
  ): CreateForgeIntegrationRequest => {
    const payload: CreateForgeIntegrationRequest = {
      name: values.name.trim(),
      base_url: values.base_url.trim(),
      token: values.token.trim(),
      allow_private_network: values.allow_private_network,
    };
    if (values.api_base_url.trim())
      payload.api_base_url = values.api_base_url.trim();
    if (values.webhook_secret.trim())
      payload.webhook_secret = values.webhook_secret.trim();
    if (values.ca_cert_pem.trim())
      payload.ca_cert_pem = values.ca_cert_pem.trim();
    return payload;
  };

  const buildUpdatePayload = (
    values: ForgeFormInput,
  ): UpdateForgeIntegrationRequest => {
    const payload: UpdateForgeIntegrationRequest = {
      name: values.name.trim(),
      base_url: values.base_url.trim(),
      allow_private_network: values.allow_private_network,
    };
    // api_base_url 只在字段值真的变了才提交（空串 = 重置为 {base_url}/api/v1
    // 推导值）：预填值原样回发会让服务端误以为管理员显式钉死了它，改
    // base_url 后推导值无法跟随新实例。比较基准是预填值不是响应展开值：
    // 推导态预填空串，留空提交本就表示保持推导。
    const apiBaseUrl = values.api_base_url.trim();
    if (apiBaseUrl !== apiBasePrefill) {
      payload.api_base_url = apiBaseUrl;
    }
    // 凭证留空 = 保留旧值（契约：省略即保留），只在填了新值时提交。
    const token = values.token.trim();
    if (token) payload.token = token;
    const secret = values.webhook_secret.trim();
    if (secret) payload.webhook_secret = secret;
    if (clearCaCert) {
      payload.ca_cert_pem = "";
    } else {
      const caCert = values.ca_cert_pem.trim();
      if (caCert) payload.ca_cert_pem = caCert;
    }
    return payload;
  };

  const onSubmit = async (values: ForgeFormInput) => {
    if (!isEdit && !values.token.trim()) {
      setError("token", { message: "请输入访问令牌（PAT）" });
      return;
    }
    try {
      if (integration) {
        await updateIntegration.mutateAsync({
          id: integration.id,
          input: buildUpdatePayload(values),
        });
        toast.success("已保存");
      } else {
        await createIntegration.mutateAsync(buildCreatePayload(values));
        toast.success("集成已创建");
      }
      onClose();
    } catch (error) {
      toast.error(errorText(error, isEdit ? "保存失败" : "创建失败"));
    }
  };

  const saving = isSubmitting || createIntegration.isPending || updateIntegration.isPending;

  return (
    <Dialog open onOpenChange={(next) => !next && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>
            {isEdit ? `编辑 ${integration?.name}` : "新建 Forgejo 集成"}
          </DialogTitle>
          <DialogDescription>
            {isEdit
              ? "更新连接信息或凭证；凭证只写不回显。"
              : "连接自建 Forgejo 实例，供项目访问仓库。"}
          </DialogDescription>
        </DialogHeader>

        <form
          onSubmit={handleSubmit(onSubmit)}
          className="space-y-4"
          data-testid="forge-integration-form"
        >
          <div className="space-y-2">
            <Label htmlFor="forge-name">名称</Label>
            <Input
              id="forge-name"
              placeholder="例如 自建 Forgejo"
              autoComplete="off"
              {...register("name")}
            />
            {errors.name && (
              <p className="text-xs text-destructive">{errors.name.message}</p>
            )}
          </div>

          <div className="space-y-2">
            <Label htmlFor="forge-base-url">实例地址</Label>
            <Input
              id="forge-base-url"
              placeholder="https://forgejo.example.com"
              autoComplete="off"
              {...register("base_url")}
            />
            {errors.base_url ? (
              <p className="text-xs text-destructive">
                {errors.base_url.message}
              </p>
            ) : (
              <p className="text-xs text-muted-foreground">
                Forgejo 网站根地址，例如 https://forgejo.example.com
              </p>
            )}
          </div>

          <div className="space-y-2">
            <div className="flex items-center justify-between">
              <Label htmlFor="forge-api-base-url">API 基址（可选）</Label>
              {isEdit && (
                <button
                  type="button"
                  className="text-xs text-muted-foreground hover:text-foreground"
                  onClick={() =>
                    setValue("api_base_url", "", { shouldDirty: true })
                  }
                >
                  重置为默认
                </button>
              )}
            </div>
            <Input
              id="forge-api-base-url"
              placeholder={derivedApiBase}
              autoComplete="off"
              {...register("api_base_url")}
            />
            <p className="text-xs text-muted-foreground">
              {isEdit
                ? integration?.api_base_url_derived
                  ? `当前跟随实例地址推导；填写则钉死该值`
                  : "当前已显式钉死；清空提交回到跟随实例地址推导"
                : `留空按 ${derivedApiBase} 推导`}
            </p>
          </div>

          <div className="space-y-2">
            <Label htmlFor="forge-token">访问令牌（PAT）</Label>
            <Input
              id="forge-token"
              type="password"
              autoComplete="off"
              placeholder={isEdit ? "••••••••" : "仓库范围的访问令牌"}
              {...register("token")}
            />
            {errors.token ? (
              <p className="text-xs text-destructive">{errors.token.message}</p>
            ) : (
              <p className="text-xs text-muted-foreground">
                {isEdit
                  ? integration?.token_set
                    ? "已设置，留空保持不变"
                    : "尚未设置"
                  : "需要仓库读取权限；仅在后端加密存储，不会回显到网页"}
              </p>
            )}
          </div>

          <div className="space-y-2">
            <Label htmlFor="forge-webhook-secret">Webhook 密钥（可选）</Label>
            <Input
              id="forge-webhook-secret"
              type="password"
              autoComplete="off"
              {...register("webhook_secret")}
            />
            <p className="text-xs text-muted-foreground">
              {isEdit && integration?.webhook_secret_set
                ? "已设置，留空保持不变"
                : "用于校验 Forgejo 事件来源"}
            </p>
          </div>

          <div className="space-y-2">
            <Label htmlFor="forge-ca-cert">自定义 CA 证书（可选）</Label>
            <Textarea
              id="forge-ca-cert"
              className="font-mono text-xs"
              rows={4}
              placeholder="-----BEGIN CERTIFICATE-----"
              autoComplete="off"
              spellCheck={false}
              disabled={clearCaCert}
              {...register("ca_cert_pem")}
            />
            {isEdit && integration?.has_ca_cert ? (
              <>
                <p className="text-xs text-muted-foreground">
                  已配置自定义 CA
                </p>
                <Label className="flex items-center gap-2 text-sm font-normal">
                  <input
                    type="checkbox"
                    checked={clearCaCert}
                    onChange={(event) => setClearCaCert(event.target.checked)}
                    className="h-4 w-4 rounded border-input accent-primary"
                  />
                  清空已存证书
                </Label>
              </>
            ) : (
              <p className="text-xs text-muted-foreground">
                自签名实例填写其 CA 证书（PEM），会追加到系统根证书池
              </p>
            )}
          </div>

          <div className="space-y-2">
            <div className="flex items-center justify-between gap-3">
              <Label>允许内网地址与 http</Label>
              {/* base-ui Switch 是 button：用 aria-label 而非 htmlFor 关联。 */}
              <Switch
                aria-label="允许内网地址与 http"
                checked={allowPrivateNetwork}
                onCheckedChange={(next) =>
                  setValue("allow_private_network", next, {
                    shouldDirty: true,
                  })
                }
              />
            </div>
            <p className="text-xs text-muted-foreground">
              放行该实例解析到内网地址；开启后实例地址也允许 http。
            </p>
          </div>

          <DialogFooter className="gap-2">
            <Button
              type="button"
              variant="outline"
              disabled={saving}
              onClick={onClose}
            >
              取消
            </Button>
            <Button type="submit" disabled={saving}>
              {saving && <Loader2 className="mr-1 h-3.5 w-3.5 animate-spin" />}
              {saving ? "保存中..." : "保存"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
