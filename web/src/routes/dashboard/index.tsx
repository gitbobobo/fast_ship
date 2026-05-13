import { useMemo } from "react";
import {
  Activity,
  Bug,
  CalendarDays,
  FolderKanban,
  TrendingDown,
  TrendingUp,
} from "lucide-react";
import { Header } from "@/components/layout/header";
import {
  MetricChart,
  type MetricChartPoint,
  type MetricChartSeriesPoint,
} from "@/components/dashboard/metric-chart";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { useDashboardOverview } from "@/lib/hooks/use-dashboard";
import { cn } from "@/lib/utils";

function formatDateLabel(date: string) {
  return date.slice(5);
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

export default function DashboardPage() {
  const { data, isLoading } = useDashboardOverview();

  const openIssuePoints = useMemo<MetricChartPoint[]>(() => {
    const points =
      data?.open_issues_by_project.map((item) => ({
        id: item.project_id,
        label: item.project_name,
        shortLabel: item.project_name,
        value: item.open_issue_count,
      })) ?? [];
    return points.length > 0
      ? points
      : [{ label: "暂无项目", shortLabel: "暂无项目", value: 0 }];
  }, [data]);

  const dailyResolvedPoints = useMemo<MetricChartSeriesPoint[]>(() => {
    const points =
      data?.daily_resolved.map((item) => ({
        label: item.date,
        shortLabel: formatDateLabel(item.date),
        value: item.resolved_count,
        series:
          item.projects?.map((p) => ({
            id: p.project_id,
            name: p.project_name,
            value: p.count,
          })) ?? [],
      })) ?? [];
    return points.length > 0
      ? points
      : Array.from({ length: 30 }, (_, index) => ({
          label: `day-${index}`,
          shortLabel: `${index + 1}`,
          value: 0,
          series: [],
        }));
  }, [data]);

  const configuredProjectCount = data?.open_issues_by_project.length ?? 0;
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

  const maxDailyResolved = useMemo(() => {
    if (!data?.daily_resolved.length) return 0;
    return Math.max(...data.daily_resolved.map((d) => d.resolved_count));
  }, [data]);

  const avgDailyResolved = useMemo(() => {
    if (!data?.daily_resolved.length) return 0;
    return totalResolvedIssues / data.daily_resolved.length;
  }, [data, totalResolvedIssues]);

  const openIssuesEmptyMessage =
    configuredProjectCount === 0 ? "暂无已配置项目" : "当前没有剩余开启问题";

  return (
    <div className="flex min-h-full flex-col bg-background">
      <Header title="仪表盘" />

      <div className="flex-1 space-y-8 p-4 md:p-6">
        {/* Hero */}
        <section className="space-y-1">
          <p className="text-xs font-medium tracking-wide text-muted-foreground uppercase">
            {formatDate()}
          </p>
          <h2 className="text-2xl font-bold tracking-tight md:text-3xl">
            {getGreeting()}，查看今日项目概况
          </h2>
        </section>

        {/* KPI Cards */}
        <section className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          <KpiCard
            icon={FolderKanban}
            label="已配置项目"
            value={configuredProjectCount}
            isLoading={isLoading}
            tone="neutral"
          />
          <KpiCard
            icon={Bug}
            label="剩余开启问题"
            value={totalOpenIssues}
            isLoading={isLoading}
            tone={totalOpenIssues > 0 ? "warning" : "neutral"}
          />
          <KpiCard
            icon={CalendarDays}
            label="近 30 天已解决"
            value={totalResolvedIssues}
            isLoading={isLoading}
            tone={totalResolvedIssues > 0 ? "positive" : "neutral"}
          />
        </section>

        {/* Charts */}
        <section className="grid gap-6 xl:grid-cols-2">
          <Card className="overflow-hidden">
            <CardContent className="p-5">
              <div className="mb-5 flex items-center justify-between">
                <div>
                  <h3 className="text-sm font-semibold">开放问题</h3>
                  <p className="text-xs text-muted-foreground">
                    按项目分布的未解决问题数量
                  </p>
                </div>
                {!isLoading && totalOpenIssues > 0 && (
                  <span className="rounded-full bg-primary/10 px-2.5 py-0.5 text-xs font-medium text-primary">
                    总计 {totalOpenIssues}
                  </span>
                )}
              </div>
              {isLoading ? (
                <ChartSkeleton />
              ) : (
                <MetricChart
                  points={openIssuePoints}
                  variant="bar"
                  emptyMessage={openIssuesEmptyMessage}
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
                    各项目每日已解决问题数量趋势
                  </p>
                </div>
                {!isLoading && (
                  <div className="flex items-center gap-1.5">
                    {avgDailyResolved >= (maxDailyResolved || 1) * 0.5 ? (
                      <TrendingUp className="h-3.5 w-3.5 text-emerald-500" />
                    ) : (
                      <TrendingDown className="h-3.5 w-3.5 text-amber-500" />
                    )}
                    <span className="text-xs font-medium text-muted-foreground">
                      日均 {avgDailyResolved.toFixed(1)}
                    </span>
                  </div>
                )}
              </div>
              {isLoading ? (
                <ChartSkeleton />
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

        {/* Bottom stats */}
        <section className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
          <StatPill
            label="最高日解决"
            value={isLoading ? "—" : String(maxDailyResolved)}
          />
          <StatPill
            label="日均解决"
            value={isLoading ? "—" : avgDailyResolved.toFixed(1)}
          />
          <StatPill
            label="活跃项目"
            value={isLoading ? "—" : String(configuredProjectCount)}
          />
          <StatPill
            label="问题总量"
            value={isLoading ? "—" : String(totalOpenIssues + totalResolvedIssues)}
          />
        </section>
      </div>
    </div>
  );
}

/* ─── Subcomponents ─── */

function KpiCard({
  icon: Icon,
  label,
  value,
  isLoading,
  tone = "neutral",
}: {
  icon: typeof Activity;
  label: string;
  value: number;
  isLoading: boolean;
  tone?: "neutral" | "warning" | "positive";
}) {
  const toneClasses = {
    neutral:
      "bg-muted/40 text-foreground ring-foreground/10 hover:ring-foreground/20",
    warning:
      "bg-amber-500/[0.06] text-amber-700 ring-amber-500/20 hover:ring-amber-500/30 dark:bg-amber-500/10 dark:text-amber-400",
    positive:
      "bg-emerald-500/[0.06] text-emerald-700 ring-emerald-500/20 hover:ring-emerald-500/30 dark:bg-emerald-500/10 dark:text-emerald-400",
  };

  return (
    <div
      className={cn(
        "group relative flex flex-col gap-3 rounded-2xl p-5 ring-1 transition-all duration-300",
        toneClasses[tone],
      )}
    >
      <div className="flex items-center gap-2 text-xs font-medium opacity-70">
        <Icon className="h-4 w-4" />
        <span>{label}</span>
      </div>
      {isLoading ? (
        <Skeleton className="h-10 w-24" />
      ) : (
        <div className="text-4xl font-bold tracking-tighter tabular-nums">
          {value}
        </div>
      )}
      <span className="absolute right-4 top-4 h-1.5 w-1.5 rounded-full bg-current opacity-20" />
    </div>
  );
}

function StatPill({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-center justify-between rounded-xl border border-border/60 px-4 py-3">
      <span className="text-xs text-muted-foreground">{label}</span>
      <span className="text-sm font-semibold tabular-nums">{value}</span>
    </div>
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
