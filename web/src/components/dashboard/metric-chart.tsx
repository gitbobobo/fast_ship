import {
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
  type RefObject,
} from "react";
import { cn } from "@/lib/utils";

export interface MetricChartPoint {
  id?: string;
  label: string;
  value: number;
  shortLabel?: string;
}

export interface MetricChartSeriesPoint {
  label: string;
  value: number;
  shortLabel?: string;
  series: MetricChartSeries[];
}

export interface MetricChartSeries {
  id: string;
  name: string;
  value: number;
}

/** 数值标签格式化：坐标刻度、行尾数值与悬浮明细共用；缺省原样输出数字。 */
export type MetricChartFormatValue = (value: number) => string;

/** 默认数值标签：剥掉二进制浮点尾差（0.30000000000000004 → 0.3）。 */
const defaultFormatValue: MetricChartFormatValue = (value) =>
  String(Number(value.toFixed(6)));

interface MetricChartProps {
  title?: string;
  description?: string;
  points: MetricChartPoint[];
  variant: "bar";
  emptyMessage: string;
  emptyAction?: ReactNode;
  formatValue?: MetricChartFormatValue;
  className?: string;
}

interface MultiSeriesChartProps {
  title?: string;
  description?: string;
  points: MetricChartSeriesPoint[];
  /** area 为堆叠面积图；columns 为按点堆叠的竖向柱状图。 */
  variant: "area" | "columns";
  emptyMessage: string;
  emptyAction?: ReactNode;
  formatValue?: MetricChartFormatValue;
  className?: string;
}

type ChartProps = MetricChartProps | MultiSeriesChartProps;

const CHART_COLORS = [
  "oklch(0.65 0.18 250)",   // blue
  "oklch(0.65 0.18 150)",   // green
  "oklch(0.65 0.18 30)",    // orange
  "oklch(0.55 0.2 300)",    // purple
  "oklch(0.6 0.16 200)",    // teal
  "oklch(0.65 0.15 50)",    // amber
  "oklch(0.55 0.18 180)",   // emerald
  "oklch(0.6 0.14 280)",    // indigo
];

export function MetricChart(props: ChartProps) {
  const { title, description, variant, emptyMessage, emptyAction, className } =
    props;

  return (
    <div className={cn("space-y-4", className)}>
      {(title || description) && (
        <div className="space-y-1">
          {title && <div className="text-sm font-medium">{title}</div>}
          {description && (
            <div className="text-xs text-muted-foreground">{description}</div>
          )}
        </div>
      )}

      <div className="relative">
        {variant === "bar" ? (
          <HorizontalBarChart
            points={(props as MetricChartProps).points}
            emptyMessage={emptyMessage}
            emptyAction={emptyAction}
            formatValue={props.formatValue}
          />
        ) : (
          <StackedSeriesChart
            shape={variant}
            points={(props as MultiSeriesChartProps).points}
            emptyMessage={emptyMessage}
            emptyAction={emptyAction}
            formatValue={props.formatValue}
          />
        )}
      </div>
    </div>
  );
}

function getPointKey(point: { label: string; id?: string }, index: number) {
  return point.id ?? `${point.label}-${index}`;
}

/** 空数据处理：只显示一段居中说明（可带一个真实入口），不画坐标轴，免得多套空状态互相打架。 */
function ChartEmptyState({
  message,
  action,
}: {
  message: string;
  action?: ReactNode;
}) {
  return (
    <div className="flex h-[240px] flex-col items-center justify-center gap-2 px-4 text-center">
      <p className="text-xs text-muted-foreground">{message}</p>
      {action}
    </div>
  );
}

/* ─── Horizontal Bar Chart ─── */

