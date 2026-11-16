import {
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
  type RefObject,
} from "react";
import { Link } from "react-router";
import { cn } from "@/lib/utils";

export interface MetricChartPoint {
  id?: string;
  label: string;
  value: number;
  shortLabel?: string;
  /** 行内跳转路径（站内）；有值时整行渲染为链接。 */
  href?: string;
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
  /** 只显示前 N 行，其余折叠成一行汇总（文案由 collapsedLabel 生成）。 */
  maxRows?: number;
  /** 折叠行文案：输入被折掉的点，返回一行说明；不传则静默截断。 */
  collapsedLabel?: (hidden: MetricChartPoint[]) => string;
}

interface MultiSeriesChartProps {
  title?: string;
  description?: string;
  points: MetricChartSeriesPoint[];
  /** area 为堆叠面积图；columns 为按点堆叠的竖向柱状图。 */
  variant: "area" | "columns";
  /** 堆叠语义（默认 true）：各系列是可加分量，面积/柱按累计顶边画。
   *  false 为叠加折线（area 变体）：各序列是独立流量，各自 y=自身值，
   *  线型区分（首序列细虚线、其余实线），线末直接标数值。columns 忽略。 */
  stacked?: boolean;
  /** 单系列图的颜色槽位（默认 0）：多系列图忽略，仍按首现顺序分配。 */
  colorIndex?: number;
  /** area 变体末点 x 正下方轴区的小字（如「今日」）；columns 忽略。 */
  lastPointLabel?: string;
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
            maxRows={(props as MetricChartProps).maxRows}
            collapsedLabel={(props as MetricChartProps).collapsedLabel}
          />
        ) : (
          <StackedSeriesChart
            shape={variant}
            points={(props as MultiSeriesChartProps).points}
            emptyMessage={emptyMessage}
            emptyAction={emptyAction}
            formatValue={props.formatValue}
            stacked={(props as MultiSeriesChartProps).stacked}
            colorIndex={(props as MultiSeriesChartProps).colorIndex}
            lastPointLabel={(props as MultiSeriesChartProps).lastPointLabel}
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
  maxRows,
  collapsedLabel,
}: {
  points: MetricChartPoint[];
  emptyMessage: string;
  emptyAction?: ReactNode;
  formatValue?: MetricChartFormatValue;
  maxRows?: number;
  collapsedLabel?: (hidden: MetricChartPoint[]) => string;
}) {
  const [hoveredIndex, setHoveredIndex] = useState<number | null>(null);
  const maxValue = Math.max(...points.map((p) => p.value), 0);
  const chartMax = Math.max(maxValue, 1);
  const barHeight = 24;
  const labelWidth = 96;
  const maxBarWidth = 360;
  // 只有完全没数据才算空；全是 0 的项目行仍然保留，读者能看到项目本身。
  const isEmpty = points.length === 0;
  const visible =
    maxRows !== undefined && points.length > maxRows
      ? points.slice(0, maxRows)
      : points;
  const hidden = points.slice(visible.length);

  if (isEmpty) {
    return <ChartEmptyState message={emptyMessage} action={emptyAction} />;
  }

  const renderRow = (point: MetricChartPoint, index: number) => {
    const ratio = point.value / chartMax;
    const isHovered = hoveredIndex === index;

    return (
      <>
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
      </>
    );
  };

  const rowClassName = "group flex items-center gap-3";

  return (
    <div className="space-y-1">
      {visible.map((point, index) =>
        // 带 href 的行整行是链接；折叠汇总行永远不可点。
        point.href ? (
          <Link
            key={getPointKey(point, index)}
            to={point.href}
            className={cn(rowClassName, "-mx-1 rounded-md px-1 hover:bg-muted/40")}
            onMouseEnter={() => setHoveredIndex(index)}
            onMouseLeave={() => setHoveredIndex(null)}
          >
            {renderRow(point, index)}
          </Link>
        ) : (
          <div
            key={getPointKey(point, index)}
            className={rowClassName}
            onMouseEnter={() => setHoveredIndex(index)}
            onMouseLeave={() => setHoveredIndex(null)}
          >
            {renderRow(point, index)}
          </div>
        ),
      )}
      {hidden.length > 0 && collapsedLabel && (
        <div className="flex items-center gap-3">
          <div className="shrink-0" style={{ width: labelWidth }} />
          <div className="flex-1 text-xs text-muted-foreground">
            {collapsedLabel(hidden)}
          </div>
        </div>
      )}
    </div>
  );
}

