import type { ReactNode } from "react";
import { useMemo } from "react";
import { Link, useParams } from "react-router";
import { Inbox, TriangleAlert } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Header } from "@/components/layout/header";
import { ContextGrowthChart } from "@/components/bots/bot-usage-insights";
import { errorText } from "@/lib/api/client";
import type {
  BotUsageMaintenanceRequest,
  BotUsageMaintenanceRun,
  BotUsageRequest,
  BotUsageRun,
} from "@/lib/api/bots";
import { useBotUsageRun } from "@/lib/hooks/use-bots";
import { formatDate } from "@/lib/utils/format";
import {
  UNPRICED_REASON_NOTE,
  USAGE_PRICE_NOTE,
  formatUsageCost,
} from "@/components/bots/bot-usage-overview";

/**
 * 单次运行花费明细（INT-162）：run 摘要 + 每次模型请求（含失败重试）及其
 * 工具调用 + 关联维护运行与请求。金额口径：display_cost = 自身已计价 +
 * 关联维护已计价；可空字段为「未计价」显示「—」。
 */

const SESSION_KIND_LABELS: Record<string, string> = {
  main: "主线",
  schedule: "定时任务",
};

const RUN_STATUS_LABELS: Record<string, string> = {
  queued: "排队中",
  running: "运行中",
  completed: "已完成",
  failed: "已失败",
  stopped: "已停止",
  skipped: "已跳过",
};

const REQUEST_STATUS_LABELS: Record<string, string> = {
  running: "运行中",
  succeeded: "已成功",
  failed: "已失败",
};

const TRIGGER_LABELS: Record<string, string> = {
  user_message: "用户消息",
  tool_result: "工具结果",
  recovery: "崩溃恢复",
  schedule: "定时触发",
  manual: "手动触发",
  schedule_event: "定时任务事件",
};

const MAINTENANCE_KIND_LABELS: Record<string, string> = {
  compaction: "上下文压缩",
  nightly: "夜间整理",
};

const MAINTENANCE_PURPOSE_LABELS: Record<string, string> = {
  summary: "摘要",
  archive: "归档",
  nightly: "夜间整理",
};

const TOOL_CALL_STATUS_LABELS: Record<string, string> = {
  pending_confirmation: "待确认",
  queued: "排队中",
  running: "运行中",
  succeeded: "已成功",
  failed: "已失败",
  rejected: "已拒绝",
  cancelled: "已取消",
  not_executed: "未执行",
};

const labelOf = (map: Record<string, string>, value: string) =>
  map[value] ?? value;

const formatTokens = (n: number | null | undefined) =>
  n == null ? "—" : n.toLocaleString("en-US");

/** 起止时间差的可读时长；缺任一端（未开始/进行中）显示「—」。 */
function formatSpan(start: string | null | undefined, end: string | null | undefined) {
  if (!start || !end) return "—";
  const ms = new Date(end).getTime() - new Date(start).getTime();
  if (Number.isNaN(ms) || ms < 0) return "—";
  const seconds = ms / 1000;
  if (seconds < 60) {
    return `${seconds < 10 ? seconds.toFixed(1) : Math.round(seconds)} 秒`;
  }
  const minutes = Math.floor(seconds / 60);
  const rest = Math.round(seconds % 60);
  return rest ? `${minutes} 分 ${rest} 秒` : `${minutes} 分钟`;
}

function statusVariant(status: string) {
  if (status === "failed") return "destructive" as const;
  if (status === "running" || status === "queued") return "default" as const;
  return "secondary" as const;
}

export default function BotUsageRunPage() {
  const { runId = "" } = useParams<{ runId: string }>();
  const { data, isLoading, isError, error, refetch } = useBotUsageRun(runId);

  return (
    <div className="flex flex-col">
      <Header title="运行花费明细" backTo="/bots/usage" />
      <div className="mx-auto w-full max-w-5xl space-y-6 p-4 md:p-6">
        {isLoading ? (
          <div className="space-y-3">
            <Skeleton className="h-32 rounded-xl" />
            <Skeleton className="h-48 rounded-xl" />
          </div>
        ) : isError ? (
          <Card>
            <CardContent className="flex flex-col items-center justify-center gap-3 py-16">
              <p className="flex items-center gap-2 text-sm text-muted-foreground">
                <TriangleAlert className="h-4 w-4 text-destructive" />
                {errorText(error, "运行花费明细加载失败")}
              </p>
              <Button
                variant="outline"
                size="sm"
                onClick={() => void refetch()}
              >
                重试
              </Button>
            </CardContent>
          </Card>
        ) : !data ? null : (
          <>
            <RunSummary
              run={data.run}
              requests={data.requests}
              truncated={data.requests_truncated}
            />
            <RunContextGrowth
              requests={data.requests}
              truncated={data.requests_truncated}
            />
            <RequestList
              requests={data.requests}
              truncated={data.requests_truncated}
            />
            <MaintenanceRunList runs={data.maintenance_runs} />
          </>
        )}
      </div>
    </div>
  );
}

