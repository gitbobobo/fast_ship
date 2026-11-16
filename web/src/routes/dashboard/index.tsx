import { useMemo } from "react";
import {
  Activity,
  Bell,
  Bug,
  CalendarDays,
  CircleAlert,
  FolderKanban,
  Gauge,
  Hourglass,
  Plus,
  Rocket,
  Sparkles,
  TriangleAlert,
} from "lucide-react";
import { Link } from "react-router";
import { Header } from "@/components/layout/header";
import {
  MetricChart,
  type MetricChartPoint,
  type MetricChartSeriesPoint,
} from "@/components/dashboard/metric-chart";
import { FlowCard } from "@/components/dashboard/flow-card";
import { AiPanelCard } from "@/components/dashboard/ai-panel-card";
import { ReleaseCard } from "@/components/dashboard/release-card";
import { Card, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useDashboardOverview } from "@/lib/hooks/use-dashboard";
import { useProjects } from "@/lib/hooks/use-projects";
import type { components } from "@/types/api.gen";
import { cn } from "@/lib/utils";

type ActionItemGroup = components["schemas"]["DashboardActionItemGroup"];
type ActionItemKind = components["schemas"]["DashboardActionItemKind"];

function formatDateLabel(date: string) {
  return date.slice(5);
}

/** 日均数量小于 0.1 时保留「有数据」的语义，不要四舍五入成 0.0。 */
function formatDailyAverage(value: number) {
  if (value > 0 && value < 0.1) return "<0.1";
  return value.toFixed(1);
}

function formatDate() {
  const now = new Date();
  const options: Intl.DateTimeFormatOptions = {
    year: "numeric",
    month: "long",
    day: "numeric",
    weekday: "long",
  };
  return now.toLocaleDateString("zh-CN", options);
}

function getGreeting() {
  const hour = new Date().getHours();
  if (hour < 6) return "夜深了";
  if (hour < 12) return "早上好";
  if (hour < 14) return "中午好";
  if (hour < 18) return "下午好";
  return "晚上好";
}

const ACTION_GROUP_META: Record<
  ActionItemKind,
  { label: string; icon: typeof Activity }
> = {
  t3_waiting: { label: "T3 线程待处理", icon: Hourglass },
  review_failed: { label: "审查失败/超时", icon: CircleAlert },
  recommendations: { label: "推荐任务", icon: Sparkles },
  ship_failed: { label: "发版失败", icon: Rocket },
  quota_attention: { label: "额度需处理", icon: Gauge },
  unread_notifications: { label: "未读通知", icon: Bell },
};

const BAR_CHART_MAX_ROWS = 10;

