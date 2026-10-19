import { useEffect } from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod/v4";
import { Globe } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { HeaderActions } from "@/components/layout/header-actions";
import { SettingsPageShell } from "@/routes/settings/layout";
import {
  useDeploymentSettings,
  useUpdateDeploymentSettings,
} from "@/lib/hooks/use-deployment";
import { useAuthStore } from "@/lib/store/auth-store";
import { formatDate } from "@/lib/utils/format";

// 与服务端同一口径：http(s) 绝对 URL、host 非空、不带 query/fragment；
// 空串合法（清空数据库值、回落部署配置）。末尾 / 提交前剥掉。
const deploymentSettingsSchema = z.object({
  public_url: z
    .string()
    .trim()
    .refine((value) => {
      if (!value) return true;
      try {
        const url = new URL(value);
        return (
          (url.protocol === "http:" || url.protocol === "https:") &&
          Boolean(url.host) &&
          !url.search &&
          !url.hash
        );
      } catch {
        return false;
      }
    }, "必须是 http(s) 绝对地址，不能带参数或锚点"),
});

type DeploymentSettingsInput = z.infer<typeof deploymentSettingsSchema>;

const sourceMeta: Record<
  DeploymentSource,
  { label: string; variant: "default" | "secondary" | "outline" }
> = {
  database: { label: "已保存到数据库", variant: "default" },
  config: { label: "来自部署配置", variant: "outline" },
  none: { label: "未配置", variant: "secondary" },
};

function errorText(error: unknown, fallback: string) {
  return error instanceof Error && error.message ? error.message : fallback;
}

function DeploymentSettingsForm() {
  const { data, isLoading, isError, isFetching, refetch } =
    useDeploymentSettings();
  const updateSettings = useUpdateDeploymentSettings();

  const {
    register,
    handleSubmit,
    reset,
    setValue,
    formState: { errors, isSubmitting, isDirty },
  } = useForm<DeploymentSettingsInput>({
    resolver: zodResolver(deploymentSettingsSchema),
    defaultValues: { public_url: "" },
  });

  // 数据落地后回填；表单有未保存改动时不覆盖用户正在输入的值。
  useEffect(() => {
    if (!data || isDirty) return;
    reset({ public_url: data.public_url });
  }, [data, isDirty, reset]);

  const onSubmit = async (values: DeploymentSettingsInput) => {
    const publicUrl = values.public_url.trim().replace(/\/+$/, "");
    try {
      const res = await updateSettings.mutateAsync({ public_url: publicUrl });
      reset({ public_url: res.data.public_url });
      toast.success("已保存");
    } catch (error) {
      toast.error(errorText(error, "保存失败"));
    }
  };

  const saving = isSubmitting || updateSettings.isPending;
  const source = data ? sourceMeta[data.public_url_source] : null;

  return (
    <SettingsPageShell
      actions={
        <HeaderActions
          primary={
            <Button
              type="submit"
              form="deployment-settings-form"
              size="sm"
              disabled={saving || isLoading || isError}
            >
              {saving ? "保存中..." : "保存"}
            </Button>
          }
        />
      }
    >
      <div className="space-y-6">
        <div>
          <h2 className="text-lg font-medium">部署</h2>
          <p className="text-sm text-muted-foreground">
            部署级设置，全站生效；仅管理员可修改。
          </p>
        </div>

        {isLoading ? (
          <div className="space-y-3">
            <Skeleton className="h-16 rounded-xl" />
            <Skeleton className="h-40 rounded-xl" />
          </div>
        ) : isError || !data ? (
          <div
            role="alert"
            className="rounded-lg border bg-muted/40 py-12 text-center"
          >
            <p className="text-sm text-muted-foreground">部署设置加载失败</p>
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
                <Globe className="h-4 w-4" />
              </div>
              <div className="min-w-0 flex-1 space-y-1">
                <div className="flex flex-wrap items-center gap-2">
                  <p className="font-medium">对外访问地址（public_url）</p>
                  {source && (
                    <Badge variant={source.variant}>{source.label}</Badge>
                  )}
                </div>
                {data.public_url_source === "database" && data.updated_at && (
                  <p className="text-xs text-muted-foreground">
                    最近保存于 {formatDate(data.updated_at)}
                  </p>
                )}
              </div>
            </div>

            <form
              id="deployment-settings-form"
              onSubmit={handleSubmit(onSubmit)}
              className="space-y-4 p-4"
            >
              <div className="space-y-2">
                <Label htmlFor="deployment-public-url">public_url</Label>
                <div className="flex items-center gap-2">
                  <Input
                    id="deployment-public-url"
                    className="flex-1"
                    placeholder={window.location.origin}
                    autoComplete="off"
                    autoCorrect="off"
                    autoCapitalize="none"
                    spellCheck={false}
                    {...register("public_url")}
                  />
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={() =>
                      setValue("public_url", window.location.origin, {
                        shouldDirty: true,
                      })
                    }
                  >
                    使用当前地址
                  </Button>
                </div>
                {errors.public_url ? (
                  <p className="text-xs text-destructive">
                    {errors.public_url.message}
                  </p>
                ) : (
                  <p className="text-xs text-muted-foreground">
                    邀请链接、GitHub Webhook 与回调地址都基于该地址对外提供；
                    留空保存则回落部署配置（config.yaml）。
                  </p>
                )}
              </div>
            </form>
          </div>
        )}
      </div>
    </SettingsPageShell>
  );
}

export default function DeploymentPage() {
  const isAdmin = useAuthStore((state) => state.user?.role === "admin");

  if (!isAdmin) {
    return (
      <SettingsPageShell>
        <div className="space-y-6">
          <div>
            <h2 className="text-lg font-medium">部署</h2>
          </div>
          <div className="rounded-lg border bg-muted/40 py-12 text-center">
            <p className="text-sm text-muted-foreground">
              需要管理员权限，请联系管理员调整部署设置。
            </p>
          </div>
        </div>
      </SettingsPageShell>
    );
  }

  return <DeploymentSettingsForm />;
}
