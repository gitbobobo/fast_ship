import { useEffect } from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod/v4";
import { Sparkles, ShieldCheck } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { useAISettings, useUpdateAISettings } from "@/lib/hooks/use-ai";
import { formatDate } from "@/lib/utils/format";
import { toast } from "sonner";

const aiSettingsSchema = z.object({
  api_host: z.url("请输入有效的 API Host"),
  model: z.string().min(1, "请输入模型名称"),
  api_key: z.string().optional(),
});

type AISettingsInput = z.infer<typeof aiSettingsSchema>;

export default function AISettingsPage() {
  const { data, isLoading } = useAISettings();
  const updateSettings = useUpdateAISettings();

  const {
    register,
    handleSubmit,
    reset,
    formState: { errors, isSubmitting },
  } = useForm<AISettingsInput>({
    resolver: zodResolver(aiSettingsSchema),
    defaultValues: {
      api_host: "https://api.minimaxi.com",
      model: "MiniMax-M2.5",
      api_key: "",
    },
  });

  useEffect(() => {
    if (!data) {
      return;
    }
    reset({
      api_host: data.api_host,
      model: data.model,
      api_key: "",
    });
  }, [data, reset]);

  const onSubmit = async (values: AISettingsInput) => {
    try {
      await updateSettings.mutateAsync({
        api_host: values.api_host.trim(),
        model: values.model.trim(),
        api_key: values.api_key?.trim() || undefined,
      });
      reset({
        api_host: values.api_host.trim(),
        model: values.model.trim(),
        api_key: "",
      });
      toast.success("MiniMax 配置已保存");
    } catch {
      toast.error("保存 MiniMax 配置失败");
    }
  };

  if (isLoading) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-16 rounded-xl" />
        <Skeleton className="h-72 rounded-xl" />
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-lg font-medium">AI 配置</h2>
        <p className="text-sm text-muted-foreground">
          配置 MiniMax 接口，用于问题详情页的智能识别建议。
        </p>
      </div>

      <div className="rounded-xl border bg-muted/30 p-4">
        <div className="flex items-start gap-3">
          <div className="mt-0.5 flex h-9 w-9 items-center justify-center rounded-full bg-background text-foreground shadow-sm ring-1 ring-border">
            {data?.configured ? (
              <ShieldCheck className="h-4 w-4 text-emerald-600" />
            ) : (
              <Sparkles className="h-4 w-4 text-muted-foreground" />
            )}
          </div>
          <div className="space-y-1">
            <p className="font-medium">
              {data?.configured ? "MiniMax 已配置" : "尚未保存 MiniMax API Key"}
            </p>
            <p className="text-sm text-muted-foreground">
              {data?.configured
                ? `当前模型 ${data.model}${data.updated_at ? `，最近更新于 ${formatDate(data.updated_at)}` : ""}`
                : "保存后，问题详情页会自动把标题、正文和评论发送给 AI 生成任务清单建议。"}
            </p>
          </div>
        </div>
      </div>

      <form onSubmit={handleSubmit(onSubmit)} className="space-y-4 rounded-xl border p-4">
        <div className="space-y-2">
          <Label htmlFor="api_host">API Host</Label>
          <Input
            id="api_host"
            placeholder="https://api.minimaxi.com"
            {...register("api_host")}
          />
          {errors.api_host && (
            <p className="text-xs text-destructive">{errors.api_host.message}</p>
          )}
        </div>

        <div className="space-y-2">
          <Label htmlFor="model">模型</Label>
          <Input id="model" placeholder="MiniMax-M2.5" {...register("model")} />
          {errors.model && (
            <p className="text-xs text-destructive">{errors.model.message}</p>
          )}
        </div>

        <div className="space-y-2">
          <Label htmlFor="api_key">API Key</Label>
          <Input
            id="api_key"
            type="password"
            placeholder={data?.configured ? "留空表示保持当前已保存 Key" : "sk-api-..."}
            autoComplete="off"
            {...register("api_key")}
          />
          <p className="text-xs text-muted-foreground">
            API Key 仅在后端加密存储，不会回显到网页。
          </p>
        </div>

        <Button type="submit" disabled={isSubmitting || updateSettings.isPending}>
          {isSubmitting || updateSettings.isPending ? "保存中..." : "保存配置"}
        </Button>
      </form>
    </div>
  );
}