export default function DashboardPage() {
  const { data, isLoading, isError, refetch } = useDashboardOverview();
  const { data: projectsData } = useProjects();
  // 仪表盘统计口径不含搁置项目（后端排除）；这里只提示它们存在，可去项目页查看
  const shelvedCount =
    projectsData?.items.filter((p) => p.is_shelved).length ?? 0;
  // 失败但已有缓存时（refetch 失败，isLoading=false、data 仍是旧值）也不能把旧数字
  // 当成当前状态：所有数字与趋势展示统一走这一个条件。
  const unavailable = isLoading || isError;

  const groups = useMemo<ActionItemGroup[]>(
    () => data?.action_item_groups ?? [],
    [data],
  );
  const pendingTotal = groups.reduce((sum, group) => sum + group.count, 0);
  // 任一组不完整时真实待办只会更多：摘要句用 M+ 表达下界，不假装精确。
  const anyGroupIncomplete = groups.some(
    (group) => group.complete === false,
  );
  const resolvedLast7Days =
    data?.daily_resolved.slice(-7).reduce((sum, point) => sum + point.resolved_count, 0) ?? 0;

  const openIssuePoints = useMemo<MetricChartPoint[]>(() => {
    return (
      data?.open_issues_by_project.map((item) => ({
        id: item.project_id,
        label: item.project_name,
        shortLabel: item.project_name,
        value: item.open_issue_count,
        href: `/issues?project=${item.project_id}&state=open`,
      })) ?? []
    );
  }, [data]);

  const dailyResolvedPoints = useMemo<MetricChartSeriesPoint[]>(() => {
    // 面积图只画总量：后端不再回传逐项目拆分，单系列值即当日已解决总数。
    return (
      data?.daily_resolved.map((item) => ({
        label: item.date,
        shortLabel: formatDateLabel(item.date),
        value: item.resolved_count,
        series: item.resolved_count
          ? [{ id: "total", name: "已解决", value: item.resolved_count }]
          : [],
      })) ?? []
    );
  }, [data]);

  // 流转效率：新建 vs 已解决按 date 对齐成两条堆叠序列（纯展示，不做方向判断）。
  const flowPoints = useMemo<MetricChartSeriesPoint[]>(() => {
    if (!data?.flow) return [];
    const resolvedByDate = new Map(
      data.daily_resolved.map((point) => [point.date, point.resolved_count]),
    );
    return data.flow.daily_created.map((point) => {
      const resolved = resolvedByDate.get(point.date) ?? 0;
      return {
        label: point.date,
        shortLabel: formatDateLabel(point.date),
        value: point.created_count + resolved,
        series: [
          { id: "created", name: "新建", value: point.created_count },
          { id: "resolved", name: "已解决", value: resolved },
        ],
      };
    });
  }, [data]);

  const configuredProjectCount = data?.open_issues_by_project.length ?? 0;
  const projectsWithOpenIssues =
    data?.open_issues_by_project.filter((item) => item.open_issue_count > 0)
      .length ?? 0;
  const totalOpenIssues =
    data?.open_issues_by_project.reduce(
      (sum, item) => sum + item.open_issue_count,
      0,
    ) ?? 0;
  const totalResolvedIssues =
    data?.daily_resolved.reduce(
      (sum, item) => sum + item.resolved_count,
      0,
    ) ?? 0;

  const avgDailyResolved = useMemo(() => {
    if (!data?.daily_resolved.length) return 0;
    return totalResolvedIssues / data.daily_resolved.length;
  }, [data, totalResolvedIssues]);

  return (
    <div className="flex min-h-full flex-col bg-background">
      <Header title="仪表盘" />

      <div className="flex-1 space-y-8 p-4 md:p-6">
        {/* Hero：有数据时给一句摘要（「今天」是 UTC 日，不能用于东八区早上，
            所以问候语只在无数据可说时兜底）；任一组不完整用 M+ 表达下界 */}
        <section className="space-y-1">
          <p className="text-xs font-medium tracking-wide text-muted-foreground uppercase">
            {formatDate()}
          </p>
          <h2 className="text-2xl font-bold tracking-tight md:text-3xl">
            {unavailable
              ? `${getGreeting()}，查看今日项目概况`
              : `近 7 天解决 ${resolvedLast7Days} 个，${pendingTotal}${
                  anyGroupIncomplete ? "+" : ""
                } 件事等你处理`}
          </h2>
        </section>

        {/* 概览请求失败时不要把「没有数据」当成「没有项目」 */}
        {isError && (
          <div
            role="alert"
            className="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-destructive/40 bg-destructive/[0.06] px-4 py-3"
          >
            <div className="flex items-center gap-2 text-sm text-destructive">
              <TriangleAlert className="h-4 w-4 shrink-0" />
              仪表盘数据加载失败，下面的统计不代表当前状态。
            </div>
            <Button
              size="sm"
              variant="outline"
              onClick={() => {
                void refetch();
              }}
            >
              重试
            </Button>
          </div>
        )}

        <div className="grid items-start gap-8 xl:grid-cols-2">
          {/* 左列：KPI → 待我处理 → 图表 → 流转效率；窄屏按 DOM 顺序单列 */}
          <div className="space-y-8">
            {/* KPI Cards */}
            <section className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
              <KpiCard
                icon={FolderKanban}
                label="已配置项目"
                value={unavailable ? null : configuredProjectCount}
                isLoading={isLoading}
                tone="neutral"
                href="/projects"
              />
              <KpiCard
                icon={Bug}
                label="剩余开启问题"
                value={unavailable ? null : totalOpenIssues}
                isLoading={isLoading}
                tone={
                  unavailable
                    ? "neutral"
                    : totalOpenIssues > 0
                      ? "warning"
                      : "neutral"
                }
              />
              <KpiCard
                icon={CalendarDays}
                label="近 30 天已解决"
                value={unavailable ? null : totalResolvedIssues}
                isLoading={isLoading}
                tone={
                  unavailable
                    ? "neutral"
                    : totalResolvedIssues > 0
                      ? "positive"
                      : "neutral"
                }
                // 迷你趋势只给这一张卡：最后一点是尚未结束的 UTC 当天，
                // 仅展示不判断方向。
                sparkline={
                  unavailable
                    ? undefined
                    : data?.daily_resolved
                        .slice(-7)
                        .map((point) => point.resolved_count)
                }
              />
            </section>

            {shelvedCount > 0 && (
              <p className="-mt-4 text-xs text-muted-foreground">
                另有 {shelvedCount} 个项目已搁置
              </p>
            )}

            {/* 待我处理：加载用骨架屏，失败不渲染，绝不能把旧数字当当前状态 */}
            {isLoading ? (
              <Card className="overflow-hidden">
                <CardContent className="space-y-4 p-5">
                  <Skeleton className="h-4 w-24" />
                  <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
                    <Skeleton className="h-28 w-full" />
                    <Skeleton className="h-28 w-full" />
                    <Skeleton className="h-28 w-full" />
                  </div>
                </CardContent>
              </Card>
            ) : isError ? null : (
              <ActionItemsCard groups={groups} />
            )}

            {/* Charts：左列内纵向堆叠（半宽列里并排会挤坏刻度） */}
            <section className="space-y-6">
              <Card className="overflow-hidden">
                <CardContent className="p-5">
                  <div className="mb-5 flex items-center justify-between">
                    <div>
                      <h3 className="text-sm font-semibold">开放问题</h3>
                      <p className="text-xs text-muted-foreground">
                        {unavailable
                          ? "—"
                          : `${projectsWithOpenIssues} 个项目有开启问题`}
                      </p>
                    </div>
                    {!unavailable && totalOpenIssues > 0 && (
                      <span className="rounded-full bg-primary/10 px-2.5 py-0.5 text-xs font-medium text-primary">
                        总计 {totalOpenIssues}
                      </span>
                    )}
                  </div>
                  {isLoading ? (
                    <ChartSkeleton />
                  ) : isError ? (
                    <ChartFailure />
                  ) : (
                    <MetricChart
                      points={openIssuePoints}
                      variant="bar"
                      emptyMessage="暂无已配置项目"
                      maxRows={BAR_CHART_MAX_ROWS}
                      collapsedLabel={(hidden) =>
                        `其他 ${hidden.length} 个项目共 ${hidden.reduce(
                          (sum, p) => sum + p.value,
                          0,
                        )} 条`
                      }
                      emptyAction={
                        <Button
                          size="sm"
                          variant="outline"
                          render={<Link to="/projects" />}
                        >
                          <Plus />
                          去创建项目
                        </Button>
                      }
                    />
                  )}
                </CardContent>
              </Card>

              <Card className="overflow-hidden">
                <CardContent className="p-5">
                  <div className="mb-5 flex items-center justify-between">
                    <div>
                      <h3 className="text-sm font-semibold">近 30 天已解决</h3>
                      <p className="text-xs text-muted-foreground">
                        每日已解决问题数量趋势
                      </p>
                    </div>
                    {!unavailable && totalResolvedIssues > 0 && (
                      <span className="text-xs font-medium text-muted-foreground">
                        日均 {formatDailyAverage(avgDailyResolved)}
                      </span>
                    )}
                  </div>
                  {isLoading ? (
                    <ChartSkeleton />
                  ) : isError ? (
                    <ChartFailure />
                  ) : (
                    <MetricChart
                      points={dailyResolvedPoints}
                      variant="area"
                      emptyMessage="暂无已解决问题数据"
                    />
                  )}
                </CardContent>
              </Card>
            </section>

            {!isLoading && !isError && data?.flow && (
              <FlowCard flow={data.flow} points={flowPoints} />
            )}
          </div>

          {/* 右列：AI 运行面板 → 发布节奏 */}
          <div className="space-y-6">
            {!isLoading && !isError && data?.ai && (
              <AiPanelCard ai={data.ai} />
            )}
            {!isLoading && !isError && data?.release && (
              <ReleaseCard release={data.release} />
            )}
          </div>
        </div>
      </div>
    </div>
  );
}

