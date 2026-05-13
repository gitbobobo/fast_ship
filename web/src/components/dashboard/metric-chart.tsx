import { useMemo, useRef, useState } from "react";
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
  className?: string;
}

interface MultiSeriesChartProps {
  title?: string;
  description?: string;
  points: MetricChartSeriesPoint[];
  variant: "area";
  emptyMessage: string;
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
  const { title, description, variant, emptyMessage, className } = props;

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
          />
        ) : (
          <StackedAreaChart
            points={(props as MultiSeriesChartProps).points}
            emptyMessage={emptyMessage}
          />
        )}
      </div>
    </div>
  );
}

function getPointKey(point: { label: string; id?: string }, index: number) {
  return point.id ?? `${point.label}-${index}`;
}

/* ─── Horizontal Bar Chart ─── */

function HorizontalBarChart({
  points,
  emptyMessage,
}: {
  points: MetricChartPoint[];
  emptyMessage: string;
}) {
  const [hoveredIndex, setHoveredIndex] = useState<number | null>(null);
  const maxValue = Math.max(...points.map((p) => p.value), 0);
  const chartMax = Math.max(maxValue, 1);
  const barHeight = 24;
  const gap = 14;
  const labelWidth = 96;
  const maxBarWidth = 360;
  const isEmpty = points.length === 0 || maxValue === 0;

  return (
    <div className="space-y-1">
      {points.map((point, index) => {
        const ratio = point.value / chartMax;
        const barWidth = Math.max(ratio * maxBarWidth, 4);
        const isHovered = hoveredIndex === index;
        const isBarEmpty = chartMax <= 1 && point.value === 0;

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
                {!isBarEmpty && (
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
                {isBarEmpty && (
                  <div className="flex h-full items-center px-3">
                    <span className="h-1.5 w-8 rounded-full bg-muted-foreground/15" />
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

      {isEmpty && (
        <div className="flex items-center justify-center py-8">
          <div className="rounded-lg border border-dashed bg-muted/40 px-4 py-3 text-xs text-muted-foreground">
            {emptyMessage}
          </div>
        </div>
      )}
    </div>
  );
}

/* ─── Stacked Area Chart ─── */

const SVG_WIDTH = 720;
const SVG_HEIGHT = 220;
const SVG_PADDING_X = 40;
const SVG_PADDING_Y = 28;

function StackedAreaChart({
  points,
  emptyMessage,
}: {
  points: MetricChartSeriesPoint[];
  emptyMessage: string;
}) {
  const svgRef = useRef<SVGSVGElement>(null);
  const [hoveredIndex, setHoveredIndex] = useState<number | null>(null);
  const [tooltipPos, setTooltipPos] = useState<{ x: number; y: number } | null>(null);

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
  const chartMax = Math.max(maxTotal, 1);
  const isEmpty = points.length === 0 || maxTotal === 0;

  const safeData = stackedData.length === 1
    ? [...stackedData, { ...stackedData[0], index: 1 }]
    : stackedData;

  const plotWidth = SVG_WIDTH - SVG_PADDING_X * 2;
  const plotHeight = SVG_HEIGHT - SVG_PADDING_Y * 2;
  const denominator = Math.max(safeData.length - 1, 1);

  // Compute coordinates for each stack layer
  const coordinates = safeData.map((point, index) => {
    const x = SVG_PADDING_X + (plotWidth * index) / denominator;
    const getY = (value: number) =>
      SVG_HEIGHT - SVG_PADDING_Y - (plotHeight * Math.min(value, chartMax)) / chartMax;
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
  const gridLines = [0, 0.25, 0.5, 0.75, 1].map((ratio) => {
    const y = SVG_HEIGHT - SVG_PADDING_Y - plotHeight * ratio;
    const value = Math.round(chartMax * ratio);
    return { y, value };
  });

  // X-axis labels
  const labelStride = Math.max(Math.ceil(Math.max(points.length - 1, 1) / 6), 1);

  const handleMouseMove = (e: React.MouseEvent<SVGSVGElement>) => {
    if (!svgRef.current) return;
    const rect = svgRef.current.getBoundingClientRect();
    const x = e.clientX - rect.left;
    const svgX = (x / rect.width) * SVG_WIDTH;

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
    setTooltipPos({ x: e.clientX - rect.left, y: e.clientY - rect.top });
  };

  const handleMouseLeave = () => {
    setHoveredIndex(null);
    setTooltipPos(null);
  };

  const hoveredData = hoveredIndex !== null ? coordinates[hoveredIndex] : null;

  return (
    <div className="relative">
      <svg
        ref={svgRef}
        viewBox={`0 0 ${SVG_WIDTH} ${SVG_HEIGHT}`}
        className="h-[240px] w-full overflow-visible"
        role="img"
        aria-label="stacked area chart"
        onMouseMove={handleMouseMove}
        onMouseLeave={handleMouseLeave}
      >
        <defs>
          {seriesMeta.map((meta, i) => (
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
              x1={SVG_PADDING_X}
              y1={line.y}
              x2={SVG_WIDTH - SVG_PADDING_X}
              y2={line.y}
              className="stroke-border/50"
              strokeWidth="1"
              strokeDasharray={i === 0 ? undefined : "3 3"}
            />
            <text
              x={SVG_PADDING_X - 8}
              y={line.y + 4}
              textAnchor="end"
              className="fill-muted-foreground text-[10px]"
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
            y1={SVG_PADDING_Y}
            x2={hoveredData.x}
            y2={SVG_HEIGHT - SVG_PADDING_Y}
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
      </svg>

      {/* X-axis labels */}
      <div className="mt-1 flex flex-wrap items-center justify-between gap-x-2 gap-y-1 px-10 text-[10px] text-muted-foreground">
        {points
          .filter((_, index) => index % labelStride === 0 || index === points.length - 1)
          .map((point, index) => (
            <span key={getPointKey(point, index)} className="min-w-0 truncate">
              {point.shortLabel ?? point.label}
            </span>
          ))}
      </div>

      {/* Tooltip */}
      {hoveredData && tooltipPos && !isEmpty && (
        <div
          className="pointer-events-none absolute z-50 min-w-[160px] rounded-lg border bg-popover px-3 py-2 shadow-lg"
          style={{
            left: Math.min(tooltipPos.x + 16, (svgRef.current?.clientWidth ?? 400) - 170),
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

      {isEmpty && (
        <div className="absolute inset-0 flex items-center justify-center">
          <div className="rounded-lg border border-dashed bg-muted/40 px-4 py-3 text-xs text-muted-foreground">
            {emptyMessage}
          </div>
        </div>
      )}
    </div>
  );
}