function HorizontalBarChart({
  points,
  emptyMessage,
  emptyAction,
  formatValue = defaultFormatValue,
}: {
  points: MetricChartPoint[];
  emptyMessage: string;
  emptyAction?: ReactNode;
  formatValue?: MetricChartFormatValue;
}) {
  const [hoveredIndex, setHoveredIndex] = useState<number | null>(null);
  const maxValue = Math.max(...points.map((p) => p.value), 0);
  const chartMax = Math.max(maxValue, 1);
  const barHeight = 24;
  const labelWidth = 96;
  const maxBarWidth = 360;
  // 只有完全没数据才算空；全是 0 的项目行仍然保留，读者能看到项目本身。
  const isEmpty = points.length === 0;

  if (isEmpty) {
    return <ChartEmptyState message={emptyMessage} action={emptyAction} />;
  }

  return (
    <div className="space-y-1">
      {points.map((point, index) => {
        const ratio = point.value / chartMax;
        const isHovered = hoveredIndex === index;

        return (
          <div
            key={getPointKey(point, index)}
            className="group flex items-center gap-3"
            onMouseEnter={() => setHoveredIndex(index)}
            onMouseLeave={() => setHoveredIndex(null)}
          >
            <div
              className="shrink-0 truncate text-right text-xs text-muted-foreground transition-colors"
              style={{ width: labelWidth }}
              title={point.label}
            >
              {point.shortLabel ?? point.label}
            </div>

            <div className="relative flex-1">
              <div
                className="relative overflow-hidden rounded-full bg-muted/60 transition-all duration-500"
                style={{ height: barHeight }}
              >
                {/* 值为 0 时只留空轨道：画一段短灰条会被误读成「有少量进度」。 */}
                {point.value > 0 && (
                  <div
                    className={cn(
                      "absolute left-0 top-0 h-full rounded-full transition-all duration-500 ease-out",
                      isHovered ? "bg-primary" : "bg-primary/70",
                    )}
                    style={{
                      width: `${(ratio * 100).toFixed(1)}%`,
                      maxWidth: maxBarWidth,
                    }}
                  >
                    <div className="absolute inset-0 -translate-x-full animate-[shimmer_2s_infinite] bg-gradient-to-r from-transparent via-white/20 to-transparent" />
                  </div>
                )}
              </div>
            </div>

            <div
              className={cn(
                "min-w-[2rem] shrink-0 text-right text-xs font-semibold tabular-nums transition-colors duration-200",
                isHovered ? "text-foreground" : "text-muted-foreground",
              )}
            >
              {formatValue(point.value)}
            </div>
          </div>
        );
      })}
    </div>
  );
}

/* ─── Stacked Series Chart（面积 / 柱状共用坐标、刻度、悬浮明细）─── */

const CHART_HEIGHT = 240;
const CHART_PADDING_X = 40;
const CHART_PADDING_TOP = 24;
const CHART_PADDING_BOTTOM = 30;

interface SeriesMeta {
  id: string;
  name: string;
  color: string;
}

interface StackedDatum {
  label: string;
  shortLabel?: string;
  total: number;
  stacks: (SeriesMeta & { value: number; bottom: number; top: number })[];
}

/** 系列清单按首次出现次序分配颜色：同一 id 跨点归入同一层。 */
function useSeriesMeta(points: MetricChartSeriesPoint[]): SeriesMeta[] {
  return useMemo(() => {
    const seen = new Map<string, string>();
    for (const point of points) {
      for (const s of point.series) {
        if (!seen.has(s.id)) {
          seen.set(s.id, s.name);
        }
      }
    }
    return Array.from(seen.entries()).map(([id, name], index) => ({
      id,
      name,
      color: CHART_COLORS[index % CHART_COLORS.length],
    }));
  }, [points]);
}

/** 每个点按系列清单累出堆叠区间：缺的系列补 0，层序与颜色分配一致。 */
function buildStackedData(
  points: MetricChartSeriesPoint[],
  seriesMeta: SeriesMeta[],
): StackedDatum[] {
  return points.map((point) => {
    let cumulative = 0;
    const stacks = seriesMeta.map((meta) => {
      const s = point.series.find((x) => x.id === meta.id);
      const value = s?.value ?? 0;
      const bottom = cumulative;
      cumulative += value;
      return { ...meta, value, bottom, top: cumulative };
    });
    return {
      label: point.label,
      shortLabel: point.shortLabel,
      total: cumulative,
      stacks,
    };
  });
}

/**
 * 纵轴刻度：整数计数沿用整数阶梯——最大 ≤2 时只分 0/1/2 两档，峰值不至于
 * 只占四分之一高；其余按 4 档整数步长。金额这类小数数据改用能覆盖量级的
 * 最小 1/2/5×10^n 步长（最大 <$0.01 也有刻度），不然整数刻度会把柱子
 * 压成几乎不可见。
 */
function yTickSpec(maxTotal: number): { step: number; intervals: number } {
  if (maxTotal <= 2 && Number.isInteger(maxTotal)) {
    return { step: 1, intervals: 2 };
  }
  const intervals = 4;
  const raw = maxTotal / intervals;
  if (raw >= 1) return { step: Math.ceil(raw), intervals };
  const mag = Math.pow(10, Math.floor(Math.log10(raw)));
  for (const m of [1, 2, 5, 10]) {
    if (m * mag >= raw) return { step: m * mag, intervals };
  }
  return { step: 10 * mag, intervals };
}

