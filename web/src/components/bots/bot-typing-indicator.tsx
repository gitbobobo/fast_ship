/**
 * 机器人回复中的等待指示：三个依次起伏的点，贴在聊天内容最底部。
 * 原来的长句提示（「新消息将在安全点交给机器人」）留在顶部运行栏，
 * 这里只表示「还在回复」，不重复文字说明。
 */
export function BotTypingIndicator() {
  return (
    <div
      data-testid="bot-typing-indicator"
      role="status"
      aria-label="机器人正在回复"
      className="flex items-center gap-1 px-1 py-1"
    >
      {[0, 1, 2].map((index) => (
        <span
          key={index}
          aria-hidden
          className="size-1.5 animate-bounce rounded-full bg-muted-foreground/60 motion-reduce:animate-none"
          style={{ animationDelay: `${index * 140}ms` }}
        />
      ))}
    </div>
  );
}
