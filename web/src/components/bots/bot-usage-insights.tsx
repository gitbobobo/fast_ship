import { useMemo, useRef } from "react";
import { Link } from "react-router";
import { TriangleAlert } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useChartWidth } from "@/components/dashboard/metric-chart";
import { errorText } from "@/lib/api/client";
import type {
  BotUsageInsights,
  BotUsageInsightsBot,
  BotUsageInsightsTask,
  BotUsageRange,
} from "@/lib/api/bots";
import { useBotUsageInsights } from "@/lib/hooks/use-bots";
import { cn } from "@/lib/utils";
import { formatCostEstimate } from "./bot-metrics";
import { formatCacheHitRate, formatUsageCost } from "./bot-usage-overview";

/**
 * 花费优化参考（INT-163）：与总览同窗口、同 range/botId 参数的只读分区，
 * 回答「钱花在哪、怎么省」——上下文增长 / 缓存命中率 / 工具定义占比 /
 * 维护占比 / 定时任务空转 / 失败重试浪费 / 换模型差价。金额与总览同口径
 * （内置目录估算价）：可空字段是「未计价」显示「—」，缺失 ≠ 0。
 * 全览页与详情「花费」标签共用；embedded 窄栏把宽表换成紧凑清单。
 */

/** 占比与命中率同构：0–1 小数转整百分数，缺失「—」，极小非零「<1%」。复用同一实现。 */
const formatPercent = formatCacheHitRate;

const formatTokens = (n: number) => n.toLocaleString("en-US");

/** 任务显示名：已删除任务名恒为空串，统一落「已删除的任务」。 */
function insightTaskName(task: Pick<BotUsageInsightsTask, "name" | "deleted">) {
  return task.deleted || !task.name ? "已删除的任务" : task.name;
}

/** 空转判定口径：与契约 BotUsageInsightsTask 字段注释一致，表头 title 引用。 */
const IDLE_CRITERIA_NOTE =
  "空转按非「跳过」（skipped）的终态运行（completed/failed/stopped）评估；三条件可叠加命中：静默哨兵（silent=true）、没有任何工具调用行、没有任何写操作调用";

const MAINTENANCE_PURPOSE_LABELS: Record<string, string> = {
  summary: "摘要",
  archive: "归档",
  nightly: "夜间整理",
};

const maintenancePurposeLabel = (purpose: string) =>
  MAINTENANCE_PURPOSE_LABELS[purpose] ?? (purpose || "未标注");

/**
 * 常见错误码的中文释义：按码包含匹配（provider_auth_failed 命中 auth），
 * 覆盖服务端分类层（bot_model_errors.go）的产出码与历史数据里的通用码。
 * 码值本身始终原样保留供排查；未知码不显示释义、只显原码。
 */
const ERROR_CODE_HINTS: ReadonlyArray<readonly [pattern: string, label: string]> = [
  ["unavailable", "服务端错误"],
  ["auth", "鉴权失败"],
  ["rate_limit", "限流"],
  ["quota", "额度不足"],
  ["timeout", "超时"],
  ["context_length", "上下文超限"],
  ["context_uncompressible", "上下文超限"],
  ["network", "网络错误"],
  ["server_error", "服务端错误"],
  ["5xx", "服务端错误"],
  ["server_restarted", "服务重启中断"],
  ["model_not_found", "模型不存在"],
  ["tool_calls_dropped", "工具调用被丢弃"],
  ["request_failed", "请求失败"],
];

/** 错误码的中文释义；未知码返回 undefined。 */
export function errorCodeLabel(code: string): string | undefined {
  const lower = code.toLowerCase();
  for (const [pattern, label] of ERROR_CODE_HINTS) {
    if (lower.includes(pattern)) return label;
  }
  return undefined;
}

/** 换模型差价的相对色：省钱绿、更贵红、差价可忽略灰（与状态徽标同一配色系）。 */
function savingClass(saving: number) {
  if (saving >= 0.001) return "text-emerald-700 dark:text-emerald-400";
  if (saving <= -0.001) return "text-destructive";
  return "text-muted-foreground";
}

/** 换模型差价的差额文案：|saving|≥$0.001（最小展示精度）才标省/贵，
 * 低于精度时标「差价可忽略」——亚厘级差异是噪声，标绿「省 <$0.001」会
 * 误导成有可感知节省。 */
function savingText(saving: number) {
  if (saving >= 0.001) return `省 ${formatCostEstimate(saving)}`;
  if (saving <= -0.001) return `贵 ${formatCostEstimate(-saving)}`;
  return "差价可忽略";
}

/**
 * 显式时区的日期格式化器（zh-CN，与 formatDate 同 locale 同风格）：横轴与
 * 点悬浮都用服务端切日时区（data.timezone）渲染，保证与同一面板按天聚合
 * 的日期归属一致；时区串非法时回退浏览器时区（Intl 构造会抛 RangeError）。
 */
