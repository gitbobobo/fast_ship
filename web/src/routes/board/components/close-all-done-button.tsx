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
import { useCloseIssuesBatch } from "@/lib/hooks/use-issues";
import { toast } from "sonner";

type SourceFilter = "all" | "internal" | "github";

const SOURCE_OPTIONS: { value: SourceFilter; label: string }[] = [
  { value: "all", label: "全部" },
  { value: "internal", label: "内部问题" },
  { value: "github", label: "GitHub 问题" },
];

export function CloseAllDoneButton({ issues }: { issues: Issue[] }) {
  const [open, setOpen] = useState(false);
  const [sourceFilter, setSourceFilter] = useState<SourceFilter>("internal");
  const projectId = issues[0]?.project_id ?? "";

  // 每次打开弹窗时重置筛选器为默认值
  useEffect(() => {
    if (open) setSourceFilter("internal");
  }, [open]);
  const closeBatch = useCloseIssuesBatch(projectId);

  const openIssues = issues.filter((i) => i.state === "open");
  const filteredIssues = openIssues.filter((i) => {
    if (sourceFilter === "all") return true;
    return i.source === sourceFilter;
  });
  // Hooks 必须在组件顶部无条件调用，因此 early return 放在 hooks 之后
  if (openIssues.length === 0) return null;

  const handleClose = async () => {
    if (filteredIssues.length === 0) {
      toast.info("当前筛选条件下没有需要关闭的问题");
      setOpen(false);
      return;
    }

    try {
      const result = await closeBatch.mutateAsync(
        filteredIssues.map((i) => i.id),
      );
      if (result.failed > 0) {
        toast.warning(
          `已关闭 ${result.succeeded} 个问题，${result.failed} 个失败`,
        );
      } else {
        toast.success(`已关闭 ${result.succeeded} 个问题`);
      }
    } catch {
      toast.error("批量关闭失败");
    } finally {
      setOpen(false);
    }
  };

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
            这将关闭本项目看板"已完成"列中的 {filteredIssues.length} 个问题。此操作不可撤销。
          </AlertDialogDescription>
        </AlertDialogHeader>
        <div className="flex items-center gap-2 px-1">
          <span className="text-sm text-muted-foreground whitespace-nowrap">关闭范围</span>
          <Select value={sourceFilter} onValueChange={(v) => setSourceFilter(v as SourceFilter)}>
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
        <AlertDialogFooter>
          <AlertDialogCancel>取消</AlertDialogCancel>
          <AlertDialogAction
            onClick={() => void handleClose()}
            disabled={closeBatch.isPending || filteredIssues.length === 0}
          >
            {closeBatch.isPending ? "关闭中..." : "确认关闭"}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
