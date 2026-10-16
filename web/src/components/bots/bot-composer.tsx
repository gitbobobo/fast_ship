import { type FormEvent, type KeyboardEvent, type ReactNode } from "react";
import { ArrowUp } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";

/**
 * 机器人主线输入框：运行中不锁输入——新消息随时可发，由服务端在
 * 安全点交给机器人（提示通过 banner 呈现）。提交走表单（按钮或
 * Cmd/Ctrl+Enter）；回车只换行，避免输入法选词时误发。
 */
export function BotComposer({
  text,
  onTextChange,
  onSubmit,
  submitting,
  disabled,
  banner,
  actions,
  error,
  placeholder = "发消息给机器人…",
}: {
  text: string;
  onTextChange: (value: string) => void;
  onSubmit: () => void;
  submitting?: boolean;
  disabled?: boolean;
  banner?: ReactNode;
  actions?: ReactNode;
  error?: string;
  placeholder?: string;
}) {
  const canSubmit = text.trim().length > 0 && !submitting && !disabled;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (canSubmit) onSubmit();
  };

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && !e.nativeEvent.isComposing) {
      submit(e);
    }
  };

  return (
    <form
      onSubmit={submit}
      aria-label="机器人消息输入"
      className="flex flex-col gap-1.5 rounded-3xl border bg-card px-3.5 pt-3 pb-2.5 shadow-xs"
    >
      {banner}
      <Textarea
        value={text}
        onChange={(e) => onTextChange(e.target.value)}
        onKeyDown={onKeyDown}
        placeholder={placeholder}
        disabled={disabled}
        aria-label="机器人消息内容"
        className="max-h-60 min-h-12 resize-none border-0 bg-transparent px-1 py-1 shadow-none focus-visible:ring-0 dark:bg-transparent"
      />
      {error && (
        <p role="alert" className="text-xs text-destructive">
          {error}
        </p>
      )}
      <div className="flex items-center gap-2">
        <div className="flex min-w-0 flex-1 items-center gap-2">{actions}</div>
        <Button
          type="submit"
          size="icon"
          className="rounded-full"
          aria-label="发送"
          title="发送"
          disabled={!canSubmit}
        >
          <ArrowUp className="size-4" />
        </Button>
      </div>
    </form>
  );
}
