import { useState } from "react";
import { Loader2 } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { errorText } from "@/lib/api/client";
import type {
  BotBudget,
  BotBudgetPeriod,
  BotBudgetUpsertInput,
} from "@/lib/api/bots";
import {
  useBotBudgets,
  useDeleteBotBudget,
  useDeleteTaskBudget,
  useSetBotBudget,
  useSetTaskBudget,
  useTaskBudgets,
} from "@/lib/hooks/use-bots";
import { cn } from "@/lib/utils";
import { formatCostEstimate } from "./bot-metrics";

/**
 * 周期预算设置（INT-164）：机器人与定时任务各一段每日/每月预算。预算只在
 * 越过阈值时发站内通知与主线事件，不会停止运行；金额口径是已计价花费
 * （USD，目录单价估算），未计价请求不计入。
 */

/** 预算目标：机器人整体，或某个定时任务（花费只算任务口径）。 */
export type BotBudgetTarget =
  | { kind: "bot"; botId: string }
  | { kind: "task"; botId: string; taskId: string };

const PERIOD_OPTIONS: { period: BotBudgetPeriod; label: string }[] = [
  { period: "daily", label: "每日预算" },
  { period: "monthly", label: "每月预算" },
];

/** 比例 → 输入框展示文本：0.8 → "80%"、1.5 → "150%"。 */
function ratioToText(ratio: number): string {
  return `${Number((ratio * 100).toFixed(4))}%`;
}

/**
 * 阈值文本解析："80%,100%"、"80,100"、"0.8,1" 同样解析成 [0.8, 1]——
 * 大于 1 的数字按百分数处理，不大于 1 的按比例处理。去重升序后提交，
 * 与服务端规范化口径一致，回显稳定。
 */
export function parseThresholdInput(
  text: string,
): { ratios: number[] } | { error: string } {
  const tokens = text
    .split(/[,，、;；\s]+/)
    .map((token) => token.trim())
    .filter(Boolean);
  if (tokens.length === 0) {
    return { error: "请至少填写一个告警阈值" };
  }
  const ratios: number[] = [];
  for (const token of tokens) {
    // 显式百分号一律按百分数处理（「0.8%」= 0.008），不带符号才走
    // >1 启发式——否则小数加 % 会被静默放大 100 倍。
    const hasPercent = /[%％]$/.test(token);
    const raw = Number.parseFloat(token.replace(/[%％]$/, ""));
    if (!Number.isFinite(raw) || raw <= 0) {
      return { error: `阈值「${token}」无效，请填写 80% 或 0.8 这样的正数` };
    }
    const ratio = hasPercent || raw > 1 ? raw / 100 : raw;
    if (ratio > 100) {
      return { error: `阈值「${token}」超出范围（最高 10000%）` };
    }
    ratios.push(ratio);
  }
  return { ratios: [...new Set(ratios)].sort((a, b) => a - b) };
}