/** run 摘要：身份 + 状态 + display_cost 与其拆分 + token/时间口径。 */
function RunSummary({
  run,
  requests,
  truncated,
}: {
  run: BotUsageRun;
  requests: BotUsageRequest[];
  truncated?: boolean;
}) {
  // run 级 token 列缺写而请求行有值时按请求汇总兜底（标注口径），避免汇总
  // 行全「—」与下方明细自相矛盾；请求行也无值仍显示「—」（缺失 ≠ 0）。
  const sum = (pick: (r: BotUsageRequest) => number | null | undefined) => {
    let total = 0;
    let any = false;
    for (const r of requests) {
      const v = pick(r);
      if (v != null) {
        total += v;
        any = true;
      }
    }
    return any ? total : null;
  };
  // 截断后兜底汇总只覆盖最近 500 条，须显式标注，否则数字系统性偏低。
  const runTokensMissing =
    run.prompt_tokens == null ||
    run.cached_tokens == null ||
    run.completion_tokens == null ||
    run.total_tokens == null;
  const partial = truncated && runTokensMissing;
  const tokenText = run.usage_complete
    ? `${formatTokens(run.prompt_tokens ?? sum((r) => r.prompt_tokens))} 输入（含缓存 ${formatTokens(run.cached_tokens ?? sum((r) => r.cached_tokens))}）· ${formatTokens(run.completion_tokens ?? sum((r) => r.completion_tokens))} 输出 · ${formatTokens(run.total_tokens ?? sum((r) => r.total_tokens))} 合计${partial ? "（仅最近 500 条）" : ""}`
    : "usage 未收齐，token 计数不可信";

  const rows: { label: string; value: ReactNode; title?: string }[] = [
    {
      label: "会话类型",
      value: labelOf(SESSION_KIND_LABELS, run.session_kind),
    },
    {
      label: "定时任务",
      value: run.schedule_task_name ?? (run.schedule_task_id ? "已删除的任务" : "—"),
    },
    { label: "触发", value: labelOf(TRIGGER_LABELS, run.trigger_source) },
    {
      label: "成本拆分",
      value: `自身 ${formatUsageCost(run.cost_estimate)} + 维护 ${formatUsageCost(run.maintenance_cost)}`,
      title: `display_cost = 本 run 已计价成本 + 关联维护已计价成本；${USAGE_PRICE_NOTE}`,
    },
    { label: "Token", value: tokenText },
    {
      label: "时间",
      value: `${formatDate(run.created_at)} 创建 · 耗时 ${formatSpan(run.started_at, run.finished_at)}`,
    },
  ];

  return (
    <Card data-testid="usage-run-summary">
      <CardContent className="space-y-3 p-4">
        <div className="flex flex-wrap items-center gap-2">
          <Link
            to={`/bots/${run.bot_id}`}
            className="text-sm font-medium hover:underline"
          >
            {run.bot_name}
          </Link>
          <Badge variant={statusVariant(run.status)}>
            {labelOf(RUN_STATUS_LABELS, run.status)}
          </Badge>
          {run.silent && <Badge variant="secondary">静默</Badge>}
          <span
            className="ml-auto text-lg font-semibold tabular-nums"
            title={`显示成本 display_cost；${USAGE_PRICE_NOTE}`}
          >
            {formatUsageCost(run.display_cost)}
          </span>
        </div>
        <dl className="grid gap-x-6 gap-y-1.5 text-sm sm:grid-cols-2">
          {rows.map((row) => (
            <div key={row.label} className="flex items-baseline gap-3">
              <dt className="shrink-0 text-muted-foreground">{row.label}</dt>
              <dd className="min-w-0" title={row.title}>
                {row.value}
              </dd>
            </div>
          ))}
        </dl>
        {run.summary && (
          <p className="whitespace-pre-wrap rounded-lg bg-muted/50 px-3 py-2 text-xs text-muted-foreground">
            {run.summary}
          </p>
        )}
        {run.error_message && (
          <p className="text-xs text-destructive">
            {run.error_code ? `${run.error_code}：` : ""}
            {run.error_message}
          </p>
        )}
      </CardContent>
    </Card>
  );
}

