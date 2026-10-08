import { useEffect, useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { useUpdateScreenshotScreen } from "@/lib/hooks/use-screenshots";

interface ScreenshotEditDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  projectId: string;
  screen: ScreenshotScreen;
}

export function ScreenshotEditDialog({
  open,
  onOpenChange,
  projectId,
  screen,
}: ScreenshotEditDialogProps) {
  const [group, setGroup] = useState(screen.group);
  const [title, setTitle] = useState(screen.title);
  const updateScreen = useUpdateScreenshotScreen(projectId);

  useEffect(() => {
    if (!open) return;
    setGroup(screen.group);
    setTitle(screen.title);
  }, [open, screen.group, screen.title]);

  const handleSubmit = async () => {
    try {
      await updateScreen.mutateAsync({
        screenId: screen.id,
        // group/title 空串合法：分别表示未分组、回退显示 screen_key
        payload: { group: group.trim(), title: title.trim() },
      });
      toast.success("已保存");
      onOpenChange(false);
    } catch {
      toast.error("保存失败，请稍后重试");
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>编辑界面</DialogTitle>
        </DialogHeader>

        <div className="space-y-4">
          <div className="space-y-2">
            <Label>界面标识</Label>
            <Input value={screen.screen_key} disabled />
          </div>
          <div className="space-y-2">
            <Label htmlFor="edit-screenshot-title">标题</Label>
            <Input
              id="edit-screenshot-title"
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              maxLength={200}
              placeholder="留空时显示界面标识"
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="edit-screenshot-group">分组</Label>
            <Input
              id="edit-screenshot-group"
              value={group}
              onChange={(e) => setGroup(e.target.value)}
              maxLength={100}
              placeholder="留空为未分组"
            />
          </div>

          <div className="flex gap-3 pt-2">
            <Button
              type="button"
              onClick={() => void handleSubmit()}
              disabled={updateScreen.isPending}
            >
              {updateScreen.isPending ? "保存中..." : "保存"}
            </Button>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
            >
              取消
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
