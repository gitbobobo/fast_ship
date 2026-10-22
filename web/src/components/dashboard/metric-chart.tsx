import {
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
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

interface MetricChartProps {
  title?: string;
  description?: string;
  points: MetricChartPoint[];
  variant: "bar";
  emptyMessage: string;
  emptyAction?: ReactNode;
  className?: string;
}

interface MultiSeriesChartProps {
  title?: string;
  description?: string;
  points: MetricChartSeriesPoint[];
  variant: "area";
  emptyMessage: string;
  emptyAction?: ReactNode;
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
          />
        ) : (
          <StackedAreaChart
            points={(props as MultiSeriesChartProps).points}
            emptyMessage={emptyMessage}
            emptyAction={emptyAction}
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
}: {
  points: MetricChartPoint[];
  emptyMessage: string;
  emptyAction?: ReactNode;
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
              {point.value}
            </div>
          </div>
        );
      })}
    </div>
  );
}

/* ─── Stacked Area Chart ─── */

const CHART_HEIGHT = 240;
const CHART_PADDING_X = 40;
const CHART_PADDING_TOP = 24;
const CHART_PADDING_BOTTOM = 30;

function StackedAreaChart({
  points,
  emptyMessage,
  emptyAction,
}: {
  points: MetricChartSeriesPoint[];
  emptyMessage: string;
  emptyAction?: ReactNode;
}) {
  const svgRef = useRef<SVGSVGElement>(null);
  const boxRef = useRef<HTMLDivElement>(null);
  // 用实测宽度画图（1:1 像素坐标）：viewBox 缩放会把坐标轴文字一起缩小，
  // 也会让横轴日期和数据点错位。
  const [chartWidth, setChartWidth] = useState(720);
  const [hoveredIndex, setHoveredIndex] = useState<number | null>(null);
  const [tooltipPos, setTooltipPos] = useState<{ x: number; y: number; containerWidth: number } | null>(null);

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

  // Extract all unique series IDs and names
  const seriesMeta = useMemo(() => {
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

  // Build stacked values for each point
  const stackedData = useMemo(() => {
    return points.map((point, pointIndex) => {
      let cumulative = 0;
      const stacks = seriesMeta.map((meta) => {
        const s = point.series.find((x) => x.id === meta.id);
        const value = s?.value ?? 0;
        const bottom = cumulative;
        cumulative += value;
        return {
          ...meta,
          value,
          bottom,
          top: cumulative,
        };
      });
      return {
        label: point.label,
        shortLabel: point.shortLabel,
        total: cumulative,
        stacks,
        index: pointIndex,
      };
    });
  }, [points, seriesMeta]);

  const maxTotal = Math.max(...stackedData.map((d) => d.total), 0);
  // 纵轴用整数阶梯：最大值较小时只分两档（0/1/2），峰値不至于只占四分之一高；
  // 其余情况分四档，每档至少 1，避免出现 1、1、1、0、0 这种重复刻度。
  const tickIntervals = maxTotal <= 2 ? 2 : 4;
  const tickStep = Math.max(Math.ceil(maxTotal / tickIntervals), 1);
  const chartMax = tickStep * tickIntervals;
  const isEmpty = points.length === 0 || maxTotal === 0;

  const safeData = stackedData.length === 1
    ? [...stackedData, { ...stackedData[0], index: 1 }]
    : stackedData;

  const plotWidth = chartWidth - CHART_PADDING_X * 2;
  const baselineY = CHART_HEIGHT - CHART_PADDING_BOTTOM;
  const plotHeight = baselineY - CHART_PADDING_TOP;
  const denominator = Math.max(safeData.length - 1, 1);

  // Compute coordinates for each stack layer
  const coordinates = safeData.map((point, index) => {
    const x = CHART_PADDING_X + (plotWidth * index) / denominator;
    const getY = (value: number) =>
      baselineY - (plotHeight * Math.min(value, chartMax)) / chartMax;
    return {
      x,
      yTotal: getY(point.total),
      stacks: point.stacks.map((stack) => ({
        ...stack,
        yBottom: getY(stack.bottom),
        yTop: getY(stack.top),
      })),
      label: point.label,
      shortLabel: point.shortLabel,
      total: point.total,
    };
  });

  // Build paths for each stack layer
  const layerPaths = useMemo(() => {
    if (coordinates.length === 0) return [];
    return seriesMeta.map((meta, seriesIndex) => {
      // Top line: left to right
      const topPoints = coordinates.map((c) => `${c.x},${c.stacks[seriesIndex].yTop}`).join(" ");
      // Bottom line: right to left
      const bottomPoints = coordinates
        .slice()
        .reverse()
        .map((c) => `${c.x},${c.stacks[seriesIndex].yBottom}`)
        .join(" ");

      const d = `M ${topPoints} L ${bottomPoints} Z`;
      return { ...meta, d };
    });
  }, [coordinates, seriesMeta]);

  // Grid lines
  const gridLines = Array.from({ length: tickIntervals + 1 }, (_, index) => ({
    y: baselineY - (plotHeight * index) / tickIntervals,
    value: tickStep * index,
  }));

  // X-axis labels：均匀取最多 7 个刻度，首尾都在，避免末尾日期与前一个刻度叠印
  // （按 stride 取点再强塞最后一个点时，27 个数据点会让 25/26 两个 MM-DD 相距约 17px）。
  const labelIndexes = useMemo(() => {
    const last = points.length - 1;
    if (last < 1) return points.length === 1 ? [0] : [];
    const count = Math.min(7, points.length);
    const indexes: number[] = [];
    for (let i = 0; i < count; i += 1) {
      const index = Math.round((last * i) / (count - 1));
      if (indexes[indexes.length - 1] !== index) indexes.push(index);
    }
    return indexes;
  }, [points.length]);

  const handleMouseMove = (e: React.MouseEvent<SVGSVGElement>) => {
    if (!svgRef.current) return;
    const rect = svgRef.current.getBoundingClientRect();
    const x = e.clientX - rect.left;
    const svgX = (x / rect.width) * chartWidth;

    // Find nearest point
    let nearestIndex = 0;
    let minDist = Infinity;
    for (let i = 0; i < coordinates.length; i++) {
      const dist = Math.abs(coordinates[i].x - svgX);
      if (dist < minDist) {
        minDist = dist;
        nearestIndex = i;
      }
    }

    setHoveredIndex(nearestIndex);
    setTooltipPos({ x: e.clientX - rect.left, y: e.clientY - rect.top, containerWidth: rect.width });
  };

  const handleMouseLeave = () => {
    setHoveredIndex(null);
    setTooltipPos(null);
  };

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
            aria-label="stacked area chart"
            onMouseMove={handleMouseMove}
            onMouseLeave={handleMouseLeave}
          >
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
              <stop offset="100%" stopColor={meta.color} stopOpacity="0.1" />
            </linearGradient>
          ))}
        </defs>

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
              {line.value}
            </text>
          </g>
        ))}

        {/* Stacked area layers */}
        {layerPaths.map((layer) => (
          <path
            key={layer.id}
            d={layer.d}
            fill={`url(#grad-${layer.id})`}
            stroke={layer.color}
            strokeWidth="1.5"
            strokeLinejoin="round"
            opacity={hoveredIndex !== null ? 0.85 : 1}
          />
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

        {/* Data points on top line */}
        {coordinates.map((point, index) => (
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
        <div
          className="pointer-events-none absolute z-50 min-w-[160px] rounded-lg border bg-popover px-3 py-2 shadow-lg"
          style={{
            left: Math.min(tooltipPos.x + 16, (tooltipPos.containerWidth ?? 400) - 170),
            top: Math.max(tooltipPos.y - 10, 0),
          }}
        >
          <div className="mb-1.5 text-xs font-medium text-popover-foreground">
            {hoveredData.label}
          </div>
          <div className="space-y-1">
            {hoveredData.stacks
              .filter((s) => s.value > 0)
              .slice()
              .reverse()
              .map((stack) => (
                <div key={stack.id} className="flex items-center justify-between gap-4">
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
                    {stack.value}
                  </span>
                </div>
              ))}
            {hoveredData.total > 0 && (
              <div className="mt-1 flex items-center justify-between gap-4 border-t border-border/50 pt-1">
                <span className="text-[11px] text-muted-foreground">总计</span>
                <span className="text-[11px] font-bold tabular-nums text-popover-foreground">
                  {hoveredData.total}
                </span>
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  );
}
