import { useEffect, useMemo, useRef, useState } from "react";
import { Link } from "react-router";
import { TriangleAlert } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  MetricChart,
  MetricChartLegend,
  type MetricChartSeriesPoint,
} from "@/components/dashboard/metric-chart";
import { errorText } from "@/lib/api/client";
import type {
  BotUsageBot,
  BotUsageDay,
  BotUsageOverview,
  BotUsageRange,
  BotUsageScheduleTask,
} from "@/lib/api/bots";
import { useBotUsage } from "@/lib/hooks/use-bots";
import { cn } from "@/lib/utils";
import { formatCostEstimate } from "./bot-metrics";
import { BotUsageInsightsPanel } from "./bot-usage-insights";

/**
 * 花费总览（INT-162）：范围切换 + 总计卡 + 按天柱状图 + 按机器人表 +
 * 按定时任务表。总览页传全部数据，机器人详情「花费」标签传 botId 只看
 * 单个机器人（embedded 版式把宽表换成紧凑清单）。金额一律为目录估算价：
 * 可空字段是「未计价」显示「—」，显式 $0 是真实的免费/零价。
 */

export const USAGE_RANGE_OPTIONS: { value: BotUsageRange; label: string }[] = [
  { value: "today", label: "今天" },
  { value: "7d", label: "近 7 天" },
  { value: "30d", label: "近 30 天" },
];

/** 计价口径说明：整页只写一次，两个挂载点共用。 */
export const USAGE_PRICE_NOTE =
  "按内置目录单价估算，可能与实际账单不同（不含折扣、套餐、最低消费）";

/** 未计价常见原因：与契约 unpriced_requests 的口径注释一致。 */
export const UNPRICED_REASON_NOTE =
  "常见原因：自定义提供商、目录无价、请求缺用量数据或旧版本记录";

/** P90/次 口径：与 avg/max 同一已计价 run 集合（INT-163 加入该列）。 */
const P90_NOTE =
  "单次运行显示成本的 P90（nearest-rank），与平均/最高同一已计价 run 集合";

/** 可空金额：未计价显示「—」，真实 $0（免费模型）照常显示。 */
export function formatUsageCost(value: number | null | undefined): string {
  return value == null ? "—" : formatCostEstimate(value);
}

/**
 * 缓存命中率：0–1 小数转整百分数；无完整 usage 为 null → 「—」。
 * 0 < rate < 0.005 四舍五入会得到 0%，与真零无法区分 → 「<1%」。
 */
export function formatCacheHitRate(rate: number | null | undefined): string {
  if (rate == null) return "—";
  if (rate > 0 && rate < 0.005) return "<1%";
  return `${Math.round(rate * 100)}%`;
}

/** 定时任务显示名：已删除任务名恒为空串，统一落「已删除的任务」。 */
export function usageTaskName(task: BotUsageScheduleTask): string {
  return task.deleted || !task.name ? "已删除的任务" : task.name;
}

/**
 * 定时任务「总花费」口径：自身已计价 + 连带主线（近似），与服务端排序键
 * （cost + linked_main_cost）一致；两者都未计价时为 null（显示「—」）。
 * 连带部分仍在单独的「连带主线≈」列展示。
 */
export function taskTotalCost(task: BotUsageScheduleTask): number | null {
  if (task.cost == null && task.linked_main_cost == null) return null;
  return (task.cost ?? 0) + (task.linked_main_cost ?? 0);
}

/**
 * 按天柱状图的多系列点：每天一根堆叠柱、每个机器人一层颜色。机器人名取
 * 总览 bots 表；窗口内有运行但已不在表里的（极端情况）退化为 bot_id 展示。
 */
export function buildDailyCostPoints(
  days: BotUsageDay[],
  bots: BotUsageBot[],
): MetricChartSeriesPoint[] {
  const names = new Map(bots.map((b) => [b.bot_id, b.name]));
  return days.map((day) => ({
    label: day.date,
    shortLabel: day.date.slice(5),
    value: day.cost,
    series: day.bots.map((b) => ({
      id: b.bot_id,
      name: names.get(b.bot_id) ?? b.bot_id,
      value: b.cost,
    })),
  }));
}