export function zonedFormatter(
  timezone: string,
  options: Intl.DateTimeFormatOptions,
): (value: number) => string {
  let formatter: Intl.DateTimeFormat;
  try {
    formatter = new Intl.DateTimeFormat("zh-CN", {
      ...options,
      timeZone: timezone,
    });
  } catch {
    formatter = new Intl.DateTimeFormat("zh-CN", options);
  }
  return (value) => formatter.format(value);
}

/* ─── 上下文增长散点/折线图（总览与运行明细页共用） ─── */

export interface ContextGrowthPoint {
  /** x 坐标值：时间毫秒（总览按 run 时间）或请求序号（运行明细页）。 */
  x: number;
  /** 输入 token 数。 */
  y: number;
  /** true = 估算值（请求缺 usage）：空心点呈现，图例须说明。 */
  estimated: boolean;
  /** 悬浮与无障碍名。 */
  label: string;
  /** 运行明细链接；缺省渲染不可点的纯点（运行明细页自指无意义）。 */
  href?: string;
}

/** 与 MetricChart 首系列一致的蓝。 */
const LINE_COLOR = "oklch(0.65 0.18 250)";

/** 图内边距：左侧留给 token 刻度文字，底部留给横轴标签。 */
const GROWTH_PAD = { left: 56, right: 16, top: 12, bottom: 28 };
const GROWTH_HEIGHT = 220;

/** 相邻横轴标签的最小像素间距（约一个「10/16」标签的宽度）。 */
const X_LABEL_MIN_GAP_PX = 48;

/**
 * 横轴标签按像素间距抽稀：本图的点按真实时间线性布局，时间分布不均时
 * 按数组下标抽稀会让多个标签挤进同一片像素。从最后一个点向左贪心（末点
 * 标签必保留，与 MetricChart 的口径一致），相邻已选标签保持 ≥ minGap；
 * 全部点同刻（xSpan=0）或单点时自然只剩 1 个标签。
 */
export function selectXLabelIndexes(
  cxs: number[],
  minGap = X_LABEL_MIN_GAP_PX,
): number[] {
  const indexes: number[] = [];
  let lastCx = Number.NaN;
  for (let i = cxs.length - 1; i >= 0; i--) {
    if (indexes.length === 0 || Math.abs(lastCx - cxs[i]) >= minGap) {
      indexes.push(i);
      lastCx = cxs[i];
    }
  }
  return indexes.reverse();
}

/** token 计数刻度：整数 4 档阶梯（token 不会是小数，直接向上取整）。 */
function tokenTickSpec(max: number): { step: number; intervals: number } {
  if (max <= 2) return { step: 1, intervals: 2 };
  const intervals = 4;
  return { step: Math.ceil(max / intervals), intervals };
}

/**
 * 每次运行第一次请求的输入 token 曲线：折线 + 散点，估算点画空心并带
 * data-estimated 标记；点带运行明细链接（href 缺省则纯展示）。
 * x 由调用方决定（时间或请求序号），本组件只负责坐标与标注。
 */
