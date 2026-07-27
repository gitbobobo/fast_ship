import { useEffect } from "react";
import { useForm, useFieldArray } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod/v4";
import { Plus, Trash, ChevronUpIcon, ChevronDownIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Skeleton } from "@/components/ui/skeleton";
import { HeaderActions } from "@/components/layout/header-actions";
import { SettingsPageShell } from "@/routes/settings/layout";
import {
  useIssuePrompts,
  useUpdateIssuePrompts,
} from "@/lib/hooks/use-issue-prompt";
import {
  DEFAULT_ISSUE_PROMPT_CONTENT,
  normalizeIssuePrompts,
} from "@/lib/issue-prompt";
import { toast } from "sonner";

const issuePromptSchema = z.object({
  prompts: z
    .array(
      z.object({
        id: z.string().min(1),
        name: z.string().min(1, "请输入名称"),
        content: z.string().min(1, "请输入正文"),
      }),
    )
    .min(1, "至少保留 1 条"),
});

type IssuePromptFormValues = z.infer<typeof issuePromptSchema>;

const newPromptId = () =>
  globalThis.crypto?.randomUUID?.() ??
  `prompt-${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;

export default function SettingsIssuePromptsPage() {
  const { data, isLoading, isError } = useIssuePrompts();
  const updatePrompts = useUpdateIssuePrompts();

  const {
    register,
    control,
    reset,
    handleSubmit,
    formState: { errors, isSubmitting },
  } = useForm<IssuePromptFormValues>({
    resolver: zodResolver(issuePromptSchema),
    defaultValues: {
      prompts: normalizeIssuePrompts(undefined),
    },
  });

  const { fields, append, remove, swap } = useFieldArray({
    control,
    name: "prompts",
  });

  useEffect(() => {
    if (isLoading) return;
    if (!data) return;
    reset({
      prompts: normalizeIssuePrompts(data.prompts).map((p) => ({ ...p })),
    });
  }, [data, isLoading, reset]);

  const onSubmit = async (values: IssuePromptFormValues) => {
    try {
      await updatePrompts.mutateAsync(values.prompts);
      toast.success("问题提示词已保存");
    } catch {
      toast.error("保存问题提示词失败");
    }
  };

  if (isLoading) {
    return (
      <SettingsPageShell>
        <div className="space-y-4">
          <Skeleton className="h-16 rounded-xl" />
          <Skeleton className="h-72 rounded-xl" />
        </div>
      </SettingsPageShell>
    );
  }

  if (isError) {
    return (
      <SettingsPageShell>
        <div className="space-y-6">
          <div>
            <h2 className="text-lg font-medium">问题提示词</h2>
            <p className="text-sm text-muted-foreground">
              加载失败，请稍后刷新重试。
            </p>
          </div>
        </div>
      </SettingsPageShell>
    );
  }

  const busy = isSubmitting || updatePrompts.isPending;

  return (
    <SettingsPageShell
      actions={
        <HeaderActions
          primary={
            <Button
              type="submit"
              form="issue-prompts-form"
              size="sm"
              disabled={busy}
            >
              {busy ? "保存中..." : "保存配置"}
            </Button>
          }
        />
      }
    >
      <div className="space-y-6">
        <div>
          <h2 className="text-lg font-medium">问题提示词</h2>
          <p className="text-sm text-muted-foreground">
            配置问题详情页「复制提示词」按钮使用的正文模板，可维护多条供切换。
          </p>
        </div>

        <form
          id="issue-prompts-form"
          onSubmit={handleSubmit(onSubmit)}
          className="space-y-4"
        >
          {fields.map((field, i) => {
            const isFirst = i === 0;
            const isLast = i === fields.length - 1;
            const onlyOne = fields.length === 1;
            return (
              <div key={field.id} className="space-y-3 rounded-xl border p-4">
                <div className="space-y-2">
                  <Label htmlFor={`prompts.${i}.name`}>名称</Label>
                  <Input
                    id={`prompts.${i}.name`}
                    placeholder="请输入名称"
                    {...register(`prompts.${i}.name`)}
                  />
                  {errors.prompts?.[i]?.name && (
                    <p className="text-xs text-destructive">
                      {errors.prompts[i]?.name?.message}
                    </p>
                  )}
                </div>

                <div className="space-y-2">
                  <Label htmlFor={`prompts.${i}.content`}>正文</Label>
                  <Textarea
                    id={`prompts.${i}.content`}
                    rows={6}
                    placeholder={DEFAULT_ISSUE_PROMPT_CONTENT}
                    {...register(`prompts.${i}.content`)}
                  />
                  {errors.prompts?.[i]?.content && (
                    <p className="text-xs text-destructive">
                      {errors.prompts[i]?.content?.message}
                    </p>
                  )}
                </div>

                <div className="flex flex-wrap items-center gap-2">
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    disabled={isFirst}
                    onClick={() => swap(i, i - 1)}
                  >
                    <ChevronUpIcon className="h-4 w-4" />
                    上移
                  </Button>
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    disabled={isLast}
                    onClick={() => swap(i, i + 1)}
                  >
                    <ChevronDownIcon className="h-4 w-4" />
                    下移
                  </Button>
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    disabled={onlyOne}
                    onClick={() => remove(i)}
                  >
                    <Trash className="h-4 w-4" />
                    删除
                  </Button>
                </div>
              </div>
            );
          })}

          {errors.prompts?.message && (
            <p className="text-xs text-destructive">{errors.prompts.message}</p>
          )}

          <Button
            type="button"
            variant="outline"
            onClick={() =>
              append({
                id: newPromptId(),
                name: "",
                content: DEFAULT_ISSUE_PROMPT_CONTENT,
              })
            }
          >
            <Plus className="h-4 w-4" />
            新增条目
          </Button>
        </form>
      </div>
    </SettingsPageShell>
  );
}
