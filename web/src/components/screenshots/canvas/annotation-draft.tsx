import { useState } from "react";
import { Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";

export const ANNOTATION_BODY_MAX = 1000;

/** 框选松手后浮在矩形旁的输入框：回车提交，Esc 取消 */
export function AnnotationDraft({
  submitting,
  onSubmit,
  onCancel,
}: {
  submitting: boolean;
  onSubmit: (body: string) => void;
  onCancel: () => void;
}) {
  const [body, setBody] = useState("");
  const trimmed = body.trim();

  const submit = () => {
    if (!trimmed || submitting) return;
    onSubmit(trimmed);
  };

  return (
    <div
      data-no-pan
      data-card-action
      className="w-64 space-y-2 rounded-lg border bg-popover p-2 text-popover-foreground shadow-lg"
      onClick={(e) => e.stopPropagation()}
    >
      <Textarea
        autoFocus
        value={body}
        maxLength={ANNOTATION_BODY_MAX}
        disabled={submitting}
        placeholder="写下标注内容，回车提交"
        aria-label="标注内容"
        className="min-h-16 resize-none text-sm"
        onChange={(e) => setBody(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Escape") {
            e.preventDefault();
            e.stopPropagation();
            onCancel();
          } else if (
            e.key === "Enter" &&
            !e.shiftKey &&
            !e.nativeEvent.isComposing
          ) {
            e.preventDefault();
            submit();
          }
        }}
      />
      <div className="flex justify-end gap-1.5">
        <Button
          size="sm"
          variant="ghost"
          disabled={submitting}
          onClick={onCancel}
        >
          取消
        </Button>
        <Button size="sm" disabled={!trimmed || submitting} onClick={submit}>
          {submitting && <Loader2 className="mr-1 h-3.5 w-3.5 animate-spin" />}
          添加
        </Button>
      </div>
    </div>
  );
}