/* ─── Stacked Series Chart（面积 / 柱状共用坐标、刻度、悬浮明细）─── */

const CHART_HEIGHT = 240;
const CHART_PADDING_X = 40;
const CHART_PADDING_TOP = 24;
const CHART_PADDING_BOTTOM = 30;
/** 面积图末点右缘内收（W10；round4 X7 由 6px 提到 12px）：最后一个点与绘图区
 *  右边框脱开，不再视觉上贴边，也给线末数值标签（X3）留出落脚空间。
 *  columns 变体按槽位居中、本就内收，坐标计算不受此值影响。 */
const AREA_END_INSET = 12;

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

/** 多系列静态图例：色块 + 系列名，与悬浮 tooltip 用同一配色分配（系列顺序即首次出现序）。 */
export function MetricChartLegend({
  points,
}: {
  points: MetricChartSeriesPoint[];
}) {
  const seriesMeta = useSeriesMeta(points);
  if (seriesMeta.length <= 1) return null;
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
      {seriesMeta.map((meta) => (
        <span
          key={meta.id}
          className="inline-flex items-center gap-1.5 text-xs text-muted-foreground"
        >
          <span
            className="inline-block h-2 w-2 shrink-0 rounded-full"
            style={{ backgroundColor: meta.color }}
          />
          <span className="max-w-40 truncate">{meta.name}</span>
        </span>
      ))}
    </div>
  );
}

/** 系列清单按首次出现次序分配颜色：同一 id 跨点归入同一层。
 *  colorIndex 只对单系列图生效（X2）：如「每日已解决」用第 1 槽（绿）与
 *  流转效率卡的已解决同色；多系列图忽略该参数（bot 用量页零变化）。 */
function useSeriesMeta(
  points: MetricChartSeriesPoint[],
  colorIndex = 0,
): SeriesMeta[] {
  return useMemo(() => {
    const seen = new Map<string, string>();
    for (const point of points) {
      for (const s of point.series) {
        if (!seen.has(s.id)) {
          seen.set(s.id, s.name);
        }
      }
    }
    return Array.from(seen.entries()).map(([id, name], index) => {
      const slot =
        seen.size === 1 && colorIndex > 0 ? colorIndex : index;
      return {
        id,
        name,
        color: CHART_COLORS[slot % CHART_COLORS.length],
      };
    });
  }, [points, colorIndex]);
}

/** 每个点按系列清单累出堆叠区间：缺的系列补 0，层序与颜色分配一致。
 *  叠加模式（X1）各序列是独立流量：bottom 恒 0、top=自身值，total 取该点
 *  各系列值的最大者（只用于 y 轴缩放；tooltip 不再报「总计」）。 */
