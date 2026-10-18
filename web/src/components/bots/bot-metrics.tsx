import type { BotMetrics as BotMetricsData } from "@/lib/api/bots";

/** 上下文窗口来源的中文标签；表外值原样展示（服务端可能先于界面升级）。 */
export const CONTEXT_SOURCE_LABELS: Record<string, string> = {
  manual: "部署覆盖",
  detected: "自动检测",
  known: "已知模型",
  fallback: "默认值",
};

function formatTokens(n: number): string {
  return n.toLocaleString("en-US");
}

/** 速度：>=100 取整，否则保留一位小数。 */
function formatSpeed(tokensPerSecond: number): string {
  return tokensPerSecond >= 100
    ? String(Math.round(tokensPerSecond))
    : tokensPerSecond.toFixed(1);
}

/**
 * 发送按钮旁的模型指标：生成速度、上下文占比估算、缓存命中率。
 * 口径都是快照里「最近一次主线模型请求」；任何一项缺失渲染「—」，
 * 绝不把缺失当成 0。usage 未收齐时 token 计数不可用；上下文占比是
 * 服务端估算值（≠精确 token 数）。所有尺寸都可见（需求四），小屏只藏
 * 「速度/上下文/缓存」标签文字、保留数值，紧凑排布不挤压输入与发送按钮。
 */
export function BotMetrics({ metrics }: { metrics?: BotMetricsData | null }) {
  const request = metrics?.request ?? null;
  // usage_complete=false 时 prompt/completion/cached 一律视为缺失。
  const usageReady = request?.usage_complete === true;

  const speed =
    usageReady &&
    request?.completion_tokens != null &&
    request.completion_tokens > 0 &&
    request.generation_ms > 0
      ? `${formatSpeed(request.completion_tokens / (request.generation_ms / 1000))} tok/s`
      : null;

  const window = metrics?.context_window ?? 0;
  const estimate = request?.input_tokens_estimate;
  // 估算为 0 等同于「还没有可展示的估算」（无请求/失败请求），显示「—」
  // 而不是「≈0%」。
  const contextPct =
    window > 0 && estimate != null && estimate > 0
      ? Math.round((estimate / window) * 100)
      : null;
  const sourceLabel =
    metrics?.context_source != null
      ? (CONTEXT_SOURCE_LABELS[metrics.context_source] ?? metrics.context_source)
      : null;
  const contextTitle =
    contextPct != null && estimate != null
      ? `上下文占比为估算值：约 ${formatTokens(estimate)} / ${formatTokens(window)} tokens` +
        ` · 估算口径 ${request?.input_estimator || "未知"}` +
        `${sourceLabel ? ` · 窗口来源：${sourceLabel}` : ""}` +
        `${metrics?.context_detail ? `（${metrics.context_detail}）` : ""}`
      : "暂无上下文占比：最近一次请求的输入估算或模型窗口未知";

  const cachePct =
    usageReady &&
    request?.prompt_tokens != null &&
    request.prompt_tokens > 0 &&
    request.cached_tokens != null
      ? Math.round((request.cached_tokens / request.prompt_tokens) * 100)
      : null;

  return (
    <div
      data-testid="bot-metrics"
      aria-label="模型指标"
      className="flex min-w-0 items-center gap-2 overflow-hidden text-xs whitespace-nowrap text-muted-foreground sm:gap-2.5"
    >
      <span
        data-testid="bot-metric-speed"
        title={
          speed != null
            ? "Token 输出速度 = completion_tokens ÷ 生成耗时（首个流式片段到请求结束）· 最近一次模型请求"
            : "暂无 Token 输出速度：最近一次请求的 usage 或生成耗时不可用"
        }
      >
        <span className="hidden sm:inline">速度 </span>
        <span className="font-mono">{speed ?? "—"}</span>
      </span>
      <span data-testid="bot-metric-context" title={contextTitle}>
        <span className="hidden sm:inline">上下文 </span>
        <span className="font-mono">
          {contextPct != null ? `≈${contextPct}%` : "—"}
        </span>
      </span>
      <span
        data-testid="bot-metric-cache"
        title={
          cachePct != null
            ? "缓存命中率 = cached_tokens / prompt_tokens · 最近一次模型请求"
            : "暂无缓存命中率：最近一次请求的 usage 不可用"
        }
      >
        <span className="hidden sm:inline">缓存 </span>
        <span className="font-mono">{cachePct != null ? `${cachePct}%` : "—"}</span>
      </span>
    </div>
  );
}
