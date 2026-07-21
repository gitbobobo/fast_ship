import { useEffect, useEffectEvent, useRef, useState } from "react";
import { Controller, useForm, useWatch } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { Loader2, Sparkles } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { MarkdownEditor } from "@/components/ui/markdown-editor";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
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
import { useAISettings, useGenerateTitle } from "@/lib/hooks/use-ai";

export const internalIssueFormSchema = z.object({
  title: z.string().trim().min(1, "请输入标题"),
  body: z.string(),
  workflow_status: z.enum(["", "todo", "in_progress", "done"]),
  source: z.enum(["internal", "github"]),
});

export type InternalIssueFormInput = z.infer<typeof internalIssueFormSchema>;

interface InternalIssueFormProps {
  defaultValues?: Partial<InternalIssueFormInput>;
  formId?: string;
  hideSubmitButton?: boolean;
  isSubmitting?: boolean;
  onBusyChange?: (busy: boolean) => void;
  onPasteImage?: (file: File) => Promise<string>;
  onSubmit: (values: InternalIssueFormInput) => Promise<void> | void;
  showWorkflowStatus?: boolean;
  showSourceSelector?: boolean;
  submitLabel: string;
  editorRows?: number;
  projectName?: string;
}

export function InternalIssueForm({
  defaultValues,
  formId,
  hideSubmitButton = false,
  isSubmitting = false,
  onBusyChange,
  onPasteImage,
  onSubmit,
  showWorkflowStatus = false,
  showSourceSelector = false,
  submitLabel,
  editorRows = 24,
  projectName,
}: InternalIssueFormProps) {
  const {
    control,
    register,
    handleSubmit,
    getValues,
    setValue,
    reset,
    formState: { errors },
  } = useForm<InternalIssueFormInput>({
    resolver: zodResolver(internalIssueFormSchema),
    defaultValues: {
      title: defaultValues?.title ?? "",
      body: defaultValues?.body ?? "",
      workflow_status: defaultValues?.workflow_status ?? "",
      source: defaultValues?.source ?? "internal",
    },
  });

  const source = useWatch({ control, name: "source" });
  const body = useWatch({ control, name: "body" });

  const { data: aiSettings } = useAISettings();
  const generateTitleMutation = useGenerateTitle();
  const canGenerateTitle = aiSettings?.configured && [...(body ?? "")].length >= 10;
  const [suggestedTitles, setSuggestedTitles] = useState<string[]>([]);

  const generateTitleTooltip = !aiSettings?.configured
    ? "请先在设置中配置 AI"
    : !canGenerateTitle
      ? "请先填写正文内容（至少 10 个字符）"
      : "AI 生成标题";

  const handleGenerateTitle = () => {
    const currentBody = getValues("body");
    generateTitleMutation.mutate(currentBody, {
      onSuccess: (data) => {
        setValue("title", data.titles[0], { shouldDirty: true });
        setSuggestedTitles(data.titles);
      },
      onError: () => {
        toast.error("生成标题失败，请稍后重试");
      },
    });
  };

  useEffect(() => {
    reset({
      title: defaultValues?.title ?? "",
      body: defaultValues?.body ?? "",
      workflow_status: defaultValues?.workflow_status ?? "",
      source: defaultValues?.source ?? "internal",
    });
  }, [defaultValues?.body, defaultValues?.title, defaultValues?.workflow_status, defaultValues?.source, reset]);

  const pendingUploadPromisesRef = useRef(new Set<Promise<string>>());
  const [pendingUploadCount, setPendingUploadCount] = useState(0);
  const [uploadErrorCount, setUploadErrorCount] = useState(0);
  const [submitRequested, setSubmitRequested] = useState<{
    uploadErrorCount: number;
  } | null>(null);
  const isBusy = isSubmitting || pendingUploadCount > 0;

  const notifyBusyChange = useEffectEvent((busy: boolean) => {
    onBusyChange?.(busy);
  });

  useEffect(() => {
    notifyBusyChange(isBusy);
  }, [isBusy]);

  const handlePasteImage = onPasteImage
    ? (file: File) => {
        const uploadPromise = onPasteImage(file);
        pendingUploadPromisesRef.current.add(uploadPromise);
        setPendingUploadCount((count) => count + 1);

        void uploadPromise.catch(() => {
          setUploadErrorCount((count) => count + 1);
        });

        void uploadPromise.finally(() => {
          pendingUploadPromisesRef.current.delete(uploadPromise);
          setPendingUploadCount((count) => Math.max(0, count - 1));
        });

        return uploadPromise;
      }
    : undefined;

  const waitForPendingUploads = async () => {
    while (pendingUploadPromisesRef.current.size > 0) {
      await Promise.all(Array.from(pendingUploadPromisesRef.current));
    }
  };

  const submitLatestValues = useEffectEvent(async () => {
    await onSubmit(getValues());
  });

  const handleFormSubmit = handleSubmit(() => {
    setSubmitRequested({ uploadErrorCount });
  });

  useEffect(() => {
    if (!submitRequested || pendingUploadCount > 0) {
      return;
    }

    if (uploadErrorCount > submitRequested.uploadErrorCount) {
      setSubmitRequested(null);
      return;
    }

    let cancelled = false;

    void (async () => {
      try {
        await waitForPendingUploads();
      } catch {
        return;
      }

      if (cancelled) {
        return;
      }

      await submitLatestValues();
      if (!cancelled) {
        setSubmitRequested(null);
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [pendingUploadCount, submitRequested, uploadErrorCount]);

  return (
    <form id={formId} className="space-y-6" onSubmit={handleFormSubmit}>
      {!hideSubmitButton && (
        <div className="flex items-center justify-end">
          <Button type="submit" disabled={isBusy} data-testid="issue-form-submit">
            {isBusy ? "保存中..." : submitLabel}
          </Button>
        </div>
      )}

      {projectName && (
        <div className="space-y-2">
          <Label htmlFor="issue-project-name">项目</Label>
          <Input
            id="issue-project-name"
            value={projectName}
            readOnly
            disabled
            className="bg-muted"
          />
        </div>
      )}

      <div className="grid grid-cols-1 gap-4 md:grid-cols-[1fr_180px]">
        <div className="space-y-2">
          <Label htmlFor="issue-title">标题</Label>
          <div className="flex gap-2">
            <Input
              id="issue-title"
              placeholder="例如：设置页在切换主题后闪退"
              className="flex-1"
              {...register("title")}
            />
            <Button
              type="button"
              variant="ghost"
              size="icon"
              className="shrink-0 text-muted-foreground hover:text-foreground disabled:opacity-30"
              disabled={!canGenerateTitle || generateTitleMutation.isPending}
              title={generateTitleTooltip}
              aria-label={generateTitleTooltip}
              onClick={handleGenerateTitle}
            >
              {generateTitleMutation.isPending ? (
                <Loader2 className="h-4 w-4 animate-spin" />
              ) : (
                <Sparkles className="h-4 w-4" />
              )}
            </Button>
          </div>
          {suggestedTitles.length > 0 && (
            <div className="flex flex-wrap items-center gap-1.5">
              <Sparkles className="h-3 w-3 shrink-0 text-muted-foreground" />
              <span className="shrink-0 text-xs text-muted-foreground">AI 推荐</span>
              {suggestedTitles.map((suggestedTitle) => (
                <button
                  key={suggestedTitle}
                  type="button"
                  className="inline-flex items-center rounded-full border border-border bg-background px-2.5 py-0.5 text-xs text-foreground transition-colors hover:bg-accent hover:text-accent-foreground"
                  onClick={() => setValue("title", suggestedTitle, { shouldDirty: true })}
                >
                  {suggestedTitle}
                </button>
              ))}
            </div>
          )}
          {errors.title && (
            <p className="text-xs text-destructive">{errors.title.message}</p>
          )}
        </div>

        {showWorkflowStatus && source === "internal" && (
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

      {showSourceSelector && (
        <div className="space-y-2">
          <Label>来源</Label>
          <Controller
            control={control}
            name="source"
            render={({ field }) => (
              <RadioGroup
                value={field.value}
                onValueChange={(value) => field.onChange(value as "internal" | "github")}
                className="flex-row gap-4"
              >
                <RadioGroupItem value="internal">内部问题</RadioGroupItem>
                <RadioGroupItem value="github">GitHub 问题</RadioGroupItem>
              </RadioGroup>
            )}
          />
        </div>
      )}

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
              onPasteImage={handlePasteImage}
              placeholder="支持 Markdown，可写复现步骤、验收标准、上下文或补充链接。"
              rows={editorRows}
            />
          )}
        />
      </div>
    </form>
  );
}
