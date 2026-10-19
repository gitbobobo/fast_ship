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

// 双环几何：圆心 (12,12)，尺寸与输入框那一行文字相称；环半径是外层 10、内层
// 5.5，环间留 2px 间隙，弧从顶部起画（rotate -90）。
const RING_CENTER = 12;
const RING_STROKE = 2.5;
const RINGS = {
  cache: { r: 10, circumference: 2 * Math.PI * 10 },
  context: { r: 5.5, circumference: 2 * Math.PI * 5.5 },
};

/** 估算可能超出窗口，占比按 0–100 夹紧，避免画出过量弧。 */
const clampPct = (pct: number) => Math.min(100, Math.max(0, pct));

/**
 * 单个进度环：实线轨道 + 进度弧。pct 为 null（缺失）时轨道画虚线并
 * 半透明，与「真实 0%」（实线轨道、无弧）视觉可区分；0% 也不画弧——
 * round linecap 会在零长度弧上画出圆点，看起来像有进度。
 */
function MetricRing({
  testId,
  radius,
  circumference,
  pct,
  title,
  arcClassName,
}: {
  testId: string;
  radius: number;
  circumference: number;
  pct: number | null;
  title: string;
  arcClassName: string;
}) {
  return (
    <g data-testid={testId}>
      <title>{title}</title>
      <circle
        cx={RING_CENTER}
        cy={RING_CENTER}
        r={radius}
        fill="none"
        strokeWidth={RING_STROKE}
        className={pct != null ? "stroke-muted" : "stroke-muted opacity-60"}
        strokeDasharray={pct != null ? undefined : "3 3"}
      />
      {pct != null && pct > 0 && (
        <circle
          cx={RING_CENTER}
          cy={RING_CENTER}
          r={radius}
          fill="none"
          strokeWidth={RING_STROKE}
          strokeLinecap="round"
          className={arcClassName}
          strokeDasharray={circumference}
          strokeDashoffset={circumference * (1 - pct / 100)}
          transform={`rotate(-90 ${RING_CENTER} ${RING_CENTER})`}
        />
      )}
    </g>
  );
}

/**
 * 发送按钮旁的模型指标：双环进度圆（外环缓存命中率、内环上下文占比，都用
 * 主题中性色，靠明暗而非色相区分）+ 右侧速度数值。口径都是快照里「最近一次主线模型请求」；任何一项
 * 缺失渲染「—」或不画弧，绝不把缺失当成 0。usage 未收齐时 token 计数
 * 不可用；上下文占比是服务端估算值（≠精确 token 数）。
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
  const speedTitle =
    speed != null
      ? "Token 输出速度 = completion_tokens ÷ 生成耗时（首个流式片段到请求结束）· 最近一次模型请求"
      : "暂无 Token 输出速度：最近一次请求的 usage 或生成耗时不可用";

  const window = metrics?.context_window ?? 0;
  const estimate = request?.input_tokens_estimate;
  // 估算为 0 等同于「还没有可展示的估算」（无请求/失败请求），不画弧
  // 也不显示「≈0%」。
  const contextPct =
    window > 0 && estimate != null && estimate > 0
      ? clampPct(Math.round((estimate / window) * 100))
      : null;
  const sourceLabel =
    metrics?.context_source != null
      ? (CONTEXT_SOURCE_LABELS[metrics.context_source] ?? metrics.context_source)
      : null;
  const contextTitle =
    contextPct != null && estimate != null
      ? `上下文占比 ≈${contextPct}%（估算）：约 ${formatTokens(estimate)} / ${formatTokens(window)} tokens` +
        ` · 估算口径 ${request?.input_estimator || "未知"}` +
        `${sourceLabel ? ` · 窗口来源：${sourceLabel}` : ""}` +
        `${metrics?.context_detail ? `（${metrics.context_detail}）` : ""}`
      : "暂无上下文占比：最近一次请求的输入估算或模型窗口未知";

  const cachePct =
    usageReady &&
    request?.prompt_tokens != null &&
    request.prompt_tokens > 0 &&
    request.cached_tokens != null
      ? clampPct(Math.round((request.cached_tokens / request.prompt_tokens) * 100))
      : null;
  const cacheTitle =
    cachePct != null
      ? `缓存命中率 ${cachePct}% = cached_tokens / prompt_tokens · 最近一次模型请求`
      : "暂无缓存命中率：最近一次请求的 usage 不可用";

  return (
    <div
      data-testid="bot-metrics"
      aria-label="模型指标"
      className="flex min-w-0 items-center gap-2 overflow-hidden text-xs whitespace-nowrap text-muted-foreground"
    >
      <svg
        viewBox="0 0 24 24"
        width={24}
        height={24}
        className="shrink-0"
        role="img"
        aria-label={`模型指标：${cachePct != null ? `缓存命中率 ${cachePct}%` : "缓存命中率不可用"}，${contextPct != null ? `上下文占比 ≈${contextPct}%` : "上下文占比不可用"}`}
      >
        <MetricRing
          testId="bot-metric-cache"
          radius={RINGS.cache.r}
          circumference={RINGS.cache.circumference}
          pct={cachePct}
          title={cacheTitle}
          arcClassName="stroke-foreground"
        />
        <MetricRing
          testId="bot-metric-context"
          radius={RINGS.context.r}
          circumference={RINGS.context.circumference}
          pct={contextPct}
          title={contextTitle}
          arcClassName="stroke-muted-foreground"
        />
      </svg>
      <span data-testid="bot-metric-speed" title={speedTitle}>
        <span className="font-mono">{speed ?? "—"}</span>
      </span>
    </div>
  );
}