/**
 * 输入上下文增长（INT-163）：x=请求序号（#1..#n），y=输入 token。
 * 值取 prompt_tokens ?? input_tokens_estimate：usage 未收齐（prompt_tokens
 * 为 null）的请求只能给估算值，图上以空心点标注并在图注说明。
 */
function RunContextGrowth({
  requests,
  truncated,
}: {
  requests: BotUsageRequest[];
  truncated?: boolean;
}) {
  const points = useMemo(
    () =>
      requests
        .filter(
          // 与服务端口径一致：usage 未收齐且无估算值的请求不产点，
          // 避免把「没有数据」画成真实的 0 输入。
          (request) =>
            request.prompt_tokens != null ||
            request.input_tokens_estimate > 0,
        )
        .map((request) => {
          const tokens = request.prompt_tokens ?? request.input_tokens_estimate;
          const estimated = request.prompt_tokens == null;
          return {
            x: request.ordinal,
            y: tokens,
            estimated,
            label: `#${request.ordinal} · ${tokens.toLocaleString("en-US")} 输入${estimated ? "（估算）" : ""}`,
          };
        }),
    [requests],
  );
  // 没有请求记录时请求列表已有空态，图不重复占位。
  if (!requests.length) return null;
  return (
    <section className="space-y-2" data-testid="usage-run-context-growth">
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5">
        <h3 className="text-sm font-medium">输入上下文增长</h3>
        <p className="text-xs text-muted-foreground">
          {truncated ? "仅最新 500 条 · " : ""}
          空心点 = 估算值（该请求缺 usage，取估算输入）
        </p>
      </div>
      <ContextGrowthChart
        points={points}
        formatXLabel={(ordinal) => `#${ordinal}`}
        chartTitle="输入上下文增长"
        titleId="usage-run-context-growth-title"
        emptyMessage="没有模型请求记录"
      />
    </section>
  );
}

/** 模型请求列表：每次请求一行（含失败重试标记），下方挂该次的工具调用。 */
function RequestList({
  requests,
  truncated,
}: {
  requests: BotUsageRequest[];
  truncated?: boolean;
}) {
  return (
    <section className="space-y-2">
      <h3 className="text-sm font-medium">
        模型请求{requests.length ? ` · ${requests.length}` : ""}
        {truncated ? "（仅最新 500 条，较早请求已省略）" : ""}
      </h3>
      {!requests.length ? (
        <div className="rounded-lg border bg-muted/40 py-8 text-center text-sm text-muted-foreground">
          <Inbox className="mx-auto mb-2 h-6 w-6 opacity-40" />
          没有模型请求记录
        </div>
      ) : (
        <ul className="space-y-2">
          {requests.map((request) => (
            <RequestRow key={request.id} request={request} />
          ))}
        </ul>
      )}
    </section>
  );
}

