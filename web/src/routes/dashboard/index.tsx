import { useMemo } from "react";
import {
  Activity,
  Bug,
  CalendarDays,
  FolderKanban,
  Plus,
  TriangleAlert,
} from "lucide-react";
import { Link } from "react-router";
import { Header } from "@/components/layout/header";
import {
  MetricChart,
  type MetricChartPoint,
  type MetricChartSeriesPoint,
} from "@/components/dashboard/metric-chart";
import { Card, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useDashboardOverview } from "@/lib/hooks/use-dashboard";
import { useProjects } from "@/lib/hooks/use-projects";
import { cn } from "@/lib/utils";

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

export default function DashboardPage() {
  const { data, isLoading, isError, refetch } = useDashboardOverview();
  const { data: projectsData } = useProjects();
  // 仪表盘统计口径不含搁置项目（后端排除）；这里只提示它们存在，可去项目页查看
  const shelvedCount =
    projectsData?.items.filter((p) => p.is_shelved).length ?? 0;
  // 失败但已有缓存时（refetch 失败，isLoading=false、data 仍是旧值）也不能把旧数字
  // 当成当前状态：所有数字与趋势展示统一走这一个条件。
  const unavailable = isLoading || isError;

  const openIssuePoints = useMemo<MetricChartPoint[]>(() => {
    return (
      data?.open_issues_by_project.map((item) => ({
        id: item.project_id,
        label: item.project_name,
        shortLabel: item.project_name,
        value: item.open_issue_count,
      })) ?? []
    );
  }, [data]);

  const dailyResolvedPoints = useMemo<MetricChartSeriesPoint[]>(() => {
    return (
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
      })) ?? []
    );
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

        {/* KPI Cards */}
        <section className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          <KpiCard
            icon={FolderKanban}
            label="已配置项目"
            value={unavailable ? null : configuredProjectCount}
            isLoading={isLoading}
            tone="neutral"
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
          />
        </section>

        {shelvedCount > 0 && (
          <p className="-mt-4 text-xs text-muted-foreground">
            另有 {shelvedCount} 个项目已搁置
          </p>
        )}

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
                    各项目每日已解决问题数量趋势
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

        {/* Bottom stats */}
        <section className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
          <StatPill
            label="最高日解决"
            value={unavailable ? "—" : String(maxDailyResolved)}
          />
          <StatPill
            label="日均解决"
            value={
              unavailable ? "—" : formatDailyAverage(avgDailyResolved)
            }
          />
          <StatPill
            label="活跃项目"
            value={unavailable ? "—" : String(configuredProjectCount)}
          />
          <StatPill
            label="问题总量"
            value={
              unavailable
                ? "—"
                : String(totalOpenIssues + totalResolvedIssues)
            }
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
  value: number | null;
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
        "flex flex-col gap-3 rounded-2xl p-5 ring-1 transition-all duration-300",
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

function ChartFailure() {
  return (
    <div className="flex h-[240px] items-center justify-center px-4 text-center text-xs text-muted-foreground">
      数据加载失败，请在上方重试。
    </div>
  );
}
