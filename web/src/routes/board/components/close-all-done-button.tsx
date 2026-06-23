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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  useBatchClosePreviewCount,
  useCloseIssuesBatch,
} from "@/lib/hooks/use-issues";
import { HTTPError } from "ky";
import { toast } from "sonner";

type SourceFilter = "all" | "internal" | "github";

const BATCH_CLOSE_MAX = 200;

const SOURCE_OPTIONS: { value: SourceFilter; label: string }[] = [
  { value: "all", label: "全部" },
  { value: "internal", label: "内部问题" },
  { value: "github", label: "GitHub 问题" },
];

export function CloseAllDoneButton({ projectId }: { projectId: string }) {
  const [open, setOpen] = useState(false);
  const [sourceFilter, setSourceFilter] = useState<SourceFilter>("internal");
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
        const refs = result.failures
          .slice(0, 3)
          .map((f) => f.reference ?? f.id)
          .join("、");
        toast.warning(
          `已关闭 ${result.succeeded} 个问题，${result.failed} 个失败${refs ? `（${refs}）` : ""}`,
        );
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
          <Select
            value={sourceFilter}
            onValueChange={(v) => setSourceFilter(v as SourceFilter)}
          >
            <SelectTrigger className="h-8 w-auto">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {SOURCE_OPTIONS.map((opt) => (
                <SelectItem key={opt.value} value={opt.value}>
                  {opt.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
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

async function formatBatchCloseError(error: unknown): Promise<string> {
  if (error instanceof HTTPError) {
    const body = await error.response.json<ApiResponse<unknown>>().catch(() => null);
    if (body?.message) return body.message;
  }
  return "批量关闭失败";
}
