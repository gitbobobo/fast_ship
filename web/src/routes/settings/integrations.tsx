import { useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod/v4";
import { Copy, RefreshCw } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { Textarea } from "@/components/ui/textarea";
import { GithubIcon } from "@/components/ui/github-icon";
import { HeaderActions } from "@/components/layout/header-actions";
import { SettingsPageShell } from "@/routes/settings/layout";
import {
  useGitHubAppIntegration,
  useTestGitHubAppIntegration,
  useUpdateGitHubAppIntegration,
} from "@/lib/hooks/use-integrations";
import type { UpdateGitHubAppIntegrationInput } from "@/lib/api/integrations";
import { copyWithToast } from "@/lib/copy";
import { formatDate } from "@/lib/utils/format";

const githubAppFormSchema = z.object({
  app_id: z
    .string()
    .trim()
    .min(1, "请输入 App ID")
    .regex(/^\d+$/, "App ID 必须是数字"),
  private_key_pem: z.string().optional(),
  webhook_secret: z.string().optional(),
});

type GitHubAppFormInput = z.infer<typeof githubAppFormSchema>;

// 状态徽章映射：未配置优先于检测状态（没配置谈不上检测）；已保存未验证是兜底。
function statusBadge(data: GitHubAppIntegration): {
  label: string;
  variant: "secondary" | "destructive" | "outline";
  className?: string;
} {
  if (data.source === "none") {
    return { label: "未配置", variant: "secondary" };
  }
  switch (data.detection_status) {
    case "ok":
      return {
        label: "已连接",
        variant: "secondary",
        className: "bg-emerald-500/10 text-emerald-700 dark:text-emerald-400",
      };
    case "invalid":
      return { label: "凭证无效", variant: "destructive" };
    case "failed":
      return {
        label: "检测失败",
        variant: "secondary",
        className: "bg-amber-500/15 text-amber-700 dark:text-amber-400",
      };
    default:
      return { label: "已保存未验证", variant: "outline" };
  }
}

function errorText(error: unknown, fallback: string) {
  return error instanceof Error && error.message ? error.message : fallback;
}

export default function IntegrationsPage() {
  const { data, isLoading, isError, isFetching, refetch } =
    useGitHubAppIntegration();
  const updateIntegration = useUpdateGitHubAppIntegration();
  const testIntegration = useTestGitHubAppIntegration();
  // 「清空已存密钥」是表单外状态：勾选后提交 webhook_secret: ""，与留空保留区分。
  const [clearSecret, setClearSecret] = useState(false);

  const {
    register,
    handleSubmit,
    reset,
    setError,
    formState: { errors, isSubmitting, isDirty },
  } = useForm<GitHubAppFormInput>({
    resolver: zodResolver(githubAppFormSchema),
    defaultValues: { app_id: "", private_key_pem: "", webhook_secret: "" },
  });

  // 密钥不回显：数据落地只回填 app_id，两个密钥输入框恒为空。
  // 表单有未保存改动或已勾选「清空已存密钥」时跳过——「测试连接」刷新
  // 检测状态不应冲掉正在填的私钥，也不该取消清空意图（仅保存成功或显式取消才复位）。
  useEffect(() => {
    if (!data || isDirty || clearSecret) return;
    reset({ app_id: data.app_id, private_key_pem: "", webhook_secret: "" });
  }, [data, isDirty, clearSecret, reset]);

  const onSubmit = async (values: GitHubAppFormInput) => {
    const pem = values.private_key_pem?.trim() ?? "";
    if (!data?.private_key_set && !pem) {
      setError("private_key_pem", { message: "首次配置必须提供 App 私钥" });
      return;
    }
    const secret = values.webhook_secret?.trim() ?? "";
    const payload: UpdateGitHubAppIntegrationInput = {
      app_id: values.app_id.trim(),
    };
    if (pem) payload.private_key_pem = pem;
    if (clearSecret && data?.webhook_secret_set) {
      payload.webhook_secret = "";
    } else if (secret) {
      payload.webhook_secret = secret;
    }
    try {
      const res = await updateIntegration.mutateAsync(payload);
      reset({
        app_id: res.data.app_id,
        private_key_pem: "",
        webhook_secret: "",
      });
      setClearSecret(false);
      toast.success("已保存");
    } catch (error) {
      toast.error(errorText(error, "保存失败"));
    }
  };

  const handleTest = async () => {
    try {
      const res = await testIntegration.mutateAsync();
      const result = res.data;
      if (result.detection_status === "ok") {
        toast.success(result.detection_detail || "连接正常");
      } else {
        toast.error(result.detection_detail || "检测失败");
      }
    } catch (error) {
      toast.error(errorText(error, "检测失败"));
    }
  };

  const saving = isSubmitting || updateIntegration.isPending;
  const webhookDisplay = data?.webhook_url || "/api/github/webhooks";
  const status = data ? statusBadge(data) : null;

  return (
    <SettingsPageShell
      actions={
        <HeaderActions
          primary={
            <Button
              type="submit"
              form="github-app-integration-form"
              size="sm"
              disabled={saving || isLoading}
            >
              {saving ? "保存中..." : "保存"}
            </Button>
          }
          secondary={[
            <Button
              key="test"
              variant="outline"
              size="sm"
              disabled={!data?.configured || testIntegration.isPending}
              onClick={handleTest}
            >
              <RefreshCw className="mr-1.5 h-3.5 w-3.5" />
              {testIntegration.isPending ? "检测中..." : "测试连接"}
            </Button>,
          ]}
        />
      }
    >
      <div className="space-y-6">
        <div>
          <h2 className="text-lg font-medium">集成</h2>
          <p className="text-sm text-muted-foreground">
            集中配置你自己的外部服务连接，保存后立即生效。
          </p>
        </div>

        {isLoading ? (
          <div className="space-y-3">
            <Skeleton className="h-16 rounded-xl" />
            <Skeleton className="h-72 rounded-xl" />
          </div>
        ) : isError || !data ? (
          <div
            role="alert"
            className="rounded-lg border bg-muted/40 py-12 text-center"
          >
            <p className="text-sm text-muted-foreground">集成配置加载失败</p>
            <Button
              variant="outline"
              size="sm"
              className="mt-3"
              onClick={() => refetch()}
              disabled={isFetching}
            >
              重试
            </Button>
          </div>
        ) : (
          <div className="rounded-xl border">
            <div className="flex items-start gap-3 border-b p-4">
              <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-full bg-muted text-foreground">
                <GithubIcon className="h-4 w-4" />
              </div>
              <div className="min-w-0 flex-1 space-y-1">
                <div className="flex flex-wrap items-center gap-2">
                  <p className="font-medium">GitHub App</p>
                  {status && (
                    <Badge variant={status.variant} className={status.className}>
                      {status.label}
                    </Badge>
                  )}
                </div>
                {data.detection_detail && (
                  <p className="text-xs text-muted-foreground">
                    {data.detection_detail}
                  </p>
                )}
                {data.detected_at && (
                  <p className="text-xs text-muted-foreground">
                    最近检测于 {formatDate(data.detected_at)}
                  </p>
                )}
              </div>
            </div>

            <div className="space-y-5 p-4">
              <div className="rounded-lg bg-muted/30 px-3 py-2 text-sm text-muted-foreground">
                这里的配置只属于你的账号，只影响你自己触发的自动化与 PR 评审流。
              </div>
              {data.source === "config" && (
                <div className="rounded-lg bg-muted/30 px-3 py-2 text-sm text-muted-foreground">
                  当前生效的是部署配置（config.yaml 兜底），保存后将改用你自己的配置。
                </div>
              )}
              {data.source === "none" && (
                <div className="rounded-lg bg-muted/30 px-3 py-2 text-sm text-muted-foreground">
                  尚未配置，填写后保存即可用你自己的 GitHub App 参与 PR 评审与 webhook。
                </div>
              )}

              <div className="space-y-2">
                <p className="text-sm font-medium">Webhook URL</p>
                <div className="flex items-center gap-2">
                  <code className="min-w-0 flex-1 truncate rounded-lg border bg-muted/30 px-2.5 py-1.5 font-mono text-xs">
                    {webhookDisplay}
                  </code>
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() =>
                      void copyWithToast(webhookDisplay, "已复制 Webhook URL")
                    }
                  >
                    <Copy className="mr-1.5 h-3.5 w-3.5" />
                    复制
                  </Button>
                </div>
                <p className="text-xs text-muted-foreground">
                  Webhook URL 是本部署的统一入口，把它与下面的验签密钥填进你自己的
                  GitHub App。
                </p>
                {!data.webhook_url && (
                  <p className="text-xs text-muted-foreground">
                    部署未配置 server.public_url，请自行拼接对外地址
                  </p>
                )}
              </div>

              <form
                id="github-app-integration-form"
                onSubmit={handleSubmit(onSubmit)}
                className="space-y-4"
              >
                <div className="space-y-2">
                  <Label htmlFor="github-app-id">App ID</Label>
                  <Input
                    id="github-app-id"
                    inputMode="numeric"
                    placeholder="例如 123456"
                    autoComplete="off"
                    {...register("app_id")}
                  />
                  {errors.app_id && (
                    <p className="text-xs text-destructive">
                      {errors.app_id.message}
                    </p>
                  )}
                </div>

                <div className="space-y-2">
                  <Label htmlFor="github-app-private-key">App 私钥（PEM）</Label>
                  <Textarea
                    id="github-app-private-key"
                    className="font-mono text-xs"
                    rows={6}
                    placeholder="-----BEGIN RSA PRIVATE KEY-----"
                    autoComplete="off"
                    spellCheck={false}
                    {...register("private_key_pem")}
                  />
                  {errors.private_key_pem ? (
                    <p className="text-xs text-destructive">
                      {errors.private_key_pem.message}
                    </p>
                  ) : (
                    <p className="text-xs text-muted-foreground">
                      {data.private_key_set
                        ? "已配置，留空则保持不变"
                        : "首次配置必填；仅在后端加密存储，不会回显到网页"}
                    </p>
                  )}
                </div>

                <div className="space-y-2">
                  <Label htmlFor="github-app-webhook-secret">Webhook 密钥</Label>
                  <Input
                    id="github-app-webhook-secret"
                    type="password"
                    autoComplete="off"
                    disabled={clearSecret}
                    {...register("webhook_secret")}
                  />
                  <p className="text-xs text-muted-foreground">
                    {data.webhook_secret_set
                      ? "已配置，留空则保持不变"
                      : "与 GitHub App 设置中的 Webhook secret 一致，用于校验事件来源"}
                  </p>
                  {data.webhook_secret_set && (
                    <Label className="flex items-center gap-2 text-sm font-normal">
                      <input
                        type="checkbox"
                        checked={clearSecret}
                        onChange={(event) =>
                          setClearSecret(event.target.checked)
                        }
                        className="h-4 w-4 rounded border-input accent-primary"
                      />
                      清空已存密钥
                    </Label>
                  )}
                </div>
              </form>
            </div>
          </div>
        )}

        <div className="rounded-xl border bg-muted/30 p-4">
          <p className="text-sm font-medium">接入步骤</p>
          <ol className="mt-2 list-decimal space-y-1 pl-5 text-sm text-muted-foreground">
            <li>在 GitHub 组织设置（Developer settings → GitHub Apps）创建 App</li>
            <li>填入上方 Webhook URL 与验签密钥，订阅 pull_request 事件</li>
            <li>把 App ID 与生成的私钥（PEM）粘贴到此处保存</li>
          </ol>
        </div>
      </div>
    </SettingsPageShell>
  );
}
