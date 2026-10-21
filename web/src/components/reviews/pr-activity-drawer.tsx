import { useState } from "react";
import { ChevronRight, ExternalLink } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Skeleton } from "@/components/ui/skeleton";
import {
  reviewRunStatusLabel,
  reviewTriggerActionLabel,
  type ReviewFinding,
  type ReviewPullRequestActivity,
  type ReviewRoundRecord,
  type ReviewRunSummary,
} from "@/lib/api/reviews";
import { useReviewPullRequestDetail } from "@/lib/hooks/use-reviews";
import { formatDate, shortSha } from "@/lib/utils/format";
import { cn } from "@/lib/utils";

// PR 审查详情抽屉（INT-141）：替代按项目的「审查记录」tab。打开时请求详情，
// 选中项属于进行中组时详情 10s 轮询（关闭即卸载、自动停止）。最新一轮展开，
// 更早的轮折叠成一行灰字、可点开。

export function PrActivityDrawer({
  item,
  onClose,
}: {
  item: ReviewPullRequestActivity | null;
  onClose: () => void;
}) {
  return (
    <Sheet
      open={Boolean(item)}
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <SheetContent
        side="right"
        className="w-full gap-0 overflow-y-auto p-0 sm:max-w-xl"
      >
        {item && (
          <DrawerBody
            key={`${item.project_id}/${item.repo_full_name}#${item.pr_number}`}
            item={item}
          />
        )}
      </SheetContent>
    </Sheet>
  );
}

function DrawerBody({ item }: { item: ReviewPullRequestActivity }) {
  const {
    data: detail,
    isLoading,
    isError,
    refetch,
  } = useReviewPullRequestDetail(
    item.project_id,
    item.pr_number,
    item.group === "in_progress",
  );
  // 手动展开的历史轮次；最新一轮恒展开。
  const [expandedRounds, setExpandedRounds] = useState<Set<number>>(new Set());

  const toggleRound = (roundNumber: number) =>
    setExpandedRounds((prev) => {
      const next = new Set(prev);
      if (next.has(roundNumber)) next.delete(roundNumber);
      else next.add(roundNumber);
      return next;
    });

  // 详情在防抖/无运行时可能没有标题快照（空串），用列表行兜底。
  const prTitle = detail?.pr_title || item.pr_title;
  const prUrl = detail?.pr_url || item.pr_url;
  const repo = detail?.repo_full_name ?? item.repo_full_name;
  // 最新在前展开；更早的轮折叠成灰字一行。
  const rounds = [...(detail?.rounds ?? [])].sort(
    (a, b) => b.round_number - a.round_number,
  );
  // 防抖中还没有新一轮运行：详情里的最新一轮是上一轮，不能当成当前轮展开。
  const debouncing = item.stage === "debouncing";
  const latestRound = debouncing ? undefined : rounds[0]?.round_number;

  return (
    <>
      <SheetHeader className="border-b px-4 py-3">
        <SheetTitle className="pr-8 text-base leading-snug">
          #{item.pr_number} {prTitle}
        </SheetTitle>
        <div className="flex items-center gap-2 text-xs text-muted-foreground">
          <span className="truncate">{repo}</span>
          <a
            href={prUrl}
            target="_blank"
            rel="noreferrer"
            className="inline-flex shrink-0 items-center gap-1 text-primary hover:underline"
          >
            在 GitHub 打开
            <ExternalLink className="h-3 w-3" />
          </a>
        </div>
      </SheetHeader>

      <div className="space-y-3 p-4">
        {debouncing && (
          <p className="rounded-lg bg-muted/50 px-3 py-2 text-xs text-muted-foreground">
            新提交 <span className="font-mono">{shortSha(item.head_sha)}</span>{" "}
            等待防抖结束
            {rounds.length > 0 ? "，下面是上一轮的记录。" : "，结束后开始新一轮审查。"}
          </p>
        )}
        {isLoading ? (
          <div className="space-y-2">
            {Array.from({ length: 3 }).map((_, i) => (
              <Skeleton key={i} className="h-12 rounded" />
            ))}
          </div>
        ) : isError ? (
          <div className="rounded-lg border bg-muted/40 py-8 text-center">
            <p className="text-sm text-muted-foreground">详情加载失败</p>
            <div className="mt-3">
              <button
                type="button"
                className="text-xs text-primary hover:underline"
                onClick={() => void refetch()}
              >
                重试
              </button>
            </div>
          </div>
        ) : rounds.length === 0 ? (
          <p className="rounded-lg border bg-muted/40 py-8 text-center text-sm text-muted-foreground">
            暂无审查轮次
          </p>
        ) : (
          rounds.map((round) =>
            round.round_number === latestRound ? (
              <RoundSection key={round.round_number} round={round} />
            ) : (
              <HistoryRound
                key={round.round_number}
                round={round}
                expanded={expandedRounds.has(round.round_number)}
                onToggle={() => toggleRound(round.round_number)}
              />
            ),
          )
        )}
      </div>
    </>
  );
}