export function ContextGrowthChart({
  points,
  formatXLabel,
  emptyMessage,
  chartTitle,
  titleId,
}: {
  points: ContextGrowthPoint[];
  formatXLabel: (x: number) => string;
  emptyMessage: string;
  /** 图的中文标题：svg 经 aria-labelledby 指向它作可访问名。 */
  chartTitle: string;
  /** 标题元素 id；同页多张图时须唯一。 */
  titleId: string;
}) {
  const svgRef = useRef<SVGSVGElement>(null);
  const { boxRef, chartWidth } = useChartWidth();

  const layout = useMemo(() => {
    const pad = GROWTH_PAD;
    if (!points.length) return null;
    const maxY = Math.max(...points.map((p) => p.y), 0);
    const { step, intervals } = tokenTickSpec(maxY);
    const chartMax = Math.max(step * intervals, 1);
    const plotWidth = chartWidth - pad.left - pad.right;
    const baselineY = GROWTH_HEIGHT - pad.bottom;
    const plotHeight = baselineY - pad.top;
    const xs = points.map((p) => p.x);
    const minX = Math.min(...xs);
    const xSpan = Math.max(Math.max(...xs) - minX, 0);
    // 点半径留边：首尾的空心圆不压坐标轴，也不被容器裁剪。
    const usable = Math.max(plotWidth - 16, 1);
    const getX = (x: number) =>
      points.length === 1 || xSpan === 0
        ? pad.left + 8 + usable / 2
        : pad.left + 8 + (usable * (x - minX)) / xSpan;
    const getY = (y: number) =>
      baselineY - (plotHeight * Math.min(y, chartMax)) / chartMax;
    const coords = points.map((p) => ({ ...p, cx: getX(p.x), cy: getY(p.y) }));
    return {
      chartMax,
      baselineY,
      plotWidth,
      gridLines: Array.from({ length: intervals + 1 }, (_, i) => ({
        y: baselineY - (plotHeight * i) / intervals,
        value: step * i,
      })),
      coords,
      labelIndexes: selectXLabelIndexes(coords.map((c) => c.cx)),
    };
  }, [points, chartWidth]);

  return (
    <div className="relative">
      <div ref={boxRef} className="w-full">
        {!layout ? (
          <p className="flex h-[160px] items-center justify-center text-xs text-muted-foreground">
            {emptyMessage}
          </p>
        ) : (
          <svg
            ref={svgRef}
            viewBox={`0 0 ${chartWidth} ${GROWTH_HEIGHT}`}
            className="w-full overflow-visible"
            style={{ height: GROWTH_HEIGHT }}
            aria-labelledby={titleId}
          >
            {/* 可访问名指向图内中文标题；不给 svg 声明 role="img"，否则
                内部可聚焦的运行明细链接会被无障碍树剪掉。 */}
            <title id={titleId}>{chartTitle}</title>
            {layout.gridLines.map((line, i) => (
              <g key={`grid-${i}`}>
                <line
                  x1={GROWTH_PAD.left}
                  y1={line.y}
                  x2={chartWidth - GROWTH_PAD.right}
                  y2={line.y}
                  className="stroke-border/50"
                  strokeWidth="1"
                  strokeDasharray={i === 0 ? undefined : "3 3"}
                />
                <text
                  x={GROWTH_PAD.left - 8}
                  y={line.y + 4}
                  textAnchor="end"
                  className="fill-muted-foreground text-[11px]"
                >
                  {formatTokens(line.value)}
                </text>
              </g>
            ))}

            {layout.coords.length > 1 && (
              <polyline
                points={layout.coords.map((c) => `${c.cx},${c.cy}`).join(" ")}
                fill="none"
                stroke={LINE_COLOR}
                strokeWidth="1.5"
                strokeLinejoin="round"
                opacity={0.7}
              />
            )}

            {/* 重叠点（同一时刻多个 run）视觉上叠在一起，点击只会命中最
                上层——已知限制；日期相同不代表 run 相同。 */}
            {layout.coords.map((c, index) => {
              const circle = (
                <circle
                  cx={c.cx}
                  cy={c.cy}
                  r={4}
                  style={{
                    fill: c.estimated ? "none" : LINE_COLOR,
                    stroke: LINE_COLOR,
                    strokeWidth: 1.5,
                  }}
                />
              );
              return c.href ? (
                <Link
                  key={`${c.x}-${index}`}
                  to={c.href}
                  data-testid="usage-context-point"
                  data-estimated={c.estimated ? "true" : undefined}
                  aria-label={c.label}
                  className="cursor-pointer"
                >
                  <title>{c.label}</title>
                  {/* 透明命中圆：可视圆点 r=4 太小，外罩 r=10 保证点击
                      目标 ≥20px（fill=transparent 才参与命中，none 不行）。 */}
                  <circle cx={c.cx} cy={c.cy} r={10} fill="transparent" />
                  {circle}
                </Link>
              ) : (
                <g
                  key={`${c.x}-${index}`}
                  role="img"
                  aria-label={c.label}
                  data-testid="usage-context-point"
                  data-estimated={c.estimated ? "true" : undefined}
                >
                  <title>{c.label}</title>
                  {circle}
                </g>
              );
            })}

            {layout.labelIndexes.map((index) => {
              const c = layout.coords[index];
              return (
                <text
                  key={`x-${index}`}
                  x={c.cx}
                  y={GROWTH_HEIGHT - 8}
                  textAnchor="middle"
                  className="fill-muted-foreground text-[11px]"
                  data-testid="usage-context-x-label"
                >
                  {formatXLabel(c.x)}
                </text>
              );
            })}
          </svg>
        )}
      </div>
    </div>
  );
}

/** 图例：实心 = 实测、空心 = 估算值，与图上圆点同配色。 */
function EstimatedLegend({ className }: { className?: string }) {
  return (
    <p
      className={cn("flex flex-wrap items-center gap-x-2 text-xs text-muted-foreground", className)}
    >
      <span
        className="inline-block h-2.5 w-2.5 rounded-full"
        style={{ backgroundColor: LINE_COLOR }}
      />
      实测（usage 完整）
      <span
        className="inline-block h-2.5 w-2.5 rounded-full border-[1.5px]"
        style={{ borderColor: LINE_COLOR }}
      />
      估算值（请求缺 usage，取估算输入）
    </p>
  );
}

/* ─── 分区骨架 ─── */