/** 横轴标签：从最后一个点按固定步长往前取，保证相邻标签间隔一致，
 *  且最后一个标签永远落在最后一个点正下方。首点可能因此没有标签（30 个点时
 *  是 4/9/14/19/24/29），不要在中间插一个不均匀的间隔。 */
function xLabelIndexes(count: number): number[] {
  const last = count - 1;
  if (last < 1) return count === 1 ? [0] : [];
  const stride = Math.max(Math.ceil(last / 6), 1);
  const indexes: number[] = [];
  for (let index = last; index >= 0; index -= stride) indexes.push(index);
  return indexes.reverse();
}

/** 用实测宽度画图（1:1 像素坐标）：viewBox 缩放会把坐标轴文字一起缩小，
 *  也会让横轴日期和数据点错位。 */
function useChartWidth() {
  const boxRef = useRef<HTMLDivElement>(null);
  const [chartWidth, setChartWidth] = useState(720);

  useLayoutEffect(() => {
    const el = boxRef.current;
    if (!el) return;
    const apply = (width: number) => {
      const next = Math.round(width);
      if (next > 0) setChartWidth(next);
    };
    apply(el.getBoundingClientRect().width);
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver((entries) => {
      apply(entries[0]?.contentRect.width ?? 0);
    });
    observer.observe(el);
    return () => observer.disconnect();
  }, []);

  return { boxRef, chartWidth };
}

/** 悬浮取最近点的横坐标索引，并记下浮层锚点（相对 svg 容器）。 */
function useSeriesHover(
  svgRef: RefObject<SVGSVGElement | null>,
  xs: number[],
  chartWidth: number,
) {
  const [hoveredIndex, setHoveredIndex] = useState<number | null>(null);
  const [tooltipPos, setTooltipPos] = useState<{
    x: number;
    y: number;
    containerWidth: number;
  } | null>(null);

  const onMouseMove = (e: React.MouseEvent<SVGSVGElement>) => {
    if (!svgRef.current) return;
    const rect = svgRef.current.getBoundingClientRect();
    const x = e.clientX - rect.left;
    const svgX = (x / rect.width) * chartWidth;

    let nearestIndex = 0;
    let minDist = Infinity;
    for (let i = 0; i < xs.length; i++) {
      const dist = Math.abs(xs[i] - svgX);
      if (dist < minDist) {
        minDist = dist;
        nearestIndex = i;
      }
    }

    setHoveredIndex(nearestIndex);
    setTooltipPos({
      x: e.clientX - rect.left,
      y: e.clientY - rect.top,
      containerWidth: rect.width,
    });
  };

  const onMouseLeave = () => {
    setHoveredIndex(null);
    setTooltipPos(null);
  };

  return { hoveredIndex, tooltipPos, onMouseMove, onMouseLeave };
}

/** 悬浮明细：该点各系列取值（倒序，视觉最上层先列）与总计。 */
function SeriesTooltip({
  datum,
  pos,
  formatValue,
}: {
  datum: {
    label: string;
    total: number;
    stacks: (SeriesMeta & { value: number })[];
  };
  pos: { x: number; y: number; containerWidth: number };
  formatValue: MetricChartFormatValue;
}) {
  return (
    <div
      className="pointer-events-none absolute z-50 min-w-[160px] rounded-lg border bg-popover px-3 py-2 shadow-lg"
      style={{
        left: Math.min(pos.x + 16, (pos.containerWidth ?? 400) - 170),
        top: Math.max(pos.y - 10, 0),
      }}
    >
      <div className="mb-1.5 text-xs font-medium text-popover-foreground">
        {datum.label}
      </div>
      <div className="space-y-1">
        {datum.stacks
          .filter((s) => s.value > 0)
          .slice()
          .reverse()
          .map((stack) => (
            <div
              key={stack.id}
              className="flex items-center justify-between gap-4"
            >
              <div className="flex items-center gap-1.5">
                <span
                  className="inline-block h-2 w-2 rounded-full"
                  style={{ backgroundColor: stack.color }}
                />
                <span className="max-w-[120px] truncate text-[11px] text-muted-foreground">
                  {stack.name}
                </span>
              </div>
              <span className="text-[11px] font-semibold tabular-nums text-popover-foreground">
                {formatValue(stack.value)}
              </span>
            </div>
          ))}
        {datum.total > 0 && (
          <div className="mt-1 flex items-center justify-between gap-4 border-t border-border/50 pt-1">
            <span className="text-[11px] text-muted-foreground">总计</span>
            <span className="text-[11px] font-bold tabular-nums text-popover-foreground">
              {formatValue(datum.total)}
            </span>
          </div>
        )}
      </div>
    </div>
  );
}