/* ─── 待我处理 ─── */

/** 「待我处理」聚合区：只列有待办或部分不可用的组；全部清零时折叠成「一切正常」。 */
function ActionItemsCard({ groups }: { groups: ActionItemGroup[] }) {
  const visible = groups.filter(
    (group) => group.count > 0 || group.complete === false,
  );

  return (
    <Card className="overflow-hidden">
      <CardContent className="p-5">
        <h3 className="mb-4 text-sm font-semibold">待我处理</h3>
        {visible.length === 0 ? (
          <p className="text-xs text-muted-foreground">一切正常</p>
        ) : (
          <div className="grid gap-5 sm:grid-cols-2 xl:grid-cols-3">
            {visible.map((group) => (
              <ActionItemGroupSection key={group.kind} group={group} />
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function ActionItemGroupSection({ group }: { group: ActionItemGroup }) {
  const meta = ACTION_GROUP_META[group.kind];
  // 服务端先于前端上线新 kind 时跳过未知组，而不是整卡崩溃。
  if (!meta) return null;
  const Icon = meta.icon;
  const incomplete = group.complete === false;

  return (
    <div className="space-y-2">
      <div className="flex items-center gap-2">
        <Icon className="h-4 w-4 shrink-0 text-muted-foreground" />
        <span className="text-xs font-medium">{meta.label}</span>
        <span
          className={cn(
            "ml-auto rounded-full px-2 py-0.5 text-[11px] font-medium tabular-nums",
            incomplete
              ? "bg-amber-500/10 text-amber-700 dark:text-amber-400"
              : "bg-muted text-muted-foreground",
          )}
        >
          {incomplete
            ? group.count > 0
              ? `${group.count}+ · 部分环境暂不可用`
              : "暂不可用"
            : group.count}
        </span>
      </div>
      <ul className="space-y-1">
        {group.items.map((item, idx) => (
          <li key={`${item.href}-${item.title}-${idx}`}>
            <Link
              to={item.href}
              className="block rounded-md px-2 py-1.5 -mx-2 transition-colors hover:bg-muted/60"
            >
              <div className="truncate text-xs font-medium">{item.title}</div>
              {item.detail && (
                <div className="truncate text-[11px] text-muted-foreground">
                  {item.detail}
                </div>
              )}
            </Link>
          </li>
        ))}
      </ul>
    </div>
  );
}

/* ─── 通用 ─── */

function KpiCard({
  icon: Icon,
  label,
  value,
  isLoading,
  tone = "neutral",
  href,
  sparkline,
}: {
  icon: typeof Activity;
  label: string;
  value: number | null;
  isLoading: boolean;
  tone?: "neutral" | "warning" | "positive";
  href?: string;
  /** 7 天迷你趋势（仅近 30 天已解决卡）；全零不画，避免噪音。 */
  sparkline?: number[];
}) {
  const toneClasses = {
    neutral:
      "bg-muted/40 text-foreground ring-foreground/10 hover:ring-foreground/20",
    warning:
      "bg-amber-500/[0.06] text-amber-700 ring-amber-500/20 hover:ring-amber-500/30 dark:bg-amber-500/10 dark:text-amber-400",
    positive:
      "bg-emerald-500/[0.06] text-emerald-700 ring-emerald-500/20 hover:ring-emerald-500/30 dark:bg-emerald-500/10 dark:text-emerald-400",
  };

  const className = cn(
    "flex flex-col gap-3 rounded-2xl p-5 ring-1 transition-all duration-300",
    toneClasses[tone],
    href && "cursor-pointer",
  );

  const body = (
    <>
      <div className="flex items-center gap-2 text-xs font-medium opacity-70">
        <Icon className="h-4 w-4" />
        <span>{label}</span>
      </div>
      {isLoading ? (
        <Skeleton className="h-10 w-24" />
      ) : (
        <div
          className={cn(
            "tracking-tighter tabular-nums",
            value === null
              ? "text-3xl font-normal text-muted-foreground"
              : "text-4xl font-bold",
          )}
        >
          {value ?? "—"}
        </div>
      )}
      {!isLoading && sparkline && (
        <Sparkline values={sparkline} tone={tone} />
      )}
    </>
  );

  return href ? (
    <Link to={href} className={className}>
      {body}
    </Link>
  ) : (
    <div className={className}>{body}</div>
  );
}

/** 7 天 sparkline：最后一格是尚未结束的 UTC 当天，仅展示不判断方向。 */
function Sparkline({
  values,
  tone,
}: {
  values: number[];
  tone: "neutral" | "warning" | "positive";
}) {
  if (values.length < 2 || values.every((value) => value === 0)) return null;
  const max = Math.max(...values);
  const width = 72;
  const height = 24;
  const points = values
    .map((value, index) => {
      const x = (index / (values.length - 1)) * width;
      const y = height - 2 - (value / max) * (height - 4);
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(" ");
  const stroke = {
    neutral: "text-muted-foreground",
    warning: "text-amber-700 dark:text-amber-400",
    positive: "text-emerald-700 dark:text-emerald-400",
  }[tone];

  return (
    <svg
      viewBox={`0 0 ${width} ${height}`}
      className={cn("h-6 w-18", stroke)}
      aria-hidden="true"
      data-testid="kpi-sparkline"
    >
      <polyline
        points={points}
        fill="none"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinejoin="round"
        strokeLinecap="round"
      />
    </svg>
  );
}

function ChartSkeleton() {
  return (
    <div className="space-y-4">
      <Skeleton className="h-4 w-40" />
      <Skeleton className="h-[200px] w-full" />
      <Skeleton className="h-8 w-full" />
    </div>
  );
}

function ChartFailure() {
  return (
    <div className="flex h-[240px] items-center justify-center px-4 text-center text-xs text-muted-foreground">
      数据加载失败，请在上方重试。
    </div>
  );
}