function Section({
  title,
  note,
  testId,
  children,
}: {
  title: string;
  note?: string;
  testId: string;
  children: React.ReactNode;
}) {
  return (
    <div className="space-y-1.5" data-testid={testId}>
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-0.5">
        <h4 className="text-xs font-medium text-muted-foreground">{title}</h4>
        {note && <p className="text-xs text-muted-foreground/70">{note}</p>}
      </div>
      {children}
    </div>
  );
}

function EmptyRow({ children }: { children: React.ReactNode }) {
  return (
    <p className="rounded-lg border bg-muted/40 py-6 text-center text-sm text-muted-foreground">
      {children}
    </p>
  );
}

function TableFrame({ children }: { children: React.ReactNode }) {
  return (
    // 末列补到 12px 右内边距：表格在卡片内通宽排布时，p-2 的 8px 末列
    // 与卡片右缘几乎相贴（数值列右对齐尤为明显），统一与左侧留白对称。
    <div className="overflow-hidden rounded-lg border [&_td:last-child]:pr-3 [&_th:last-child]:pr-3">
      <Table>{children}</Table>
    </div>
  );
}

/* ─── 1. 输入上下文增长 ─── */

function ContextGrowthSection({
  bots,
  timezone,
}: {
  bots: BotUsageInsightsBot[];
  /** 服务端切日时区（data.timezone）：横轴与点悬浮都按它渲染。 */
  timezone: string;
}) {
  const series = bots.filter((bot) => bot.context_growth.length > 0);
  // 点按真实时间布局，但日期归属要与同一面板的按天缓存柱一致：
  // 横轴与点悬浮都用服务端切日时区格式化，不用浏览器时区。
  const formatAxisDay = useMemo(
    () => zonedFormatter(timezone, { month: "2-digit", day: "2-digit" }),
    [timezone],
  );
  const formatPointAt = useMemo(
    () =>
      zonedFormatter(timezone, {
        year: "numeric",
        month: "2-digit",
        day: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
      }),
    [timezone],
  );
  return (
    <Section
      title="输入上下文增长"
      note="每次运行第一次请求的输入 token 随时间变化；点可打开对应运行明细"
      testId="usage-insights-context"
    >
      {!series.length ? (
        <EmptyRow>窗口内没有运行记录</EmptyRow>
      ) : (
        <div className="space-y-3">
          <EstimatedLegend />
          {series.map((bot) => (
            <div key={bot.bot_id} className="space-y-0.5">
              <p className="flex flex-wrap items-baseline gap-x-2 text-xs">
                <Link
                  to={`/bots/${bot.bot_id}`}
                  className="font-medium hover:underline"
                >
                  {bot.name}
                </Link>
                {bot.context_growth_truncated && (
                  <span className="text-muted-foreground">仅最近 200 次</span>
                )}
              </p>
              <ContextGrowthChart
                points={bot.context_growth.map((point) => ({
                  x: Date.parse(point.at),
                  y: point.input_tokens,
                  estimated: point.estimated,
                  label: `${formatPointAt(Date.parse(point.at))} · ${formatTokens(point.input_tokens)} 输入${point.estimated ? "（估算）" : ""}`,
                  href: `/bots/usage/runs/${point.run_id}`,
                }))}
                formatXLabel={formatAxisDay}
                chartTitle="输入上下文增长"
                titleId={`usage-insights-context-title-${bot.bot_id}`}
                emptyMessage="窗口内没有运行记录"
              />
            </div>
          ))}
        </div>
      )}
    </Section>
  );
}

/* ─── 2. 缓存命中率（按天） ─── */

/**
 * 按天命中率条形自画：条长 = 命中率 × 轨道宽，按 0–100% 画满可用轨道，
 * 不按窗口内最大值拉伸、无 px 上限——条长本身编码比例。轨道规格（高度、
 * 圆角、标签列宽、右值排版）与 MetricChart 横向条形一致。缺 usage 的天
 * 保留行：条不画，轨道内浅灰标「无完整数据」，右值「—」（缺失 ≠ 0）。
 */