/** 历史轮：一行灰字，点开显示完整内容。 */
function HistoryRound({
  round,
  expanded,
  onToggle,
}: {
  round: ReviewRoundRecord;
  expanded: boolean;
  onToggle: () => void;
}) {
  return (
    <div>
      <button
        type="button"
        aria-expanded={expanded}
        onClick={onToggle}
        className="flex w-full items-center gap-1 rounded px-1 py-1 text-left text-xs text-muted-foreground hover:bg-muted/40"
      >
        <ChevronRight
          className={cn(
            "h-3.5 w-3.5 shrink-0 transition-transform",
            expanded && "rotate-90",
          )}
        />
        第 {round.round_number} 轮 ·{" "}
        <span className="font-mono">{shortSha(round.head_sha)}</span> · 历史轮次
      </button>
      {expanded && (
        <div className="pl-4 pt-1">
          <RoundSection round={round} />
        </div>
      )}
    </div>
  );
}

function RoundSection({ round }: { round: ReviewRoundRecord }) {
  return (
    <div className="space-y-2 rounded-lg border px-3 py-2.5">
      <p className="text-xs font-medium text-muted-foreground">
        第 {round.round_number} 轮 ·{" "}
        <span className="font-mono">{shortSha(round.head_sha)}</span> ·{" "}
        {reviewTriggerActionLabel(round.trigger_action)} ·{" "}
        {formatDate(round.created_at)}
      </p>
      <div className="space-y-1.5">
        {round.code_runs.map((run) => (
          <RunBlock key={run.id} run={run} kindLabel="代码审查" />
        ))}
        {round.visual_runs.map((run) => (
          <RunBlock key={run.id} run={run} kindLabel="视觉审查" />
        ))}
      </div>
    </div>
  );
}

function RunBlock({
  run,
  kindLabel,
}: {
  run: ReviewRunSummary;
  kindLabel: string;
}) {
  const findings = run.findings ?? [];
  const unmeasurable = run.unmeasurable ?? [];
  return (
    <div className="rounded-md bg-muted/40 px-3 py-2">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm">
        <span className="text-muted-foreground">{kindLabel}</span>
        <span className="font-medium">
          {run.environment_name || run.environment_id}
        </span>
        <span className="text-muted-foreground">
          {run.instance_id} · {run.model}
          {run.options?.length
            ? ` · ${run.options.map((o) => `${o.id}=${String(o.value)}`).join(", ")}`
            : null}
        </span>
        <Badge variant={runStatusVariant(run.status)}>
          {reviewRunStatusLabel(run.status)}
        </Badge>
        {typeof run.findings_count === "number" && run.findings_count > 0 && (
          <span className="text-xs text-muted-foreground">
            {run.findings_count} 条发现
          </span>
        )}
        {run.thread_id && (
          <a
            className="ml-auto text-xs text-primary hover:underline"
            href={`/threads/${run.environment_id}/${run.thread_id}`}
          >
            线程
          </a>
        )}
      </div>
      {(run.error_message || run.error_code) && (
        <p className="mt-1 text-xs text-destructive">
          {run.error_message || run.error_code}
        </p>
      )}
      {run.summary && (
        <p className="mt-1 line-clamp-2 text-xs text-muted-foreground">
          {run.summary}
        </p>
      )}
      {findings.length > 0 && (
        <ul className="mt-2 space-y-2 border-t border-border/50 pt-2">
          {findings.map((finding, i) => (
            <FindingRow key={i} finding={finding} />
          ))}
        </ul>
      )}
      {unmeasurable.length > 0 && (
        <ul className="mt-2 space-y-2 border-t border-border/50 pt-2">
          {unmeasurable.map((item, i) => (
            <li key={i} className="text-xs">
              <div className="flex flex-wrap items-center gap-1.5">
                <Badge variant="outline">无法判断</Badge>
                <span className="font-mono text-muted-foreground">
                  {item.screenshot}
                </span>
              </div>
              <p className="mt-0.5 text-muted-foreground">{item.reason}</p>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function FindingRow({ finding }: { finding: ReviewFinding }) {
  // 代码发现定位到 文件:行号；视觉发现是 截图 · 维度。
  const location = finding.file
    ? finding.line
      ? `${finding.file}:${finding.line}`
      : finding.file
    : [finding.screenshot, finding.dimension].filter(Boolean).join(" · ");
  return (
    <li className="text-xs">
      <div className="flex flex-wrap items-center gap-1.5">
        <Badge variant={findingVariant(finding.level)}>{finding.level}</Badge>
        {location && (
          <span className="font-mono text-muted-foreground">{location}</span>
        )}
      </div>
      <p className="mt-0.5 text-foreground/90">{finding.body}</p>
      {finding.fix && (
        <p className="mt-0.5 text-muted-foreground">建议：{finding.fix}</p>
      )}
    </li>
  );
}

function findingVariant(
  level: string,
): "default" | "secondary" | "destructive" | "outline" {
  switch (level) {
    case "P0":
    case "P1":
    case "blocker":
      return "destructive";
    case "P2":
    case "worth_fixing":
      return "secondary";
    default:
      return "outline";
  }
}

function runStatusVariant(
  status: ReviewRunSummary["status"],
): "default" | "secondary" | "destructive" | "outline" {
  switch (status) {
    case "completed":
      return "secondary";
    case "failed":
    case "timed_out":
      return "destructive";
    case "cancelled":
      return "outline";
    default:
      return "default";
  }
}