/** 未计价占比：无请求时返回 null（显示「—」而非 0%）。 */
export function unpricedShare(totals: BotUsageOverview["totals"]): {
  unpriced: number;
  total: number;
  percent: number;
} | null {
  if (totals.total_requests <= 0) return null;
  return {
    unpriced: totals.unpriced_requests,
    total: totals.total_requests,
    percent: Math.round(
      (totals.unpriced_requests / totals.total_requests) * 100,
    ),
  };
}

function StatCard({
  label,
  value,
  sub,
  title,
}: {
  label: string;
  value: string;
  sub?: string;
  title?: string;
}) {
  return (
    <div
      className="rounded-xl border border-border/60 px-4 py-3"
      title={title}
    >
      {/* 标签是固定短语，窄面板（内嵌详情页）里 truncate 会把「未计价请求」
          裁成「未计价…」；短文案直接完整展示。 */}
      <p className="text-xs text-muted-foreground">{label}</p>
      <p className="mt-1 text-xl font-semibold tabular-nums">{value}</p>
      {sub && <p className="mt-0.5 text-xs text-muted-foreground">{sub}</p>}
    </div>
  );
}

function CostCell({ value }: { value: number | null | undefined }) {
  return (
    <TableCell className="tabular-nums">{formatUsageCost(value)}</TableCell>
  );
}