function buildStackedData(
  points: MetricChartSeriesPoint[],
  seriesMeta: SeriesMeta[],
  overlay: boolean,
): StackedDatum[] {
  return points.map((point) => {
    let cumulative = 0;
    let maxValue = 0;
    const stacks = seriesMeta.map((meta) => {
      const s = point.series.find((x) => x.id === meta.id);
      const value = s?.value ?? 0;
      cumulative += value;
      maxValue = Math.max(maxValue, value);
      return {
        ...meta,
        value,
        bottom: overlay ? 0 : cumulative - value,
        top: overlay ? value : cumulative,
      };
    });
    return {
      label: point.label,
      shortLabel: point.shortLabel,
      total: overlay ? maxValue : cumulative,
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
 *  是 4/9/14/19/24/29），不要在中间插一个不均匀的间隔。步长取两档的较大者：
 *  常规每 6 点一档；窄图（如详情面板内嵌）里相邻标签不足约 48px 时进一步
 *  加稀，避免 7 天全标签连排成「10-1910-20…」。本图按槽位均分布点，下标
 *  间距即像素间距；按真实时间布局的图（bot-usage-insights）用像素抽稀。 */
function xLabelIndexes(count: number, chartWidth: number): number[] {
  const last = count - 1;
  if (last < 1) return count === 1 ? [0] : [];
  const stride = Math.max(
    Math.ceil(last / 6),
    Math.ceil((48 * last) / Math.max(chartWidth, 1)),
    1,
  );
  const indexes: number[] = [];
  for (let index = last; index >= 0; index -= stride) indexes.push(index);
  return indexes.reverse();
}

/** 用实测宽度画图（1:1 像素坐标）：viewBox 缩放会把坐标轴文字一起缩小，
 *  也会让横轴日期和数据点错位。同风格的散点/折线图（bot-usage-insights）复用。 */
export function useChartWidth() {
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

/** 悬浮明细：该点各系列取值（倒序，视觉最上层先列）与总计；
 *  叠加模式（X1）各序列是独立流量，没有可加的「总计」，不显示该行。 */
function SeriesTooltip({
  datum,
  pos,
  formatValue,
  overlay,
}: {
  datum: {
    label: string;
    total: number;
    stacks: (SeriesMeta & { value: number })[];
  };
  pos: { x: number; y: number; containerWidth: number };
  formatValue: MetricChartFormatValue;
  overlay: boolean;
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
        {!overlay && datum.total > 0 && (
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
  stacked = true,
  colorIndex = 0,
  lastPointLabel,
}: {
  /** area 渲染堆叠面积层（stacked=false 时为叠加折线），columns 渲染每点一根堆叠柱。 */
  shape: "area" | "columns";
  points: MetricChartSeriesPoint[];
  emptyMessage: string;
  emptyAction?: ReactNode;
  formatValue?: MetricChartFormatValue;
  /** 默认 true（堆叠，bot 用量页语义不变）；false 仅对 area 生效为叠加折线。 */
  stacked?: boolean;
  colorIndex?: number;
  lastPointLabel?: string;
}) {
  const svgRef = useRef<SVGSVGElement>(null);
  const { boxRef, chartWidth } = useChartWidth();

  // 叠加折线只定义在 area 变体上：columns（bot 用量页）永远按堆叠画。
  const overlay = !stacked && shape === "area";
  const seriesMeta = useSeriesMeta(points, colorIndex);
  const stackedData = useMemo(
    () => buildStackedData(points, seriesMeta, overlay),
    [points, seriesMeta, overlay],
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
        : // 面积图只有末点内收 6px（W10），中间点坐标不变：视觉差异最小。
          CHART_PADDING_X +
          ((plotWidth - (index === shapedData.length - 1 ? AREA_END_INSET : 0)) *
            index) /
            denominator,
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
    if (shape !== "area" || overlay || coordinates.length === 0) return [];
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
  }, [shape, overlay, coordinates, seriesMeta]);

  // 叠加折线路径（X1）：每条序列独立连线，首序列细虚线、其余实线，
  // 末段重合时仍能分辨两条线。
  const overlayLines = useMemo(() => {
    if (!overlay || coordinates.length === 0) return [];
    return seriesMeta.map((meta, seriesIndex) => ({
      ...meta,
      dashed: seriesIndex === 0,
      d: coordinates
        .map(
          (c, i) =>
            `${i === 0 ? "M" : "L"} ${c.x} ${c.stacks[seriesIndex].yTop}`,
        )
        .join(" "),
    }));
  }, [overlay, coordinates, seriesMeta]);

  const gridLines = Array.from({ length: tickIntervals + 1 }, (_, index) => ({
    y: baselineY - (plotHeight * index) / tickIntervals,
    value: tickStep * index,
  }));

  const labelIndexes = useMemo(
    () => xLabelIndexes(points.length, chartWidth),
    [points.length, chartWidth],
  );

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
              shape === "columns"
                ? "stacked column chart"
                : overlay
                  ? "overlay line chart"
                  : "stacked area chart"
            }
            onMouseMove={onMouseMove}
            onMouseLeave={onMouseLeave}
          >
            {shape === "area" && !overlay && (
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

            {shape === "area" && overlay
              ? overlayLines.map((line) => (
                  <path
                    key={line.id}
                    data-series={line.id}
                    d={line.d}
                    fill="none"
                    stroke={line.color}
                    strokeWidth="1.5"
                    strokeLinejoin="round"
                    strokeLinecap="round"
                    strokeDasharray={line.dashed ? "6 4" : undefined}
                    opacity={hoveredIndex !== null ? 0.85 : 1}
                  />
                ))
              : shape === "area"
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
                    {point.stacks.map((stack) => {
                      if (stack.value <= 0) return null;
                      // 非零分段至少 2px：小额系列被大额压到亚像素时仍可见；
                      // 贴基线的分段改为自基线向上，避免压过轴线。
                      const top = Math.min(stack.yTop, baselineY - 2);
                      const height = Math.max(stack.yBottom - stack.yTop, 2);
                      return (
                        <rect
                          key={stack.id}
                          x={point.x - columnWidth / 2}
                          y={top}
                          width={columnWidth}
                          height={height}
                          fill={stack.color}
                          opacity={
                            hoveredIndex === null || hoveredIndex === pointIndex
                              ? 1
                              : 0.4
                          }
                        />
                      );
                    })}
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

            {/* Data points on top line（面积图：悬浮时标点；叠加模式标在各序列线上） */}
            {shape === "area" && overlay
              ? coordinates.map((point, index) =>
                  point.stacks.map((stack) => (
                    <circle
                      key={`pt-${index}-${stack.id}`}
                      cx={point.x}
                      cy={stack.yTop}
                      r={hoveredIndex === index ? 4 : 0}
                      fill={stack.color}
                      className="stroke-background stroke-[2] transition-all duration-150"
                    />
                  )),
                )
              : shape === "area" &&
                coordinates.map((point, index) => (
                  <circle
                    key={`pt-${index}`}
                    cx={point.x}
                    cy={point.yTotal}
                    r={hoveredIndex === index ? 5 : 0}
                    className="fill-foreground stroke-background stroke-[2] transition-all duration-150"
                  />
                ))}

            {/* 线末数值标签（X3，叠加模式）：两条线末端右上/右下错开。
                字用中性深灰（round5 视觉：系列色原样白底对比度不足 AA，
                且这是全图唯一精确读数），前置系列色小点保留颜色关联。 */}
            {overlay &&
              coordinates.length > 0 &&
              seriesMeta.map((meta, seriesIndex) => {
                const last = coordinates[coordinates.length - 1];
                const stack = last.stacks[seriesIndex];
                return (
                  <g key={`end-label-${meta.id}`} data-series={meta.id}>
                    <circle
                      cx={last.x + 8}
                      cy={stack.yTop + (seriesIndex % 2 === 0 ? 8 : -6)}
                      r={3}
                      fill={meta.color}
                    />
                    <text
                      x={last.x + 14}
                      y={stack.yTop + (seriesIndex % 2 === 0 ? 8 : -6)}
                      dominantBaseline="middle"
                      className="fill-gray-700 tabular-nums text-[11px] font-semibold dark:fill-gray-300"
                    >
                      {formatValue(stack.value)}
                    </text>
                  </g>
                );
              })}

            {/* （lastPointLabel 已并入末刻度文字，不再单独渲染） */}

            {/* X-axis labels：画在图内，和数据点共用同一套坐标，刻度与日期一一对应；
                lastPointLabel 并入末刻度（round5 视觉：与「今日」单字堆叠难读） */}
            {labelIndexes.map((index) => {
              const point = points[index];
              const isLast = index === points.length - 1;
              const text =
                isLast && lastPointLabel
                  ? `${point.shortLabel ?? point.label}·${lastPointLabel}`
                  : (point.shortLabel ?? point.label);
              return (
                <text
                  key={getPointKey(point, index)}
                  x={coordinates[index].x}
                  y={CHART_HEIGHT - 8}
                  textAnchor="middle"
                  className="fill-muted-foreground text-[11px]"
                >
                  {text}
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
          overlay={overlay}
        />
      )}
    </div>
  );
}