function StackedSeriesChart({
  shape,
  points,
  emptyMessage,
  emptyAction,
  formatValue = defaultFormatValue,
}: {
  /** area 渲染堆叠面积层，columns 渲染每点一根堆叠柱。 */
  shape: "area" | "columns";
  points: MetricChartSeriesPoint[];
  emptyMessage: string;
  emptyAction?: ReactNode;
  formatValue?: MetricChartFormatValue;
}) {
  const svgRef = useRef<SVGSVGElement>(null);
  const { boxRef, chartWidth } = useChartWidth();

  const seriesMeta = useSeriesMeta(points);
  const stackedData = useMemo(
    () => buildStackedData(points, seriesMeta),
    [points, seriesMeta],
  );

  const maxTotal = Math.max(...stackedData.map((d) => d.total), 0);
  const { step: tickStep, intervals: tickIntervals } = yTickSpec(maxTotal);
  const chartMax = tickStep * tickIntervals;
  const isEmpty = points.length === 0 || maxTotal === 0;

  // 面积图至少两个点才能画出形状：单点复制一份到右端；柱状按槽位居中，
  // 单点独占中央，不需要复制。
  const shapedData =
    shape === "area" && stackedData.length === 1
      ? [...stackedData, { ...stackedData[0] }]
      : stackedData;

  const plotWidth = chartWidth - CHART_PADDING_X * 2;
  const baselineY = CHART_HEIGHT - CHART_PADDING_BOTTOM;
  const plotHeight = baselineY - CHART_PADDING_TOP;
  const denominator = Math.max(shapedData.length - 1, 1);
  const slotWidth = stackedData.length
    ? plotWidth / stackedData.length
    : plotWidth;
  const columnWidth = Math.min(slotWidth * 0.55, 48);

  const getY = (value: number) =>
    baselineY - (plotHeight * Math.min(value, chartMax)) / chartMax;

  const coordinates = shapedData.map((point, index) => ({
    x:
      shape === "columns"
        ? CHART_PADDING_X + slotWidth * (index + 0.5)
        : CHART_PADDING_X + (plotWidth * index) / denominator,
    yTotal: getY(point.total),
    stacks: point.stacks.map((stack) => ({
      ...stack,
      yBottom: getY(stack.bottom),
      yTop: getY(stack.top),
    })),
    label: point.label,
    shortLabel: point.shortLabel,
    total: point.total,
  }));

  // 面积层路径：顶线从左到右、底线从右到左围出闭合区域。
  const layerPaths = useMemo(() => {
    if (shape !== "area" || coordinates.length === 0) return [];
    return seriesMeta.map((meta, seriesIndex) => {
      const topPoints = coordinates
        .map((c) => `${c.x},${c.stacks[seriesIndex].yTop}`)
        .join(" ");
      const bottomPoints = coordinates
        .slice()
        .reverse()
        .map((c) => `${c.x},${c.stacks[seriesIndex].yBottom}`)
        .join(" ");
      const d = `M ${topPoints} L ${bottomPoints} Z`;
      return { ...meta, d };
    });
  }, [shape, coordinates, seriesMeta]);

  const gridLines = Array.from({ length: tickIntervals + 1 }, (_, index) => ({
    y: baselineY - (plotHeight * index) / tickIntervals,
    value: tickStep * index,
  }));

  const labelIndexes = useMemo(() => xLabelIndexes(points.length), [points.length]);

  const { hoveredIndex, tooltipPos, onMouseMove, onMouseLeave } = useSeriesHover(
    svgRef,
    coordinates.map((c) => c.x),
    chartWidth,
  );

  const hoveredData = hoveredIndex !== null ? coordinates[hoveredIndex] : null;

  return (
    <div className="relative">
      {/* 测量容器始终渲染：空状态挂载后再出现数据时，ResizeObserver 仍然有效，
          否则 chartWidth 会永久停在默认值，viewBox 缩放问题会回归。 */}
      <div ref={boxRef} className="w-full">
        {isEmpty ? (
          <ChartEmptyState message={emptyMessage} action={emptyAction} />
        ) : (
          <svg
            ref={svgRef}
            viewBox={`0 0 ${chartWidth} ${CHART_HEIGHT}`}
            className="w-full overflow-visible"
            style={{ height: CHART_HEIGHT }}
            role="img"
            aria-label={
              shape === "columns" ? "stacked column chart" : "stacked area chart"
            }
            onMouseMove={onMouseMove}
            onMouseLeave={onMouseLeave}
          >
            {shape === "area" && (
              <defs>
                {seriesMeta.map((meta) => (
                  <linearGradient
                    key={meta.id}
                    id={`grad-${meta.id}`}
                    x1="0"
                    y1="0"
                    x2="0"
                    y2="1"
                  >
                    <stop offset="0%" stopColor={meta.color} stopOpacity="0.6" />
                    <stop
                      offset="100%"
                      stopColor={meta.color}
                      stopOpacity="0.1"
                    />
                  </linearGradient>
                ))}
              </defs>
            )}

            {/* Grid lines */}
            {gridLines.map((line, i) => (
              <g key={`grid-${i}`}>
                <line
                  x1={CHART_PADDING_X}
                  y1={line.y}
                  x2={chartWidth - CHART_PADDING_X}
                  y2={line.y}
                  className="stroke-border/50"
                  strokeWidth="1"
                  strokeDasharray={i === 0 ? undefined : "3 3"}
                />
                <text
                  x={CHART_PADDING_X - 8}
                  y={line.y + 4}
                  textAnchor="end"
                  className="fill-muted-foreground text-[11px]"
                >
                  {formatValue(line.value)}
                </text>
              </g>
            ))}

            {shape === "area"
              ? layerPaths.map((layer) => (
                  <path
                    key={layer.id}
                    d={layer.d}
                    fill={`url(#grad-${layer.id})`}
                    stroke={layer.color}
                    strokeWidth="1.5"
                    strokeLinejoin="round"
                    opacity={hoveredIndex !== null ? 0.85 : 1}
                  />
                ))
              : coordinates.map((point, pointIndex) => (
                  <g key={`col-${pointIndex}`}>
                    {point.stacks.map(
                      (stack) =>
                        stack.value > 0 && (
                          <rect
                            key={stack.id}
                            x={point.x - columnWidth / 2}
                            y={stack.yTop}
                            width={columnWidth}
                            height={Math.max(stack.yBottom - stack.yTop, 0)}
                            fill={stack.color}
                            opacity={
                              hoveredIndex === null || hoveredIndex === pointIndex
                                ? 1
                                : 0.4
                            }
                          />
                        ),
                    )}
                  </g>
                ))}

            {/* Hover indicator line */}
            {hoveredData && (
              <line
                x1={hoveredData.x}
                y1={CHART_PADDING_TOP}
                x2={hoveredData.x}
                y2={baselineY}
                stroke="currentColor"
                strokeWidth="1"
                strokeDasharray="4 4"
                className="text-muted-foreground/40"
              />
            )}

            {/* Data points on top line（面积图：悬浮时标出合计点） */}
            {shape === "area" &&
              coordinates.map((point, index) => (
                <circle
                  key={`pt-${index}`}
                  cx={point.x}
                  cy={point.yTotal}
                  r={hoveredIndex === index ? 5 : 0}
                  className="fill-foreground stroke-background stroke-[2] transition-all duration-150"
                />
              ))}

            {/* X-axis labels：画在图内，和数据点共用同一套坐标，刻度与日期一一对应 */}
            {labelIndexes.map((index) => {
              const point = points[index];
              return (
                <text
                  key={getPointKey(point, index)}
                  x={coordinates[index].x}
                  y={CHART_HEIGHT - 8}
                  textAnchor="middle"
                  className="fill-muted-foreground text-[11px]"
                >
                  {point.shortLabel ?? point.label}
                </text>
              );
            })}
          </svg>
        )}
      </div>

      {/* Tooltip：图表已转成空状态时不能留浮层（数据可能在悬浮中被刷新成全零） */}
      {!isEmpty && hoveredData && tooltipPos && (
        <SeriesTooltip
          datum={hoveredData}
          pos={tooltipPos}
          formatValue={formatValue}
        />
      )}
    </div>
  );
}
