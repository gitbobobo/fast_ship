import { type FormEvent, type KeyboardEvent, type ReactNode } from "react";
import { ArrowUp } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";

/**
 * 机器人主线输入框：运行中不锁输入——新消息随时可发，由服务端在
 * 安全点交给机器人（回复中的等待指示在聊天内容底部，不在输入框里）。
 * Enter 发送、Shift+Enter 换行；输入法组合中（isComposing / keyCode 229）
 * 的 Enter 不拦截，避免选词时误发。与线程输入框（thread-composer 的
 * Cmd/Ctrl+Enter 发送、回车换行）刻意不同：机器人主线是单线快问快答，按
 * 聊天软件习惯回车直发。Cmd/Ctrl+Enter 落在同一分支同样发送；表单提交
 * （发送按钮）保留。
 */
export function BotComposer({
  text,
  onTextChange,
  onSubmit,
  submitting,
  disabled,
  actions,
  error,
  placeholder = "发消息给机器人…",
}: {
  text: string;
  onTextChange: (value: string) => void;
  onSubmit: () => void;
  submitting?: boolean;
  disabled?: boolean;
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
    // Enter（含 Cmd/Ctrl 修饰）直接发送；Shift+Enter 与输入法组合中的
    // Enter 不拦截，走浏览器默认换行，避免选词时误发。
    if (
      e.key === "Enter" &&
      !e.shiftKey &&
      !(e.nativeEvent.isComposing || e.keyCode === 229)
    ) {
      e.preventDefault();
      if (canSubmit) onSubmit();
    }
  };

  return (
    <form
      onSubmit={submit}
      aria-label="机器人消息输入"
      className="flex flex-col gap-1.5 rounded-3xl border bg-card px-3.5 pt-3 pb-2.5 shadow-xs"
    >
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