function CacheHitSection({ data }: { data: BotUsageInsights }) {
  const days = data.cache_hit_by_day;
  const missing = days.filter((day) => day.rate == null).length;
  return (
    <Section
      title="缓存命中率"
      note="缓存命中 token / 输入 token（cached/prompt），按天，只统计 usage 完整的请求；条长按 0–100% 画满轨道"
      testId="usage-insights-cache"
    >
      {!days.length ? (
        <EmptyRow>窗口内没有完整用量数据</EmptyRow>
      ) : (
        <div className="space-y-1">
          {days.map((day) => (
            <div
              key={day.date}
              className="group flex items-center gap-3"
              data-testid="usage-insights-cache-row"
            >
              <div
                className="w-14 shrink-0 truncate text-right text-xs text-muted-foreground"
                title={day.date}
              >
                {day.date.slice(5)}
              </div>
              <div className="relative flex-1">
                <div className="relative h-6 overflow-hidden rounded-full bg-muted/60">
                  {/* 真零不画条：一段短灰条会被误读成「有少量命中率」。 */}
                  {day.rate != null && day.rate > 0 && (
                    <div
                      className="absolute left-0 top-0 h-full rounded-full bg-primary/70 transition-colors duration-200 group-hover:bg-primary"
                      data-testid="usage-insights-cache-bar"
                      style={{ width: `${(day.rate * 100).toFixed(1)}%` }}
                    >
                      <div className="absolute inset-0 -translate-x-full animate-[shimmer_2s_infinite] bg-gradient-to-r from-transparent via-white/20 to-transparent" />
                    </div>
                  )}
                  {day.rate == null && (
                    <span className="absolute inset-0 flex items-center justify-center whitespace-nowrap text-[10px] text-muted-foreground/60">
                      无完整数据
                    </span>
                  )}
                </div>
              </div>
              <div className="min-w-[2rem] shrink-0 pr-3 text-right text-xs font-semibold tabular-nums text-muted-foreground">
                {formatCacheHitRate(day.rate)}
              </div>
            </div>
          ))}
          {missing > 0 && (
            <p className="text-xs text-muted-foreground">
              其中 {missing} 天无完整用量数据，未画条形。
            </p>
          )}
        </div>
      )}
    </Section>
  );
}

/* ─── 3. 工具定义占比 ─── */