export function BotUsageOverviewPanel({
  botId,
  focusTaskId,
  embedded,
}: {
  /** 限定单个机器人（详情「花费」标签）；缺省为全部机器人总览。 */
  botId?: string;
  /** 定位参数：定位并高亮某条定时任务行（如 ?task=<taskId>）。 */
  focusTaskId?: string | null;
  /** 窄栏嵌入版式：宽表换成紧凑清单（详情右栏 w-80）。 */
  embedded?: boolean;
}) {
  const [range, setRange] = useState<BotUsageRange>("7d");
  const { data, isLoading, isError, error, refetch } = useBotUsage(
    range,
    botId,
  );

  const dailyPoints = useMemo(
    () => (data ? buildDailyCostPoints(data.days, data.bots) : []),
    [data],
  );

  // 定位参数指向的任务行：渲染完成后滚进视野并短暂高亮。usage 会被多处
  // 失效重取（run 终态、任务开关、窗口聚焦），同一 focusTaskId 只滚一次，
  // 不把已滚离的用户反复拽回；行还没渲染出来时不记账，等下次数据到位再试。
  const lastFocusedRef = useRef<string | null>(null);
  useEffect(() => {
    if (!focusTaskId || lastFocusedRef.current === focusTaskId) return;
    const el = document.querySelector(
      `[data-usage-task="${CSS.escape(focusTaskId)}"]`,
    );
    if (!el) return;
    el.scrollIntoView({ block: "center" });
    lastFocusedRef.current = focusTaskId;
  }, [focusTaskId, data]);

  const share = data ? unpricedShare(data.totals) : null;
  const singleBot = data?.bots.length === 1 ? data.bots[0] : null;

  return (
    <div
      className={cn("space-y-5", embedded && "pb-4")}
      data-testid="bot-usage-overview"
    >
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <div
          role="group"
          aria-label="统计范围"
          className="inline-flex rounded-lg bg-muted p-[3px]"
        >
          {USAGE_RANGE_OPTIONS.map((option) => (
            <button
              key={option.value}
              type="button"
              aria-pressed={range === option.value}
              data-testid={`usage-range-${option.value}`}
              onClick={() => setRange(option.value)}
              className={cn(
                "h-7 rounded-md px-3 text-xs font-medium transition-colors",
                range === option.value
                  ? "bg-background text-foreground shadow-sm"
                  : "text-muted-foreground hover:text-foreground",
              )}
            >
              {option.label}
            </button>
          ))}
        </div>
        <p className="text-xs text-muted-foreground">{USAGE_PRICE_NOTE}</p>
      </div>

      {isLoading ? (
        <div className="space-y-3">
          <Skeleton className="h-20 rounded-xl" />
          <Skeleton className="h-60 rounded-xl" />
        </div>
      ) : isError ? (
        <div className="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-destructive/40 bg-destructive/[0.06] px-4 py-3">
          <div className="flex items-center gap-2 text-sm text-destructive">
            <TriangleAlert className="h-4 w-4 shrink-0" />
            {errorText(error, "花费数据加载失败")}
          </div>
          <Button size="sm" variant="outline" onClick={() => void refetch()}>
            重试
          </Button>
        </div>
      ) : !data ? null : (
        <>
          <div className="grid gap-3 sm:grid-cols-3">
            <StatCard
              label="总花费"
              value={formatUsageCost(data.totals.cost)}
              sub={`${data.totals.total_requests} 次模型请求`}
              title={USAGE_PRICE_NOTE}
            />
            <StatCard
              label="日均花费"
              value={formatUsageCost(data.totals.daily_average)}
              title={USAGE_PRICE_NOTE}
            />
            <StatCard
              label="未计价"
              value={share ? `${share.unpriced} 次` : "—"}
              sub={share ? `占 ${share.percent}%` : undefined}
              title={UNPRICED_REASON_NOTE}
            />
          </div>
          {share != null && share.unpriced > 0 && (
            <p className="text-xs text-muted-foreground">
              {UNPRICED_REASON_NOTE}，未计价不计入金额。
            </p>
          )}

          <section className="space-y-2">
            <div className="flex items-baseline justify-between gap-3">
              <h3 className="text-sm font-medium">按天</h3>
              <MetricChartLegend points={dailyPoints} />
            </div>
            <MetricChart
              points={dailyPoints}
              variant="columns"
              formatValue={formatCostEstimate}
              emptyMessage="窗口内没有已计价花费"
            />
          </section>

          {embedded && data.bots.length <= 1 ? (
            singleBot ? (
              <CompactBotUsage bot={singleBot} />
            ) : (
              <p className="rounded-lg border bg-muted/40 py-6 text-center text-sm text-muted-foreground">
                窗口内没有运行记录
              </p>
            )
          ) : (
            <BotsUsageTable bots={data.bots} />
          )}

          {embedded ? (
            <CompactTaskUsage tasks={data.schedule_tasks} focusTaskId={focusTaskId} />
          ) : (
            <TasksUsageTable
              tasks={data.schedule_tasks}
              focusTaskId={focusTaskId}
            />
          )}

          {/* 优化参考（INT-163）：与总览同一 range/botId，在按定时任务之后。 */}
          <BotUsageInsightsPanel range={range} botId={botId} embedded={embedded} />
        </>
      )}
    </div>
  );
}

