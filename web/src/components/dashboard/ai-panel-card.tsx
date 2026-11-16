import { Bot, Gauge, GitPullRequest, Hourglass } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import {
  SectionHeader,
  SectionUnavailable,
  formatScheduleTime,
} from "@/components/dashboard/panel-bits";
import {
  accountRemaining,
  quotaAccountGroup,
  quotaUsageTone,
  type QuotaUsageTone,
} from "@/lib/quota";
import type { components } from "@/types/api.gen";
import { cn } from "@/lib/utils";

type AiBlock = components["schemas"]["DashboardAi"];

const QUOTA_STATUS_BADGE: Record<string, string> = {
  pending: "待抓取",
  credential_invalid: "凭据失效",
  provider_error: "服务商异常",
};

/**
 * AI 运行面板卡（INT-235 阶段三）：T3 线程、PR 审查四分桶、机器人近 7 天
 * 用量与套餐额度。从仪表盘页拆出（panel P8），纯展示，数据由页面装配。
 */
export function AiPanelCard({ ai }: { ai: AiBlock }) {
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

function AiT3Section({ t3 }: { t3: NonNullable<AiBlock["t3"]> }) {
  // 降级不整块短路（panel P3）：可读环境的行照常展示，不可读的行才标
  // 「本轮不可读」；有环境行且降级时区块标头加琥珀色「部分环境暂不可用」
  // 徽章（与「待我处理」t3_waiting 组的琥珀徽章同款）。只有 environments
  // 为空且降级才是整块「暂不可用」。
  const degraded = t3.complete === false;
  return (
    <section className="space-y-2 py-3 first:pt-0" data-testid="ai-t3-section">
      <SectionHeader
        icon={Hourglass}
        title="T3 线程"
        trailing={
          degraded && t3.environments.length > 0 ? (
            <span className="rounded-full bg-amber-500/10 px-2 py-0.5 text-[11px] font-medium text-amber-700 dark:text-amber-400">
              部分环境暂不可用
            </span>
          ) : undefined
        }
      />
      {t3.environments.length === 0 ? (
        degraded ? (
          <SectionUnavailable />
        ) : (
          <p className="text-xs text-muted-foreground">没有活跃线程</p>
        )
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
                  ? // 排队是 running 的先导阶段（已含在 running 内），并排
                    // 「排队 K」会被读成并集；括号注记表达包含关系（panel P13）。
                    env.queued > 0
                      ? `运行 ${env.running}（含排队 ${env.queued}）· 等待 ${env.waiting}`
                      : `运行 ${env.running} · 等待 ${env.waiting}`
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
