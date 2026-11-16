import { useMemo } from "react";
import {
  Activity,
  Bell,
  Bot,
  Bug,
  CalendarDays,
  CircleAlert,
  FolderKanban,
  Gauge,
  GitPullRequest,
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
import { Card, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useDashboardOverview } from "@/lib/hooks/use-dashboard";
import { useProjects } from "@/lib/hooks/use-projects";
import {
  accountRemaining,
  quotaAccountGroup,
  quotaUsageTone,
  type QuotaUsageTone,
} from "@/lib/quota";
import type { components } from "@/types/api.gen";
import { cn } from "@/lib/utils";

type ActionItemGroup = components["schemas"]["DashboardActionItemGroup"];
type ActionItemKind = components["schemas"]["DashboardActionItemKind"];
type Overview = components["schemas"]["DashboardOverview"];
type FlowBlock = NonNullable<Overview["flow"]>;
type AiBlock = NonNullable<Overview["ai"]>;
type ReleaseBlock = NonNullable<Overview["release"]>;

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

const QUOTA_STATUS_BADGE: Record<string, string> = {
  pending: "待抓取",
  credential_invalid: "凭据失效",
  provider_error: "服务商异常",
};

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

/* ─── 流转效率 ─── */

function FlowCard({
  flow,
  points,
}: {
  flow: FlowBlock;
  points: MetricChartSeriesPoint[];
}) {
  const backlogTotal =
    flow.backlog_age.under_7d +
    flow.backlog_age.d7_to_30d +
    flow.backlog_age.over_30d;

  return (
    <Card className="overflow-hidden" data-testid="flow-card">
      <CardContent className="space-y-5 p-5">
        <div className="flex items-center justify-between">
          <div>
            <h3 className="text-sm font-semibold">流转效率</h3>
            <p className="text-xs text-muted-foreground">
              近 30 天新建与已解决（UTC 日）
            </p>
          </div>
          {/* 恒等式：待办+进行中 == Σ open_issue_count（剩余开启问题 KPI）；
              降级时零值占位不可见，避免被读成真实读数 */}
          {flow.complete !== false && (
            <div className="flex items-center gap-1.5">
              <span className="rounded-full bg-muted px-2.5 py-0.5 text-xs font-medium tabular-nums text-muted-foreground">
                待办 {flow.todo_count}
              </span>
              <span className="rounded-full bg-primary/10 px-2.5 py-0.5 text-xs font-medium tabular-nums text-primary">
                进行中 {flow.in_progress_count}
              </span>
            </div>
          )}
        </div>

        {flow.complete === false ? (
          /* 降级时数字是清零的占位，绝不能当成真实读数 */
          <p className="text-xs text-muted-foreground">暂不可用</p>
        ) : (
          <>
            <MetricChart
              points={points}
              variant="area"
              emptyMessage="近 30 天暂无新建或已解决"
            />

            <div>
              <h4 className="mb-2 text-xs font-medium text-muted-foreground">
                各项目完成周期（近 30 天中位数）
              </h4>
              {flow.cycle_time_by_project.length === 0 ? (
                <p className="text-xs text-muted-foreground">暂无周期数据</p>
              ) : (
                <ul className="space-y-1">
                  {flow.cycle_time_by_project.map((row) => (
                    <li
                      key={row.project_id}
                      className="flex items-center justify-between gap-2 text-xs"
                    >
                      <span className="min-w-0 truncate">{row.project_name}</span>
                      <span className="shrink-0 tabular-nums text-muted-foreground">
                        中位 {row.median_days.toFixed(1)} 天 ·{" "}
                        {row.completed_count} 个
                      </span>
                    </li>
                  ))}
                </ul>
              )}
            </div>

            <div>
              <h4 className="mb-2 text-xs font-medium text-muted-foreground">
                积压年龄分布
              </h4>
              {backlogTotal === 0 ? (
                <p className="text-xs text-muted-foreground">
                  当前没有开启问题
                </p>
              ) : (
                <BacklogAgeBar age={flow.backlog_age} total={backlogTotal} />
              )}
            </div>
          </>
        )}
      </CardContent>
    </Card>
  );
}

const BACKLOG_SEGMENTS: Array<{
  key: "under_7d" | "d7_to_30d" | "over_30d";
  label: string;
  className: string;
}> = [
  { key: "under_7d", label: "7 天内", className: "bg-primary/70" },
  { key: "d7_to_30d", label: "7-30 天", className: "bg-amber-500/60" },
  {
    key: "over_30d",
    label: "30 天以上",
    className: "bg-muted-foreground/50",
  },
];

function BacklogAgeBar({
  age,
  total,
}: {
  age: FlowBlock["backlog_age"];
  total: number;
}) {
  return (
    <div data-testid="backlog-age-bar">
      <div className="flex h-2 w-full overflow-hidden rounded-full bg-muted">
        {BACKLOG_SEGMENTS.map((segment) => {
          const value = age[segment.key];
          if (value <= 0) return null;
          return (
            <div
              key={segment.key}
              role="progressbar"
              aria-label={`${segment.label} ${value} 个`}
              aria-valuemin={0}
              aria-valuemax={total}
              aria-valuenow={value}
              className={segment.className}
              style={{ width: `${(value / total) * 100}%` }}
            />
          );
        })}
      </div>
      <div className="mt-1.5 flex flex-wrap gap-x-4 gap-y-1 text-[11px] text-muted-foreground">
        {BACKLOG_SEGMENTS.map((segment) => (
          <span key={segment.key} className="tabular-nums">
            {segment.label} {age[segment.key]}
          </span>
        ))}
      </div>
    </div>
  );
}

/* ─── AI 运行面板 ─── */

function AiPanelCard({ ai }: { ai: AiBlock }) {
  const sections = [
    ai.t3 != null && <AiT3Section key="t3" t3={ai.t3} />,
    ai.reviews != null && <AiReviewsSection key="reviews" reviews={ai.reviews} />,
    ai.bots != null && <AiBotsSection key="bots" bots={ai.bots} />,
    ai.quota != null && <AiQuotaSection key="quota" quota={ai.quota} />,
  ].filter(Boolean);

  if (sections.length === 0) return null;

  return (
    <Card className="overflow-hidden" data-testid="ai-panel-card">
      <CardContent className="p-5">
        <h3 className="mb-4 flex items-center gap-2 text-sm font-semibold">
          <Bot className="h-4 w-4 text-muted-foreground" />
          AI 运行
        </h3>
        <div className="divide-y">
          {sections}
        </div>
      </CardContent>
    </Card>
  );
}

function SectionUnavailable() {
  return <p className="text-xs text-muted-foreground">暂不可用</p>;
}

function AiT3Section({ t3 }: { t3: NonNullable<AiBlock["t3"]> }) {
  return (
    <section className="space-y-2 py-3 first:pt-0" data-testid="ai-t3-section">
      <SectionHeader icon={Hourglass} title="T3 线程" />
      {t3.complete === false ? (
        <SectionUnavailable />
      ) : t3.environments.length === 0 ? (
        <p className="text-xs text-muted-foreground">没有活跃线程</p>
      ) : (
        <ul className="space-y-1">
          {t3.environments.map((env) => (
            <li
              key={env.environment_id}
              className="flex items-center justify-between gap-2 text-xs"
            >
              <span className="min-w-0 truncate">{env.environment_name}</span>
              <span className="shrink-0 tabular-nums text-muted-foreground">
                {env.readable
                  ? [
                      `运行 ${env.running}`,
                      `等待 ${env.waiting}`,
                      env.queued > 0 ? `排队 ${env.queued}` : null,
                    ]
                      .filter(Boolean)
                      .join(" · ")
                  : "本轮不可读"}
              </span>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function AiReviewsSection({
  reviews,
}: {
  reviews: NonNullable<AiBlock["reviews"]>;
}) {
  const total =
    reviews.passed + reviews.blocked + reviews.errored + reviews.in_flight;
  return (
    <section
      className="space-y-2 py-3"
      data-testid="ai-reviews-section"
    >
      <SectionHeader
        icon={GitPullRequest}
        title="PR 审查（近 30 天）"
        trailing={
          // 分母为 0 时 pass_rate 是 null：不显示，也不能读成 100%
          reviews.pass_rate != null ? (
            <span className="rounded-full bg-muted px-2 py-0.5 text-[11px] font-medium tabular-nums text-muted-foreground">
              通过率 {Math.round(reviews.pass_rate * 100)}%
            </span>
          ) : undefined
        }
      />
      {reviews.complete === false ? (
        <SectionUnavailable />
      ) : total === 0 ? (
        <p className="text-xs text-muted-foreground">近 30 天没有审查记录</p>
      ) : (
        <div className="grid grid-cols-4 gap-2 text-center">
          <BucketStat label="通过" value={reviews.passed} tone="positive" />
          <BucketStat label="阻塞" value={reviews.blocked} tone="warning" />
          <BucketStat label="出错" value={reviews.errored} tone="danger" />
          <BucketStat label="进行中" value={reviews.in_flight} tone="neutral" />
        </div>
      )}
    </section>
  );
}

const BUCKET_TONE: Record<
  "positive" | "warning" | "danger" | "neutral",
  string
> = {
  positive: "text-emerald-700 dark:text-emerald-400",
  warning: "text-amber-700 dark:text-amber-400",
  danger: "text-destructive",
  neutral: "text-foreground",
};

function BucketStat({
  label,
  value,
  tone,
}: {
  label: string;
  value: number;
  tone: keyof typeof BUCKET_TONE;
}) {
  return (
    <div className="rounded-lg bg-muted/50 px-1 py-2">
      <div
        className={cn(
          "text-base font-semibold tabular-nums",
          BUCKET_TONE[tone],
        )}
      >
        {value}
      </div>
      <div className="text-[11px] text-muted-foreground">{label}</div>
    </div>
  );
}

function AiBotsSection({ bots }: { bots: NonNullable<AiBlock["bots"]> }) {
  return (
    <section className="space-y-2 py-3" data-testid="ai-bots-section">
      <SectionHeader
        icon={Bot}
        title="机器人"
        trailing={
          <span className="text-[11px] text-muted-foreground">
            近 7 天（按你的时区）
          </span>
        }
      />
      {bots.complete === false ? (
        <SectionUnavailable />
      ) : bots.bot_count === 0 ? (
        <p className="text-xs text-muted-foreground">未配置机器人</p>
      ) : (
        <div className="space-y-1.5 text-xs">
          <div className="flex items-center justify-between gap-2">
            <span className="text-muted-foreground">
              Tokens · 请求（{bots.bot_count} 个机器人）
            </span>
            <span className="shrink-0 tabular-nums">
              {bots.total_tokens.toLocaleString("en-US")} ·{" "}
              {bots.total_requests.toLocaleString("en-US")}
            </span>
          </div>
          <div className="flex items-center justify-between gap-2">
            <span className="text-muted-foreground">花费</span>
            <span className="shrink-0 tabular-nums">
              {/* cost 为 null 是「缺失 ≠ 0」：不能渲染成 $0.00 */}
              {bots.cost == null ? (
                <span className="text-muted-foreground">未计价</span>
              ) : (
                `$${bots.cost.toFixed(2)}`
              )}
            </span>
          </div>
          {bots.next_schedule && (
            <div className="flex items-center justify-between gap-2">
              <span className="text-muted-foreground">下次任务</span>
              <span className="min-w-0 truncate text-right">
                {bots.next_schedule.task_name} · {bots.next_schedule.bot_name}{" "}
                <span className="tabular-nums text-muted-foreground">
                  {formatScheduleTime(bots.next_schedule.next_run_at)}
                </span>
              </span>
            </div>
          )}
        </div>
      )}
    </section>
  );
}

function formatScheduleTime(iso: string) {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return iso;
  return date.toLocaleString("zh-CN", {
    month: "numeric",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

function AiQuotaSection({ quota }: { quota: NonNullable<AiBlock["quota"]> }) {
  return (
    <section className="space-y-2 py-3" data-testid="ai-quota-section">
      <SectionHeader icon={Gauge} title="套餐额度" />
      {quota.complete === false ? (
        <SectionUnavailable />
      ) : quota.accounts.length === 0 ? (
        <p className="text-xs text-muted-foreground">未配置额度账号</p>
      ) : (
        <ul className="space-y-1.5">
          {quota.accounts.map((account) => (
            <li
              key={account.id}
              className="flex items-center gap-2 text-xs"
            >
              <span className="min-w-0 w-24 truncate">{account.label}</span>
              <QuotaMiniBar account={account} />
              {account.status !== "ok" && (
                <span
                  className={cn(
                    "shrink-0 rounded-full px-2 py-0.5 text-[11px] font-medium",
                    quotaAccountGroup(account) === "attention"
                      ? "bg-destructive/10 text-destructive"
                      : "bg-muted text-muted-foreground",
                  )}
                >
                  {QUOTA_STATUS_BADGE[account.status] ?? account.status}
                </span>
              )}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

/**
 * 额度迷你条：复用 lib/quota.ts 的有效剩余/配色口径——剩余 =
 * accountRemaining（默认池 100−max(used_percent)），条画的是已用方向。
 */
function QuotaMiniBar({ account }: { account: components["schemas"]["QuotaAccount"] }) {
  const remaining = accountRemaining(account);
  const usedPercent = remaining == null ? null : 100 - remaining;
  const tone: QuotaUsageTone = quotaUsageTone(usedPercent);
  const exhausted = usedPercent != null && usedPercent >= 100;
  const width = usedPercent == null ? 0 : Math.min(100, Math.max(0, usedPercent));

  return (
    <div className="flex min-w-0 flex-1 items-center gap-2">
      <div
        role="progressbar"
        aria-label={`${account.label} 已用 ${
          usedPercent == null ? "未知" : `${Math.round(usedPercent)}%`
        }`}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={usedPercent ?? undefined}
        className="h-1.5 min-w-8 flex-1 overflow-hidden rounded-full bg-muted"
      >
        <div
          data-testid="dashboard-quota-bar"
          className={cn(
            "h-full rounded-full",
            exhausted
              ? "bg-muted-foreground/60"
              : tone === "normal"
                ? "bg-primary"
                : "bg-amber-500 dark:bg-amber-400",
          )}
          style={{ width: `${width}%` }}
        />
      </div>
      <span
        className={cn(
          "w-10 shrink-0 text-right tabular-nums",
          exhausted ? "text-muted-foreground" : "text-foreground",
        )}
      >
        {remaining == null ? "—" : `${Math.round(remaining)}%`}
      </span>
    </div>
  );
}

function SectionHeader({
  icon: Icon,
  title,
  trailing,
}: {
  icon: typeof Activity;
  title: string;
  trailing?: React.ReactNode;
}) {
  return (
    <div className="flex items-center gap-2">
      <Icon className="h-4 w-4 shrink-0 text-muted-foreground" />
      <h4 className="text-xs font-medium">{title}</h4>
      {trailing != null && <div className="ml-auto">{trailing}</div>}
    </div>
  );
}

/* ─── 发布节奏 ─── */

function ReleaseCard({ release }: { release: ReleaseBlock }) {
  // 首条发版推「距上次发版 N 天」；今天发版显示「今天」。
  const lastRelease = release.recent_releases[0];
  const lastReleaseText = daysAgoText(lastRelease?.shipped_at);

  return (
    <Card className="overflow-hidden" data-testid="release-card">
      <CardContent className="space-y-4 p-5">
        <div className="flex items-center gap-2">
          <Rocket className="h-4 w-4 text-muted-foreground" />
          <h3 className="text-sm font-semibold">发布节奏</h3>
          {lastReleaseText && (
            <span className="ml-auto rounded-full bg-muted px-2 py-0.5 text-[11px] font-medium text-muted-foreground">
              距上次发版 {lastReleaseText}
            </span>
          )}
        </div>

        {release.complete === false ? (
          <SectionUnavailable />
        ) : (
          <>
            <div>
              <h4 className="mb-2 text-xs font-medium text-muted-foreground">
                待发版 {release.pending_count}
              </h4>
              {release.pending_versions.length === 0 ? (
                <p className="text-xs text-muted-foreground">没有待发版</p>
              ) : (
                <ul className="space-y-1">
                  {release.pending_versions.map((version) => (
                    <li key={version.version_id}>
                      <Link
                        to={version.href}
                        className="flex items-center justify-between gap-2 rounded-md px-2 py-1.5 -mx-2 transition-colors hover:bg-muted/60"
                      >
                        <span className="truncate text-xs font-medium tabular-nums">
                          {version.version_number}
                        </span>
                        <span className="min-w-0 truncate text-[11px] text-muted-foreground">
                          {version.project_name}
                        </span>
                      </Link>
                    </li>
                  ))}
                </ul>
              )}
            </div>

            <div>
              <h4 className="mb-2 text-xs font-medium text-muted-foreground">
                近期发版
              </h4>
              {release.recent_releases.length === 0 ? (
                <p className="text-xs text-muted-foreground">
                  还没有发版记录
                </p>
              ) : (
                <ol className="space-y-1.5">
                  {release.recent_releases.map((releaseItem) => (
                    <li
                      key={releaseItem.version_id}
                      className="flex items-center gap-2 text-xs"
                    >
                      <span
                        aria-hidden="true"
                        className="h-1.5 w-1.5 shrink-0 rounded-full bg-primary/60"
                      />
                      <Link
                        to={releaseItem.href}
                        className="min-w-0 shrink-0 truncate font-medium tabular-nums hover:underline"
                      >
                        {releaseItem.version_number}
                      </Link>
                      <span className="min-w-0 flex-1 truncate text-[11px] text-muted-foreground">
                        {releaseItem.project_name}
                      </span>
                      <span className="shrink-0 tabular-nums text-[11px] text-muted-foreground">
                        {formatScheduleTime(releaseItem.shipped_at)}
                      </span>
                    </li>
                  ))}
                </ol>
              )}
            </div>

            <p className="border-t pt-3 text-xs text-muted-foreground">
              {release.unmerged_pr_issue_count > 0
                ? `${release.unmerged_pr_issue_count} 个开启问题还有未合并的 PR`
                : "没有挂着未合并 PR 的问题"}
            </p>
          </>
        )}
      </CardContent>
    </Card>
  );
}

/** 距今 N 天（自然日向下取整）；无效时间返回 null，今天发版返回「今天」。 */
function daysAgoText(iso: string | undefined) {
  if (!iso) return null;
  const time = new Date(iso).getTime();
  if (Number.isNaN(time)) return null;
  const days = Math.floor((Date.now() - time) / 86_400_000);
  if (days <= 0) return "今天";
  return `${days} 天`;
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
