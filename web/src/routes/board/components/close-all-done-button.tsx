import { useState } from "react";
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
import { useCloseIssuesBatch } from "@/lib/hooks/use-issues";
import { toast } from "sonner";

export function CloseAllDoneButton({ issues }: { issues: Issue[] }) {
  const [open, setOpen] = useState(false);
  const projectId = issues[0]?.project_id ?? "";
  const closeBatch = useCloseIssuesBatch(projectId);

  const openCount = issues.filter((i) => i.state === "open").length;
  // Hooks 必须在组件顶部无条件调用，因此 early return 放在 hooks 之后
  if (openCount === 0) return null;

  const handleClose = async () => {
    const openIssues = issues.filter((i) => i.state === "open");

    try {
      const result = await closeBatch.mutateAsync(
        openIssues.map((i) => i.id),
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
            这将关闭本项目看板"已完成"列中的 {openCount} 个问题。此操作不可撤销。
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>取消</AlertDialogCancel>
          <AlertDialogAction
            onClick={() => void handleClose()}
            disabled={closeBatch.isPending}
          >
            {closeBatch.isPending ? "关闭中..." : "确认关闭"}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
