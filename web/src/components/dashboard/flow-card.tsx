import { Card, CardContent } from "@/components/ui/card";
import {
  MetricChart,
  MetricChartLegend,
  type MetricChartSeriesPoint,
} from "@/components/dashboard/metric-chart";
import type { components } from "@/types/api.gen";

type FlowBlock = components["schemas"]["DashboardFlow"];

/**
 * 完成周期自适应单位（W3）：<1 小时显示「<1 小时」，不足一天显示「N 小时」
 * （四舍五入到小时），≥1 天显示一位小数的「N 天」。阈值按天数比 1/24，
 * 避免先乘 24 引入的浮点误差。
 */
function formatMedianDays(medianDays: number): string {
  if (medianDays < 1 / 24) return "<1 小时";
  if (medianDays < 1) return `${Math.round(medianDays * 24)} 小时`;
  return `${medianDays.toFixed(1)} 天`;
}

/**
 * 流转效率卡（INT-235 阶段二）：新建 vs 已解决堆叠面积图、各项目完成周期
 * 与积压年龄分布。从仪表盘页拆出（panel P8），纯展示，数据由页面装配。
 */
export function FlowCard({
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
  // 全零时图表转空状态，图例不能留在空态上方（与 MetricChart 的 isEmpty 同口径）
  const chartEmpty =
    points.length === 0 || points.every((point) => point.value === 0);

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
              降级时零值占位不可见，避免被读成真实读数。
              纯文本元信息（W5），与「日均 x.x」同款，不再用药丸底色 */}
          {flow.complete !== false && (
            <span className="shrink-0 text-[11px] tabular-nums text-muted-foreground">
              待办 {flow.todo_count} · 进行中 {flow.in_progress_count}
            </span>
          )}
        </div>

        {flow.complete === false ? (
          /* 降级时数字是清零的占位，绝不能当成真实读数 */
          <p className="text-xs text-muted-foreground">暂不可用</p>
        ) : (
          <>
            <div className="space-y-2">
              {/* 双序列图例（W1）：复用 bot 用量页的 MetricChartLegend，
                  色点与两条叠加序列同色，顺序与副标题一致；单序列的
                  「每日已解决」图不用图例（组件 ≤1 系列时自动返回 null） */}
              {!chartEmpty && <MetricChartLegend points={points} />}
              {/* 叠加折线（round4 X1）：新建/已解决是两个独立流量，不是可加
                  分量——堆叠顶边=累计值会把「已解决」画成 KPI 的数倍。叠加
                  模式下两条线各自 y=自身值（新建细虚线、已解决实线），线末
                  直接标数值（X3）；末点轴区标「今日」（X7）。 */}
              <MetricChart
                points={points}
                variant="area"
                stacked={false}
                lastPointLabel="今日"
                emptyMessage="近 30 天暂无新建或已解决"
              />
            </div>

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
                        中位 {formatMedianDays(row.median_days)} ·{" "}
                        {row.completed_count} 个问题
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

/** 积压年龄三档配色（W4）：条段与下方标签前的小圆点共用同一色，
 *  新（emerald）→ 渐旧（amber → red），一眼读出年龄结构。 */
const BACKLOG_SEGMENTS: Array<{
  key: "under_7d" | "d7_to_30d" | "over_30d";
  label: string;
  className: string;
}> = [
  { key: "under_7d", label: "7 天内", className: "bg-emerald-500/70" },
  { key: "d7_to_30d", label: "7-30 天", className: "bg-amber-500/60" },
  {
    key: "over_30d",
    label: "30 天以上",
    className: "bg-red-500/60",
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
          <span
            key={segment.key}
            className="inline-flex items-center gap-1.5 tabular-nums"
          >
            {/* 标签前同色小点（W4）：与条段逐档对应，无值档也保留作图例 */}
            <span
              aria-hidden="true"
              className={`h-2 w-2 shrink-0 rounded-full ${segment.className}`}
            />
            <span>
              {segment.label} {age[segment.key]}
            </span>
          </span>
        ))}
      </div>
    </div>
  );
}