/** 单个周期的预算表单：金额必填正数，阈值文本解析成比例数组。 */
function BudgetPeriodForm({
  period,
  label,
  budget,
  pending,
  onSave,
  onDelete,
}: {
  period: BotBudgetPeriod;
  label: string;
  budget: BotBudget | undefined;
  pending: boolean;
  onSave: (
    period: BotBudgetPeriod,
    input: BotBudgetUpsertInput,
  ) => Promise<BotBudget>;
  onDelete: (period: BotBudgetPeriod) => Promise<unknown>;
}) {
  // 挂载时查询已完成（加载态在父级拦下），直接从已存预算回填。
  const [amount, setAmount] = useState(
    budget ? String(budget.amount_usd) : "",
  );
  const [thresholds, setThresholds] = useState(
    budget ? budget.thresholds.map(ratioToText).join("，") : "",
  );
  const [problem, setProblem] = useState<string | null>(null);

  const spendPct =
    budget && budget.amount_usd > 0
      ? (budget.spend_usd / budget.amount_usd) * 100
      : 0;

  const handleSave = async () => {
    const parsedAmount = Number.parseFloat(amount);
    if (!Number.isFinite(parsedAmount) || parsedAmount <= 0) {
      setProblem("金额需为大于 0 的数字（USD）");
      return;
    }
    const parsed = parseThresholdInput(thresholds);
    if ("error" in parsed) {
      setProblem(parsed.error);
      return;
    }
    setProblem(null);
    try {
      const saved = await onSave(period, {
        amount_usd: parsedAmount,
        thresholds: parsed.ratios,
      });
      // 回显服务端规范化结果（阈值已去重升序），与下次打开看到的一致。
      setAmount(String(saved.amount_usd));
      setThresholds(saved.thresholds.map(ratioToText).join("，"));
      toast.success(`${label}已保存`);
    } catch (err) {
      toast.error(errorText(err, "预算保存失败"));
    }
  };

  const handleDelete = async () => {
    try {
      await onDelete(period);
      setAmount("");
      setThresholds("");
      setProblem(null);
      toast.success(`${label}已清除`);
    } catch (err) {
      toast.error(errorText(err, "预算清除失败"));
    }
  };

  return (
    <section
      className="space-y-2.5 rounded-lg border px-3 py-3"
      data-testid={`budget-period-${period}`}
    >
      <div className="flex items-baseline gap-2">
        <h4 className="text-sm font-medium">{label}</h4>
        <span className="text-xs text-muted-foreground">
          {budget ? `当前周期 ${budget.period_key}` : "未设置"}
        </span>
      </div>

      {budget && (
        <div className="space-y-1">
          <p className="text-xs text-muted-foreground tabular-nums">
            已花费 {formatCostEstimate(budget.spend_usd)} /{" "}
            {formatCostEstimate(budget.amount_usd)}
            （{Math.round(spendPct)}%）
          </p>
          <div
            className="h-1.5 w-full overflow-hidden rounded-full bg-muted"
            role="progressbar"
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={Math.min(Math.round(spendPct), 100)}
            aria-label={`${label}使用进度`}
            title={`统计窗口 ${budget.spend_window_start.slice(0, 16).replace("T", " ")} 起`}
          >
            <div
              className={cn(
                "h-full rounded-full transition-all",
                spendPct >= 100 ? "bg-destructive" : "bg-primary",
              )}
              style={{ width: `${Math.min(Math.max(spendPct, 0), 100)}%` }}
            />
          </div>
        </div>
      )}

      <div className="grid gap-2 sm:grid-cols-2">
        <div className="space-y-1.5">
          <Label htmlFor={`budget-amount-${period}`}>金额（USD）</Label>
          <Input
            id={`budget-amount-${period}`}
            data-testid={`budget-amount-${period}`}
            inputMode="decimal"
            value={amount}
            onChange={(e) => setAmount(e.target.value)}
            placeholder="例如：10"
          />
        </div>
        <div className="space-y-1.5">
          <Label htmlFor={`budget-thresholds-${period}`}>告警阈值</Label>
          <Input
            id={`budget-thresholds-${period}`}
            data-testid={`budget-thresholds-${period}`}
            value={thresholds}
            onChange={(e) => setThresholds(e.target.value)}
            placeholder="例如：80%,100%"
          />
        </div>
      </div>
      <p className="text-xs text-muted-foreground">
        阈值是相对金额的比例，可超过 100%（如 150%）；越过阈值只发提醒，不会停止运行。
      </p>

      {problem && (
        <p role="alert" className="text-xs text-destructive">
          {problem}
        </p>
      )}

      <div className="flex items-center gap-2">
        <Button
          type="button"
          size="sm"
          data-testid={`budget-save-${period}`}
          disabled={pending}
          onClick={() => void handleSave()}
        >
          {pending && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
          保存
        </Button>
        {budget && (
          <Button
            type="button"
            size="sm"
            variant="outline"
            data-testid={`budget-clear-${period}`}
            disabled={pending}
            onClick={() => void handleDelete()}
          >
            清除
          </Button>
        )}
      </div>
    </section>
  );
}

/** 加载/错误态之外的对话框主体：两个周期各一段，数据与变更由注入方提供。 */
function BudgetsBody({
  isLoading,
  isError,
  error,
  budgets,
  onRetry,
  pending,
  onSave,
  onDelete,
  onClose,
}: {
  isLoading: boolean;
  isError: boolean;
  error: unknown;
  budgets: BotBudget[] | undefined;
  onRetry: () => void;
  pending: boolean;
  onSave: (
    period: BotBudgetPeriod,
    input: BotBudgetUpsertInput,
  ) => Promise<BotBudget>;
  onDelete: (period: BotBudgetPeriod) => Promise<unknown>;
  onClose: () => void;
}) {
  return (
    <Dialog open onOpenChange={(next) => !next && onClose()}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-md">
        <DialogHeader>
          <DialogTitle>预算设置</DialogTitle>
          <DialogDescription>
            预算只用于告警提醒，不会停止机器人运行；金额口径是已计价花费
            （USD，按内置目录单价估算）。
          </DialogDescription>
        </DialogHeader>
        {isError ? (
          <div className="rounded-lg border bg-muted/40 px-4 py-6 text-center">
            <p className="text-sm text-muted-foreground">
              {errorText(error, "预算加载失败")}
            </p>
            <Button
              variant="outline"
              size="sm"
              className="mt-3"
              onClick={onRetry}
            >
              重试
            </Button>
          </div>
        ) : isLoading ? (
          <div className="space-y-2 py-1">
            <Skeleton className="h-40 rounded-lg" />
            <Skeleton className="h-40 rounded-lg" />
          </div>
        ) : (
          <div className="space-y-3">
            {PERIOD_OPTIONS.map(({ period, label }) => (
              <BudgetPeriodForm
                key={period}
                period={period}
                label={label}
                budget={budgets?.find((b) => b.period === period)}
                pending={pending}
                onSave={onSave}
                onDelete={onDelete}
              />
            ))}
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

function BotBudgetsDialogContent({
  botId,
  onClose,
}: {
  botId: string;
  onClose: () => void;
}) {
  const budgetsQuery = useBotBudgets(botId);
  const setBudget = useSetBotBudget(botId);
  const removeBudget = useDeleteBotBudget(botId);
  return (
    <BudgetsBody
      isLoading={budgetsQuery.isLoading}
      isError={budgetsQuery.isError}
      error={budgetsQuery.error}
      budgets={budgetsQuery.data}
      onRetry={() => void budgetsQuery.refetch()}
      pending={setBudget.isPending || removeBudget.isPending}
      onSave={(period, input) => setBudget.mutateAsync({ period, input })}
      onDelete={(period) => removeBudget.mutateAsync(period)}
      onClose={onClose}
    />
  );
}

function TaskBudgetsDialogContent({
  botId,
  taskId,
  onClose,
}: {
  botId: string;
  taskId: string;
  onClose: () => void;
}) {
  const budgetsQuery = useTaskBudgets(botId, taskId);
  const setBudget = useSetTaskBudget(botId, taskId);
  const removeBudget = useDeleteTaskBudget(botId, taskId);
  return (
    <BudgetsBody
      isLoading={budgetsQuery.isLoading}
      isError={budgetsQuery.isError}
      error={budgetsQuery.error}
      budgets={budgetsQuery.data}
      onRetry={() => void budgetsQuery.refetch()}
      pending={setBudget.isPending || removeBudget.isPending}
      onSave={(period, input) => setBudget.mutateAsync({ period, input })}
      onDelete={(period) => removeBudget.mutateAsync(period)}
      onClose={onClose}
    />
  );
}

/** 花费总览行的预算设置弹窗；关闭即卸载，未保存的输入不保留。 */
export function BotBudgetDialog({
  target,
  open,
  onClose,
}: {
  target: BotBudgetTarget;
  open: boolean;
  onClose: () => void;
}) {
  if (!open) return null;
  return target.kind === "task" ? (
    <TaskBudgetsDialogContent
      key={`${target.botId}/${target.taskId}`}
      botId={target.botId}
      taskId={target.taskId}
      onClose={onClose}
    />
  ) : (
    <BotBudgetsDialogContent
      key={target.botId}
      botId={target.botId}
      onClose={onClose}
    />
  );
}