/** 按机器人表格：三列成本拆分 + 合计 + 次数/均值/峰值 + 缓存命中率 + 未计价。 */
function BotsUsageTable({ bots }: { bots: BotUsageBot[] }) {
  return (
    <section className="space-y-2" data-testid="usage-bots-section">
      <h3 className="text-sm font-medium">按机器人</h3>
      {!bots.length ? (
        <p className="rounded-lg border bg-muted/40 py-8 text-center text-sm text-muted-foreground">
          窗口内没有运行记录
        </p>
      ) : (
        <div className="overflow-hidden rounded-lg border [&_td:last-child]:pr-3 [&_th:last-child]:pr-3">
          {/* 末列补到 12px 右内边距：通宽表格右缘留白与左侧对齐（同 TableFrame） */}
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>机器人</TableHead>
                <TableHead>主线</TableHead>
                <TableHead title="定时任务运行及连带主线的已计价成本（近似）">
                  定时任务
                </TableHead>
                <TableHead>维护</TableHead>
                <TableHead>合计</TableHead>
                <TableHead className="text-right">运行</TableHead>
                <TableHead>平均/次</TableHead>
                <TableHead title={P90_NOTE}>P90/次</TableHead>
                <TableHead>最高/次</TableHead>
                <TableHead>缓存命中率</TableHead>
                <TableHead className="text-right">未计价</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {bots.map((bot) => (
                <TableRow key={bot.bot_id} data-testid="usage-bot-row">
                  <TableCell className="font-medium">
                    <Link
                      to={`/bots/${bot.bot_id}`}
                      className="hover:underline"
                    >
                      {bot.name}
                    </Link>
                  </TableCell>
                  <CostCell value={bot.cost_main} />
                  <TableCell
                    className="tabular-nums"
                    title={
                      bot.cost_schedule_linked_approx != null &&
                      bot.cost_schedule_linked_approx > 0
                        ? `含连带主线成本（近似）${formatCostEstimate(bot.cost_schedule_linked_approx)}`
                        : undefined
                    }
                  >
                    {formatUsageCost(bot.cost_schedule)}
                  </TableCell>
                  <CostCell value={bot.cost_maintenance} />
                  <CostCell value={bot.cost} />
                  <TableCell className="text-right tabular-nums">
                    {bot.runs}
                  </TableCell>
                  <CostCell value={bot.avg_run_cost} />
                  <CostCell value={bot.p90_run_cost} />
                  <CostCell value={bot.max_run_cost} />
                  <TableCell className="tabular-nums">
                    {formatCacheHitRate(bot.cache_hit_rate)}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {bot.unpriced_requests}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}
    </section>
  );
}

/** 单机器人紧凑拆分（详情「花费」标签）：三桶成本 + 关键指标一行。 */
function CompactBotUsage({ bot }: { bot: BotUsageBot }) {
  const rows: { label: string; value: number | null | undefined; title?: string }[] = [
    { label: "主线", value: bot.cost_main },
    {
      label: "定时任务",
      value: bot.cost_schedule,
      title:
        bot.cost_schedule_linked_approx != null &&
        bot.cost_schedule_linked_approx > 0
          ? `含连带主线成本（近似）${formatCostEstimate(bot.cost_schedule_linked_approx)}`
          : undefined,
    },
    { label: "维护", value: bot.cost_maintenance },
    { label: "合计", value: bot.cost },
  ];
  return (
    <section className="space-y-2" data-testid="usage-bots-section">
      <h3 className="text-sm font-medium">成本构成</h3>
      <dl className="rounded-lg border">
        {rows.map((row, i) => (
          <div
            key={row.label}
            className={cn(
              "flex items-center justify-between px-3 py-2 text-sm",
              i > 0 && "border-t",
            )}
            title={row.title}
          >
            <dt className="text-muted-foreground">{row.label}</dt>
            <dd className="tabular-nums">{formatUsageCost(row.value)}</dd>
          </div>
        ))}
      </dl>
      {/* pb-4：末行嵌在面板滚动容器里，无留白时滚到底文字贴视口底边。 */}
      <p className="pb-4 text-xs text-muted-foreground">
        {bot.runs} 次运行 · 平均 {formatUsageCost(bot.avg_run_cost)} · P90{" "}
        {formatUsageCost(bot.p90_run_cost)} · 最高{" "}
        {formatUsageCost(bot.max_run_cost)} · 缓存命中率{" "}
        {formatCacheHitRate(bot.cache_hit_rate)} · 未计价{" "}
        {bot.unpriced_requests} 次
      </p>
    </section>
  );
}

function taskFocusClass(focused: boolean) {
  return cn(focused && "bg-primary/5 ring-1 ring-inset ring-primary/30");
}

/** 按定时任务表格；focusTaskId 命中的行滚入视野并高亮。 */
function TasksUsageTable({
  tasks,
  focusTaskId,
}: {
  tasks: BotUsageScheduleTask[];
  focusTaskId?: string | null;
}) {
  return (
    <section className="space-y-2" data-testid="usage-tasks-section">
      <h3 className="text-sm font-medium">按定时任务</h3>
      {!tasks.length ? (
        <p className="rounded-lg border bg-muted/40 py-8 text-center text-sm text-muted-foreground">
          窗口内没有定时任务运行
        </p>
      ) : (
        <div className="overflow-hidden rounded-lg border [&_td:last-child]:pr-3 [&_th:last-child]:pr-3">
          {/* 末列补到 12px 右内边距：通宽表格右缘留白与左侧对齐（同 TableFrame） */}
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>任务</TableHead>
                <TableHead>机器人</TableHead>
                <TableHead className="text-right">运行</TableHead>
                <TableHead className="text-right">失败</TableHead>
                <TableHead className="text-right">跳过</TableHead>
                <TableHead title="定时任务运行已计价成本 + 连带主线成本（近似），与服务端排序口径一致">
                  总花费
                </TableHead>
                <TableHead>平均/次</TableHead>
                <TableHead title={P90_NOTE}>P90/次</TableHead>
                <TableHead>最高/次</TableHead>
                <TableHead title="定时任务运行结果写回主线时的连带成本（近似）">
                  连带主线≈
                </TableHead>
                <TableHead title="按单次平均（含连带主线摊销）与计划频率推算的未来 30 天成本">
                  推算月花费
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {tasks.map((task) => (
                <TableRow
                  key={task.task_id}
                  data-testid="usage-task-row"
                  data-usage-task={task.task_id}
                  className={taskFocusClass(task.task_id === focusTaskId)}
                >
                  <TableCell className="max-w-40 font-medium whitespace-normal">
                    <span
                      className={cn(task.deleted && "text-muted-foreground")}
                    >
                      {usageTaskName(task)}
                    </span>
                    {!task.enabled && !task.deleted && (
                      <Badge variant="secondary" className="ml-1.5">
                        已停用
                      </Badge>
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
                  <TableCell className="text-right tabular-nums">
                    {task.runs}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {task.failed}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">
                    {task.skipped}
                  </TableCell>
                  <CostCell value={taskTotalCost(task)} />
                  <CostCell value={task.avg_run_cost} />
                  <CostCell value={task.p90_run_cost} />
                  <CostCell value={task.max_run_cost} />
                  <CostCell value={task.linked_main_cost} />
                  <CostCell value={task.estimated_monthly_cost} />
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}
    </section>
  );
}

/** 窄栏版任务清单：每条任务一段紧凑摘要，宽列换行排。 */
function CompactTaskUsage({
  tasks,
  focusTaskId,
}: {
  tasks: BotUsageScheduleTask[];
  focusTaskId?: string | null;
}) {
  return (
    <section className="space-y-2" data-testid="usage-tasks-section">
      <h3 className="text-sm font-medium">按定时任务</h3>
      {!tasks.length ? (
        <p className="rounded-lg border bg-muted/40 py-6 text-center text-sm text-muted-foreground">
          窗口内没有定时任务运行
        </p>
      ) : (
        <ul className="divide-y rounded-lg border">
          {tasks.map((task) => (
            <li
              key={task.task_id}
              data-testid="usage-task-row"
              data-usage-task={task.task_id}
              className={cn(
                "space-y-0.5 px-3 py-2",
                task.task_id === focusTaskId && "bg-primary/5",
              )}
            >
              <p className="flex items-center gap-1.5 text-sm font-medium">
                <span
                  className={cn(
                    "truncate",
                    task.deleted && "text-muted-foreground",
                  )}
                >
                  {usageTaskName(task)}
                </span>
                {!task.enabled && !task.deleted && (
                  <Badge variant="secondary">已停用</Badge>
                )}
              </p>
              <p className="text-xs text-muted-foreground">
                运行 {task.runs} · 失败 {task.failed} · 跳过 {task.skipped} ·
                总花费 {formatUsageCost(taskTotalCost(task))}
              </p>
              <p className="text-xs text-muted-foreground">
                平均 {formatUsageCost(task.avg_run_cost)} · P90{" "}
                {formatUsageCost(task.p90_run_cost)} · 最高{" "}
                {formatUsageCost(task.max_run_cost)} · 连带主线≈
                {formatUsageCost(task.linked_main_cost)} · 推算月{" "}
                {formatUsageCost(task.estimated_monthly_cost)}
              </p>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
