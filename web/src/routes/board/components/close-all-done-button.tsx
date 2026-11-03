import { useEffect, useState } from "react";
import { XCircle } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { OptionSelect } from "@/components/ui/option-select";
import {
  useBatchClosePreviewCount,
  useCloseIssuesBatch,
} from "@/lib/hooks/use-issues";
import {
  type IssueSourceFilter,
  ISSUE_SOURCE_FILTER_OPTIONS,
} from "@/lib/issue-source";
import { HTTPError } from "ky";
import { toast } from "sonner";

const BATCH_CLOSE_MAX = 200;

// 整请求层「结果未确认」的统一文案：远端可能已生效（部分关闭已落地），
// 误报失败的代价高于多提示一次刷新（INT-185）。
const REQUEST_UNCONFIRMED_COPY =
  "请求超时：部分问题可能已关闭，请刷新看板确认结果";

export function CloseAllDoneButton({ projectId }: { projectId: string }) {
  const [open, setOpen] = useState(false);
  const [sourceFilter, setSourceFilter] = useState<IssueSourceFilter>("internal");
  const closeBatch = useCloseIssuesBatch(projectId);
  const { data: previewCount, isLoading: isPreviewLoading } =
    useBatchClosePreviewCount(projectId, sourceFilter, open);

  useEffect(() => {
    if (open) setSourceFilter("internal");
  }, [open]);

  const exceedsLimit = (previewCount ?? 0) > BATCH_CLOSE_MAX;

  const handleClose = async () => {
    try {
      const result = await closeBatch.mutateAsync({ source: sourceFilter });
      if (result.total === 0) {
        toast.info("当前筛选条件下没有需要关闭的问题");
      } else if (result.failed > 0) {
        // 服务端已把失败文案收敛成少量固定类别（不含 URL/Issue 号，INT-185
        // R3 审计）：按文案分组计数、全部展示，每组取样例引用；不做容量截断
        // ——每新增一类服务端文案都会再次挤掉已有类别，那是三轮审查反例的根因。
        const groups = new Map<
          string,
          { count: number; sample?: string }
        >();
        for (const f of result.failures) {
          const label = f.error || "原因未说明";
          const group = groups.get(label) ?? { count: 0 };
          group.count += 1;
          group.sample ??= f.reference;
          groups.set(label, group);
        }
        const lines = [...groups.entries()].map(([label, { count, sample }]) =>
          sample ? `${label} ×${count}（如 ${sample}）` : `${label} ×${count}`,
        );
        const unlisted = result.failed - result.failures.length;
        if (unlisted > 0) lines.push(`另有 ${unlisted} 条未列明细`);
        toast.warning(`已关闭 ${result.succeeded} 个问题，${result.failed} 个失败`, {
          description: lines.join("\n"),
        });
      } else {
        toast.success(`已关闭 ${result.succeeded} 个问题`);
      }
    } catch (error) {
      toast.error(await formatBatchCloseError(error));
    } finally {
      setOpen(false);
    }
  };

  const previewText =
    isPreviewLoading || previewCount === undefined
      ? "…"
      : String(previewCount);

  return (
    <AlertDialog open={open} onOpenChange={setOpen}>
      <Button
        variant="outline"
        size="sm"
        className="h-7 text-xs"
        onClick={() => setOpen(true)}
      >
        <XCircle className="mr-1 h-3 w-3" />
        关闭全部
      </Button>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>确认关闭所有已完成问题？</AlertDialogTitle>
          <AlertDialogDescription>
            这将关闭本项目看板「已完成」列中约 {previewText}{" "}
            个符合当前筛选条件的问题。此操作不可撤销。
          </AlertDialogDescription>
        </AlertDialogHeader>
        <div className="flex items-center gap-2 px-1">
          <span className="text-sm text-muted-foreground whitespace-nowrap">
            关闭范围
          </span>
          <OptionSelect
            value={sourceFilter}
            onValueChange={(v) => setSourceFilter(v as IssueSourceFilter)}
            options={ISSUE_SOURCE_FILTER_OPTIONS}
            className="h-8 w-auto"
          />
        </div>
        {!isPreviewLoading && previewCount === 0 && (
          <p className="px-1 text-xs text-muted-foreground">
            当前来源下无可关闭项，可尝试切换为「全部」。
          </p>
        )}
        {!isPreviewLoading && exceedsLimit && (
          <p className="px-1 text-xs text-destructive">
            匹配数量超过 {BATCH_CLOSE_MAX} 条，请切换更小的来源范围后重试。
          </p>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel>取消</AlertDialogCancel>
          <AlertDialogAction
            onClick={() => void handleClose()}
            disabled={
              closeBatch.isPending ||
              isPreviewLoading ||
              previewCount === 0 ||
              exceedsLimit
            }
          >
            {closeBatch.isPending ? "关闭中..." : "确认关闭"}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

// 导出仅供单测覆盖文案分支。
export async function formatBatchCloseError(error: unknown): Promise<string> {
  if (error instanceof HTTPError) {
    const body = await error.response.json<ApiResponse<unknown>>().catch(() => null);
    if (body?.message) return body.message;
    // 网关超时（反代 502/503/504 截断长批次）不带 JSON 正文：服务端可能已
    // 关闭一部分，与 ky 超时同样按「刷新确认」处理，不能报笼统失败（INT-185）。
    const status = error.response.status;
    if (status === 502 || status === 503 || status === 504) {
      return REQUEST_UNCONFIRMED_COPY;
    }
  }
  // fetch 网络中断抛 TypeError：请求是否到达服务端未知，同样按刷新确认。
  if (error instanceof TypeError) {
    return REQUEST_UNCONFIRMED_COPY;
  }
  // ky 超时（TimeoutError，非 HTTPError）：整批请求中断，服务端可能已关闭
  // 一部分，提示刷新确认而不是笼统报「批量关闭失败」（INT-185）。
  if (error instanceof Error && error.name === "TimeoutError") {
    return REQUEST_UNCONFIRMED_COPY;
  }
  return "批量关闭失败";
}
