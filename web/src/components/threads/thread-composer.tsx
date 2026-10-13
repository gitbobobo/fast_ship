import { useRef, type ClipboardEvent, type FormEvent, type KeyboardEvent, type ReactNode } from "react";
import { ImagePlus, Send } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import type { ImageUploads } from "@/lib/hooks/use-image-uploads";
import { ImageStrip } from "./thread-images";

/**
 * 新建页与详情页共用的消息输入：正文 + 图片预览 + 底部工具栏。
 * 提交走表单（按钮或 Cmd/Ctrl+Enter）；回车只换行，避免输入法选词时误发。
 */
export function ThreadComposer({
  environmentId,
  text,
  onTextChange,
  uploads,
  placeholder = "输入消息…",
  submitLabel,
  submitting = false,
  blocked = false,
  allowImageOnly = true,
  disabled = false,
  onSubmit,
  banner,
  toolbar,
  actions,
  error,
}: {
  environmentId: string;
  text: string;
  onTextChange: (value: string) => void;
  uploads: ImageUploads;
  placeholder?: string;
  submitLabel: string;
  submitting?: boolean;
  /** 额外的不可提交条件（缺项目、环境只读、目标陈旧等）。 */
  blocked?: boolean;
  /** 允许只发图片（空正文）。 */
  /** 整体不可输入（只读环境）。 */
  disabled?: boolean;
  onSubmit: () => void;
  banner?: ReactNode;
  toolbar?: ReactNode;
  actions?: ReactNode;
  error?: string;
}) {
  const fileInput = useRef<HTMLInputElement>(null);
  const hasImages = uploads.images.some((i) => i.status === "done");
  const hasContent = text.trim().length > 0 || (allowImageOnly && hasImages);
  const canSubmit =
    hasContent && !uploads.uploading && !submitting && !blocked && !disabled;

  const submit = (e?: FormEvent) => {
    e?.preventDefault();
    if (canSubmit) onSubmit();
  };

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && !e.nativeEvent.isComposing) {
      submit();
    }
  };

  const onPaste = (e: ClipboardEvent<HTMLTextAreaElement>) => {
    const files = [...e.clipboardData.files].filter((f) => f.type.startsWith("image/"));
    if (files.length > 0) {
      e.preventDefault();
      uploads.add(files);
    }
  };

  return (
    <form
      onSubmit={submit}
      aria-label="消息输入"
      className="flex flex-col gap-2 rounded-2xl border bg-card p-3 shadow-xs"
    >
      {banner}
      <ImageStrip
        environmentId={environmentId}
        images={uploads.images}
        onRemove={uploads.remove}
      />
      <Textarea
        value={text}
        onChange={(e) => onTextChange(e.target.value)}
        onKeyDown={onKeyDown}
        onPaste={onPaste}
        placeholder={placeholder}
        disabled={disabled}
        aria-label="消息内容"
        className="max-h-60 min-h-20 resize-none border-0 bg-transparent px-1 shadow-none focus-visible:ring-0 dark:bg-transparent"
      />
      {error && (
        <p role="alert" className="text-xs text-destructive">
          {error}
        </p>
      )}
      <div className="flex flex-wrap items-center gap-2">
        <input
          ref={fileInput}
          type="file"
          accept="image/png,image/jpeg,image/gif,image/webp"
          multiple
          hidden
          data-testid="image-input"
          onChange={(e) => {
            uploads.add([...(e.target.files ?? [])]);
            e.target.value = "";
          }}
        />
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          aria-label="添加图片"
          disabled={disabled}
          onClick={() => fileInput.current?.click()}
        >
          <ImagePlus className="h-4 w-4" />
        </Button>
        <div className="flex min-w-0 flex-1 flex-wrap items-center gap-1.5">
          {toolbar}
        </div>
        <div className="ml-auto flex items-center gap-2">
          {actions}
          <Button type="submit" size="sm" disabled={!canSubmit}>
            <Send className="mr-1.5 h-3.5 w-3.5" />
            {submitLabel}
          </Button>
        </div>
      </div>
    </form>
  );
}