function ToolsShareSection({
  bots,
  embedded,
}: {
  bots: BotUsageInsightsBot[];
  embedded?: boolean;
}) {
  return (
    <Section
      title="工具定义占比"
      note="工具定义 token 占输入 token 的比例；分子按工具结构（schema）字节估算，非精确 token 数"
      testId="usage-insights-tools"
    >
      {!bots.length ? (
        <EmptyRow>窗口内没有运行记录</EmptyRow>
      ) : embedded ? (
        <dl className="rounded-lg border">
          {bots.map((bot, i) => (
            <div
              key={bot.bot_id}
              className={cn(
                "flex items-baseline justify-between gap-3 px-3 py-2 text-sm",
                i > 0 && "border-t",
              )}
              title={`工具定义 ≈${formatTokens(bot.tools.tools_tokens_estimate)} / 输入 ${formatTokens(bot.tools.prompt_tokens)}（按工具结构 schema 字节估算）`}
            >
              <dt className="truncate text-muted-foreground">{bot.name}</dt>
              <dd className="tabular-nums">
                {bot.tools.share == null ? (
                  // 与宽表同口径：未计价只显「—」，不印 ≈0 / 0 的假分子分母。
                  "—"
                ) : (
                  <>
                    <span>{formatPercent(bot.tools.share)}</span>
                    <span className="ml-2 text-xs text-muted-foreground">
                      ≈{formatTokens(bot.tools.tools_tokens_estimate)} /{" "}
                      {formatTokens(bot.tools.prompt_tokens)}
                    </span>
                  </>
                )}
              </dd>
            </div>
          ))}
        </dl>
      ) : (
        <TableFrame>
          <TableHeader>
            <TableRow>
              <TableHead>机器人</TableHead>
              <TableHead>占比</TableHead>
              <TableHead title="分子：工具定义估算 token（按工具结构 schema 字节）；分母：usage 完整请求的输入 token 合计">
                明细
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {bots.map((bot) => (
              <TableRow key={bot.bot_id} data-testid="usage-insights-tools-row">
                <TableCell className="font-medium">{bot.name}</TableCell>
                <TableCell className="tabular-nums">
                  {formatPercent(bot.tools.share)}
                </TableCell>
                <TableCell
                  className="tabular-nums text-muted-foreground"
                  title={`工具定义 ≈${formatTokens(bot.tools.tools_tokens_estimate)} / 输入 ${formatTokens(bot.tools.prompt_tokens)}（按工具结构 schema 字节估算）`}
                >
                  {bot.tools.share == null
                    ? "—"
                    : `≈${formatTokens(bot.tools.tools_tokens_estimate)} / ${formatTokens(bot.tools.prompt_tokens)}`}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </TableFrame>
      )}
    </Section>
  );
}

/* ─── 4. 维护占比 ─── */

function maintenancePurposeText(bot: BotUsageInsightsBot) {
  if (!bot.maintenance.by_purpose.length) return "—";
  return bot.maintenance.by_purpose
    .map(
      (p) =>
        `${maintenancePurposeLabel(p.purpose)} ${formatUsageCost(p.cost)}`,
    )
    .join(" · ");
}

function MaintenanceShareSection({
  bots,
  embedded,
}: {
  bots: BotUsageInsightsBot[];
  embedded?: boolean;
}) {
  return (
    <Section
      title="维护占比"
      note="全部维护请求（含挂到运行 run 名下的压缩/归档，口径大于总览「维护」桶）占该机器人总花费"
      testId="usage-insights-maintenance"
    >
      {!bots.length ? (
        <EmptyRow>窗口内没有运行记录</EmptyRow>
      ) : embedded ? (
        <dl className="rounded-lg border">
          {bots.map((bot, i) => (
            <div
              key={bot.bot_id}
              className={cn(
                "space-y-0.5 px-3 py-2 text-sm",
                i > 0 && "border-t",
              )}
            >
              <div className="flex items-baseline justify-between gap-3">
                <dt className="truncate text-muted-foreground">{bot.name}</dt>
                <dd className="tabular-nums">
                  {bot.maintenance.cost == null
                    ? "—"
                    : `${formatUsageCost(bot.maintenance.cost)} · ${formatPercent(bot.maintenance.share)}`}
                </dd>
              </div>
              {bot.maintenance.by_purpose.length > 0 && (
                <p className="text-xs text-muted-foreground">
                  {maintenancePurposeText(bot)}
                </p>
              )}
            </div>
          ))}
        </dl>
      ) : (
        <TableFrame>
          <TableHeader>
            <TableRow>
              <TableHead>机器人</TableHead>
              <TableHead>维护花费</TableHead>
              <TableHead title="维护花费 / 该机器人总花费（总览三桶之和）；任一未计价时为空">
                占比
              </TableHead>
              <TableHead>用途拆分</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {bots.map((bot) => (
              <TableRow
                key={bot.bot_id}
                data-testid="usage-insights-maintenance-row"
              >
                <TableCell className="font-medium">{bot.name}</TableCell>
                <TableCell className="tabular-nums">
                  {formatUsageCost(bot.maintenance.cost)}
                </TableCell>
                <TableCell className="tabular-nums">
                  {formatPercent(bot.maintenance.share)}
                </TableCell>
                <TableCell className="text-muted-foreground">
                  {maintenancePurposeText(bot)}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </TableFrame>
      )}
    </Section>
  );
}

/* ─── 5. 定时任务空转（含每任务缓存命中率） ─── */

function IdleSection({
  tasks,
  embedded,
}: {
  tasks: BotUsageInsightsTask[];
  embedded?: boolean;
}) {
  return (
    <Section
      title="定时任务空转"
      note="白跑的定时任务：按非「跳过」（skipped）终态运行评估，三条件可叠加命中"
      testId="usage-insights-idle"
    >
      {!tasks.length ? (
        <EmptyRow>窗口内没有定时任务运行</EmptyRow>
      ) : embedded ? (
        <ul className="divide-y rounded-lg border">
          {tasks.map((task) => (
            <li key={task.task_id} className="space-y-0.5 px-3 py-2">
              <p className="flex items-center gap-1.5 text-sm font-medium">
                <span
                  className={cn(
                    "truncate",
                    task.deleted && "text-muted-foreground",
                  )}
                >
                  {insightTaskName(task)}
                </span>
                {!task.enabled && !task.deleted && (
                  <span className="rounded bg-secondary px-1.5 py-0.5 text-[10px] text-secondary-foreground">
                    已停用
                  </span>
                )}
              </p>
              <p
                className="text-xs text-muted-foreground"
                title={IDLE_CRITERIA_NOTE}
              >
                空转 {task.idle_runs}/{task.evaluated_runs}（
                {formatPercent(task.idle_rate)}）· 浪费{" "}
                {formatUsageCost(task.idle_cost)} · 静默 {task.silent_runs} ·
                无工具 {task.no_tool_call_runs} · 无写 {task.no_write_call_runs}
              </p>
              <p className="text-xs text-muted-foreground">
                缓存命中率 {formatCacheHitRate(task.cache_hit_rate)}
              </p>
            </li>
          ))}
        </ul>
      ) : (
        <TableFrame>
          <TableHeader>
            <TableRow>
              <TableHead>任务</TableHead>
              <TableHead>机器人</TableHead>
              <TableHead title={`空转 / 评估分母：${IDLE_CRITERIA_NOTE}`}>
                空转/评估
              </TableHead>
              <TableHead>空转率</TableHead>
              <TableHead>浪费</TableHead>
              <TableHead title="条件一：静默哨兵（silent=true）">静默</TableHead>
              <TableHead title="条件二：整次运行没有任何工具调用行">
                无工具
              </TableHead>
              <TableHead title="条件三：没有任何写操作调用（只读之外的动作）">
                无写
              </TableHead>
              <TableHead>缓存命中率</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {tasks.map((task) => (
              <TableRow key={task.task_id} data-testid="usage-insights-idle-row">
                <TableCell className="max-w-40 font-medium whitespace-normal">
                  <span
                    className={cn(task.deleted && "text-muted-foreground")}
                  >
                    {insightTaskName(task)}
                  </span>
                  {!task.enabled && !task.deleted && (
                    <span className="ml-1.5 rounded bg-secondary px-1.5 py-0.5 text-[10px] text-secondary-foreground">
                      已停用
                    </span>
                  )}
                </TableCell>
                <TableCell>
                  <Link
                    to={`/bots/${task.bot_id}`}
                    className="hover:underline"
                  >
                    {task.bot_name}
                  </Link>
                </TableCell>
                <TableCell className="tabular-nums">
                  {task.idle_runs}/{task.evaluated_runs}
                </TableCell>
                <TableCell className="tabular-nums">
                  {formatPercent(task.idle_rate)}
                </TableCell>
                <TableCell className="tabular-nums">
                  {formatUsageCost(task.idle_cost)}
                </TableCell>
                <TableCell className="text-right tabular-nums">
                  {task.silent_runs}
                </TableCell>
                <TableCell className="text-right tabular-nums">
                  {task.no_tool_call_runs}
                </TableCell>
                <TableCell className="text-right tabular-nums">
                  {task.no_write_call_runs}
                </TableCell>
                <TableCell className="tabular-nums">
                  {formatCacheHitRate(task.cache_hit_rate)}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </TableFrame>
      )}
    </Section>
  );
}

/* ─── 6. 失败与重试浪费 ─── */

/** 错误码内容：码值原样保留，已知码附小字中文副标并写进 title 悬浮。 */
function ErrorCodeValue({ code }: { code?: string | null }) {
  if (!code) return <>无错误码</>;
  const label = errorCodeLabel(code);
  return (
    <span title={label ? `${code}：${label}` : undefined}>
      {code}
      {label && (
        <span className="ml-1.5 text-xs text-muted-foreground">{label}</span>
      )}
    </span>
  );
}

function FailuresSection({
  bots,
  embedded,
}: {
  bots: BotUsageInsightsBot[];
  embedded?: boolean;
}) {
  const errorRows = bots.flatMap((bot) =>
    bot.failures.by_error.map((error) => ({ bot, error })),
  );
  return (
    <Section
      title="失败与重试浪费"
      note="失败尝试若送过 usage 就已计价，其花费即浪费；重试按重复请求指纹（request_hash）计数"
      testId="usage-insights-failures"
    >
      {!bots.length ? (
        <EmptyRow>窗口内没有运行记录</EmptyRow>
      ) : embedded ? (
        <dl className="rounded-lg border">
          {bots.map((bot, i) => (
            <div
              key={bot.bot_id}
              className={cn(
                "space-y-0.5 px-3 py-2 text-sm",
                i > 0 && "border-t",
              )}
            >
              <div className="flex items-baseline justify-between gap-3">
                <dt className="truncate text-muted-foreground">{bot.name}</dt>
                <dd className="tabular-nums">
                  请求浪费 {formatUsageCost(bot.failures.failed_requests_cost)}
                </dd>
              </div>
              <p className="text-xs text-muted-foreground">
                失败运行 {bot.failures.failed_runs} 次（
                {formatUsageCost(bot.failures.failed_runs_cost)}）· 重试{" "}
                {bot.failures.retried_requests} 次
              </p>
              {bot.failures.by_error.map((error, index) => (
                <p
                  key={index}
                  className="text-xs text-muted-foreground"
                  data-testid="usage-insights-error-row"
                >
                  <ErrorCodeValue code={error.error_code} /> ·{" "}
                  {error.requests} 次 · {formatUsageCost(error.cost)}
                </p>
              ))}
            </div>
          ))}
        </dl>
      ) : (
        <>
          <TableFrame>
            <TableHeader>
              <TableRow>
                <TableHead>机器人</TableHead>
                <TableHead title="失败请求中已计价金额合计">请求浪费</TableHead>
                <TableHead>失败运行</TableHead>
                <TableHead>失败运行花费</TableHead>
                <TableHead title="与同次运行（run）内更早请求指纹（request_hash）相同的请求行数">
                  重试请求
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {bots.map((bot) => (
                <TableRow
                  key={bot.bot_id}
                  data-testid="usage-insights-failures-row"
                >
                  <TableCell className="font-medium">{bot.name}</TableCell>
                  <TableCell className="tabular-nums">
                    {formatUsageCost(bot.failures.failed_requests_cost)}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {bot.failures.failed_runs}
                  </TableCell>
                  <TableCell className="tabular-nums">
                    {formatUsageCost(bot.failures.failed_runs_cost)}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {bot.failures.retried_requests}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </TableFrame>
          {errorRows.length > 0 && (
            <TableFrame>
              <TableHeader>
                <TableRow>
                  <TableHead>机器人</TableHead>
                  <TableHead>错误码</TableHead>
                  <TableHead className="text-right">请求</TableHead>
                  <TableHead>花费</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {errorRows.map(({ bot, error }, i) => (
                  <TableRow
                    key={`${bot.bot_id}-${i}-${error.error_code ?? "none"}`}
                    data-testid="usage-insights-error-row"
                  >
                    <TableCell>{bot.name}</TableCell>
                    <TableCell
                      className={cn(!error.error_code && "text-muted-foreground")}
                    >
                      <ErrorCodeValue code={error.error_code} />
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {error.requests}
                    </TableCell>
                    <TableCell className="tabular-nums">
                      {formatUsageCost(error.cost)}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </TableFrame>
          )}
        </>
      )}
    </Section>
  );
}

/* ─── 7. 换模型差价 ─── */

function ModelAlternativesSection({ bots }: { bots: BotUsageInsightsBot[] }) {
  const groups = bots.flatMap((bot) =>
    bot.model_alternatives.map((group) => ({ bot, group })),
  );
  return (
    <Section
      title="换模型差价"
      testId="usage-insights-models"
    >
      {groups.length === 0 ? (
        <EmptyRow>窗口内没有已计价请求，无法比较模型差价</EmptyRow>
      ) : (
        <>
          {/* 醒目提示：口径边界必须先于任何候选数字出现（验收断言）。 */}
          <p
            className="flex items-start gap-2 rounded-lg border border-amber-500/40 bg-amber-500/5 px-3 py-2 text-xs text-amber-700 dark:text-amber-400"
            data-testid="usage-insights-models-warning"
          >
            <TriangleAlert className="mt-0.5 h-3.5 w-3.5 shrink-0" />
            差价只按内置目录价格重算，不代表效果一样；也未考虑不同模型
            tokenizer 的差异。
          </p>
          <ul className="space-y-3">
            {groups.map(({ bot, group }) => (
              <li
                key={`${bot.bot_id}-${group.catalog_id}-${group.model}`}
                className="space-y-1 rounded-lg border px-3 py-2"
                data-testid="usage-insights-model-group"
              >
                <p className="text-xs">
                  <span className="font-medium">{bot.name}</span>
                  <span className="ml-2 font-mono">{group.model}</span>
                  <span className="ml-2 text-muted-foreground">
                    {group.catalog_id} · {group.requests} 次请求 · 当前实际{" "}
                    {formatCostEstimate(group.actual_cost)}
                  </span>
                </p>
                <ul className="space-y-0.5">
                  {group.candidates.map((candidate) => (
                    <li
                      key={candidate.model}
                      className="flex flex-wrap items-baseline gap-x-3 text-xs"
                      data-testid="usage-insights-model-candidate"
                    >
                      <span className="font-mono">{candidate.model}</span>
                      <span className="whitespace-nowrap tabular-nums">
                        {formatCostEstimate(candidate.cost)}
                      </span>
                      <span
                        className={cn(
                          "whitespace-nowrap tabular-nums",
                          savingClass(candidate.saving),
                        )}
                      >
                        {savingText(candidate.saving)}
                      </span>
                    </li>
                  ))}
                </ul>
              </li>
            ))}
          </ul>
        </>
      )}
    </Section>
  );
}

/* ─── 面板 ─── */

export function BotUsageInsightsPanel({
  range,
  botId,
  embedded,
}: {
  /** 与总览共用同一 range 状态，切范围两份数据一起换窗口。 */
  range: BotUsageRange;
  /** 限定单个机器人（详情「花费」标签）；缺省为全部机器人。 */
  botId?: string;
  /** 窄栏嵌入版式：宽表换成紧凑清单。 */
  embedded?: boolean;
}) {
  const { data, isLoading, isError, error, refetch } = useBotUsageInsights(
    range,
    botId,
  );

  return (
    <section className="space-y-4" data-testid="usage-insights">
      <h3 className="text-sm font-medium">优化参考</h3>
      {isLoading ? (
        <div className="space-y-3">
          <Skeleton className="h-40 rounded-xl" />
          <Skeleton className="h-24 rounded-xl" />
        </div>
      ) : isError ? (
        <div className="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-destructive/40 bg-destructive/[0.06] px-4 py-3">
          <div className="flex items-center gap-2 text-sm text-destructive">
            <TriangleAlert className="h-4 w-4 shrink-0" />
            {errorText(error, "优化参考数据加载失败")}
          </div>
          <Button size="sm" variant="outline" onClick={() => void refetch()}>
            重试
          </Button>
        </div>
      ) : !data ? null : (
        <>
          <ContextGrowthSection bots={data.bots} timezone={data.timezone} />
          <CacheHitSection data={data} />
          <ToolsShareSection bots={data.bots} embedded={embedded} />
          <MaintenanceShareSection bots={data.bots} embedded={embedded} />
          <IdleSection tasks={data.schedule_tasks} embedded={embedded} />
          <FailuresSection bots={data.bots} embedded={embedded} />
          <ModelAlternativesSection bots={data.bots} />
        </>
      )}
    </section>
  );
}
