import { useEffect } from "react";
import { Controller, useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { MarkdownEditor } from "@/components/ui/markdown-editor";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  ISSUE_WORKFLOW_STATUS_LABELS,
  ISSUE_WORKFLOW_STATUS_OPTIONS,
} from "@/lib/issue-workflow-status";

export const internalIssueFormSchema = z.object({
  title: z.string().trim().min(1, "请输入标题"),
  body: z.string(),
  workflow_status: z.enum(["todo", "in_progress", "done"]),
});

export type InternalIssueFormInput = z.infer<typeof internalIssueFormSchema>;

interface InternalIssueFormProps {
  defaultValues?: Partial<InternalIssueFormInput>;
  isSubmitting?: boolean;
  onPasteImage?: (file: File) => Promise<string>;
  onCancel: () => void;
  onSubmit: (values: InternalIssueFormInput) => Promise<void> | void;
  showWorkflowStatus?: boolean;
  submitLabel: string;
}

export function InternalIssueForm({
  defaultValues,
  isSubmitting = false,
  onPasteImage,
  onCancel,
  onSubmit,
  showWorkflowStatus = false,
  submitLabel,
}: InternalIssueFormProps) {
  const {
    control,
    register,
    handleSubmit,
    reset,
    formState: { errors },
  } = useForm<InternalIssueFormInput>({
    resolver: zodResolver(internalIssueFormSchema),
    defaultValues: {
      title: defaultValues?.title ?? "",
      body: defaultValues?.body ?? "",
      workflow_status: defaultValues?.workflow_status ?? "todo",
    },
  });

  useEffect(() => {
    reset({
      title: defaultValues?.title ?? "",
      body: defaultValues?.body ?? "",
      workflow_status: defaultValues?.workflow_status ?? "todo",
    });
  }, [defaultValues?.body, defaultValues?.title, defaultValues?.workflow_status, reset]);

  return (
    <form className="space-y-6" onSubmit={handleSubmit(async (values) => onSubmit(values))}>
      <div className="grid grid-cols-1 gap-4 md:grid-cols-[1fr_180px]">
        <div className="space-y-2">
          <Label htmlFor="issue-title">标题</Label>
          <Input
            id="issue-title"
            placeholder="例如：设置页在切换主题后闪退"
            {...register("title")}
          />
          {errors.title && (
            <p className="text-xs text-destructive">{errors.title.message}</p>
          )}
        </div>

        {showWorkflowStatus && (
          <div className="space-y-2">
            <Label htmlFor="issue-workflow-status">内部状态</Label>
            <Controller
              control={control}
              name="workflow_status"
              render={({ field }) => (
                <Select value={field.value} onValueChange={field.onChange}>
                  <SelectTrigger id="issue-workflow-status" className="w-full">
                    <SelectValue>{ISSUE_WORKFLOW_STATUS_LABELS[field.value]}</SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    {ISSUE_WORKFLOW_STATUS_OPTIONS.map((option) => (
                      <SelectItem key={option.value} value={option.value}>
                        {option.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
            />
          </div>
        )}
      </div>

      <div className="space-y-2">
        <Label htmlFor="issue-body">描述</Label>
        <Controller
          control={control}
          name="body"
          render={({ field }) => (
            <MarkdownEditor
              id="issue-body"
              value={field.value}
              onChange={field.onChange}
              onPasteImage={onPasteImage}
              placeholder="支持 Markdown，可写复现步骤、验收标准、上下文或补充链接。"
              rows={16}
            />
          )}
        />
      </div>

      <div className="flex items-center justify-end gap-3 pt-2">
        <Button type="button" variant="outline" onClick={onCancel}>
          取消
        </Button>
        <Button type="submit" disabled={isSubmitting}>
          {isSubmitting ? "保存中..." : submitLabel}
        </Button>
      </div>
    </form>
  );
}
