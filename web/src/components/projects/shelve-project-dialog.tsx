import { useEffect, useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useShelveProject } from "@/lib/hooks/use-projects";

function localTodayString(): string {
  const d = new Date();
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

// 截止日期的语义是「搁置到当天结束」：按本地 23:59:59.999 转 RFC3339 提交。
function untilFromDateInput(dateStr: string): string | undefined {
  if (!dateStr) return undefined;
  return new Date(`${dateStr}T23:59:59.999`).toISOString();
}

export function ShelveProjectDialog({
  project,
  onClose,
}: {
  project: Project | null;
  onClose: () => void;
}) {
  const shelveProject = useShelveProject();
  const [untilDate, setUntilDate] = useState("");
  const open = project !== null;

  // 每次打开都回到无限期，上次填的日期不带到下一个项目
  useEffect(() => {
    if (open) setUntilDate("");
  }, [open]);

  const handleConfirm = async () => {
    if (!project) return;
    // 契约要求 until 晚于当前时间；选当天按当天结束算仍合法，早于今天直接拦下
    if (untilDate && untilDate < localTodayString()) {
      toast.error("截止日期不能早于今天");
      return;
    }
    const until = untilFromDateInput(untilDate);
    try {
      await shelveProject.mutateAsync({ id: project.id, until });
      toast.success(until ? `已搁置至 ${untilDate}` : "项目已搁置");
      onClose();
    } catch {
      toast.error("搁置项目失败");
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(nextOpen) => {
        if (!nextOpen) onClose();
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>搁置项目</DialogTitle>
          <DialogDescription>
            搁置后「{project?.name}」将收进项目页底部的「已搁置」分组，切换器
            默认不再列出；链接仍可直接访问，设置截止日期的到期自动恢复，
            也可随时取消搁置。
          </DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-2">
          <Label htmlFor="shelve-until">截止日期（留空则无限期）</Label>
          <Input
            id="shelve-until"
            type="date"
            value={untilDate}
            onChange={(event) => setUntilDate(event.target.value)}
          />
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            取消
          </Button>
          <Button onClick={handleConfirm} disabled={shelveProject.isPending}>
            确认搁置
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