function RequestRow({ request }: { request: BotUsageRequest }) {
  // usage 未收齐时实际计数不可信，只展示服务端估算列。合计口径：
  // 输入（含其中缓存命中部分）+ 输出，各字段可直接相加自验。
  const actual = request.usage_complete
    ? `输入 ${formatTokens(request.prompt_tokens)}（含缓存 ${formatTokens(request.cached_tokens)}）· 输出 ${formatTokens(request.completion_tokens)} · 合计 ${formatTokens(request.total_tokens)}`
    : "usage 未收齐";

  return (
    <li
      data-testid="usage-request-row"
      className="space-y-1.5 rounded-lg border px-3 py-2.5"
    >
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-sm font-medium tabular-nums">
          #{request.ordinal}
        </span>
        <Badge variant={statusVariant(request.status)}>
          {labelOf(REQUEST_STATUS_LABELS, request.status)}
        </Badge>
        {request.is_retry && <Badge variant="secondary">重试</Badge>}
        <span className="text-xs text-muted-foreground">
          {formatDate(request.started_at)}
        </span>
        <span className="text-xs text-muted-foreground">
          耗时 {formatSpan(request.started_at, request.finished_at)}
        </span>
        <span
          className="ml-auto text-sm font-medium tabular-nums"
          title={
            request.cost_estimate != null
              ? USAGE_PRICE_NOTE
              : `未计价；${UNPRICED_REASON_NOTE}`
          }
        >
          {formatUsageCost(request.cost_estimate)}
        </span>
      </div>
      <p className="text-xs text-muted-foreground">
        估算输入 {formatTokens(request.input_tokens_estimate)} · {actual} ·
        工具定义 {formatTokens(request.tools_tokens_estimate)}
      </p>
      {request.error_message && (
        <p className="text-xs text-destructive">
          {request.error_code ? `${request.error_code}：` : ""}
          {request.error_message}
        </p>
      )}
      {request.tool_calls.length > 0 && (
        <ul className="space-y-1 border-l-2 border-border/60 pl-3">
          {request.tool_calls.map((call) => (
            <li
              key={call.id}
              className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground"
            >
              <span className="font-mono text-foreground">
                {call.tool_name}
                {call.action ? `:${call.action}` : ""}
              </span>
              <Badge variant="secondary" className="h-4 px-1.5 text-[10px]">
                {labelOf(TOOL_CALL_STATUS_LABELS, call.status)}
              </Badge>
              <span>耗时 {formatSpan(call.created_at, call.finished_at)}</span>
              {call.error_code && (
                <span className="text-destructive">{call.error_code}</span>
              )}
            </li>
          ))}
        </ul>
      )}
    </li>
  );
}

/** 关联维护运行（压缩/夜间整理）：每个运行一张卡，内列各次维护请求。 */
function MaintenanceRunList({ runs }: { runs: BotUsageMaintenanceRun[] }) {
  if (!runs.length) return null;
  return (
    <section className="space-y-2">
      <h3 className="text-sm font-medium">
        关联维护{runs.length ? ` · ${runs.length}` : ""}
      </h3>
      <ul className="space-y-2">
        {runs.map((run) => (
          <li
            key={run.id}
            data-testid="usage-maintenance-run"
            className="rounded-lg border px-3 py-2.5"
          >
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-sm font-medium">
                {labelOf(MAINTENANCE_KIND_LABELS, run.kind)}
              </span>
              <Badge variant={statusVariant(run.status)}>
                {labelOf(REQUEST_STATUS_LABELS, run.status)}
              </Badge>
              <span className="text-xs text-muted-foreground">
                {formatDate(run.created_at)}
              </span>
              <span className="text-xs text-muted-foreground">
                耗时 {formatSpan(run.started_at, run.finished_at)}
              </span>
              <span className="ml-auto text-sm font-medium tabular-nums">
                {formatUsageCost(run.cost_estimate)}
              </span>
            </div>
            {run.requests.length > 0 && (
              <ul className="mt-2 space-y-1 border-t pt-2">
                {run.requests.map((request) => (
                  <MaintenanceRequestRow
                    key={request.id}
                    request={request}
                  />
                ))}
              </ul>
            )}
          </li>
        ))}
      </ul>
    </section>
  );
}

function MaintenanceRequestRow({
  request,
}: {
  request: BotUsageMaintenanceRequest;
}) {
  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
      <span className="tabular-nums">#{request.ordinal}</span>
      <span className="text-foreground">
        {labelOf(MAINTENANCE_PURPOSE_LABELS, request.purpose)}
      </span>
      <span className="font-mono">{request.model}</span>
      <Badge
        variant={statusVariant(request.status)}
        className="h-4 px-1.5 text-[10px]"
      >
        {labelOf(REQUEST_STATUS_LABELS, request.status)}
      </Badge>
      <span>
        {request.usage_complete
          ? `输入 ${formatTokens(request.prompt_tokens)}（含缓存 ${formatTokens(request.cached_tokens)}）· 输出 ${formatTokens(request.completion_tokens)}`
          : "usage 未收齐"}
      </span>
      <span className="ml-auto tabular-nums">
        {formatUsageCost(request.cost_estimate)}
      </span>
      {request.error_message && (
        <span className="basis-full text-destructive">
          {request.error_code ? `${request.error_code}：` : ""}
          {request.error_message}
        </span>
      )}
    </li>
  );
}
