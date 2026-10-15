import { useEffect, useState } from "react";
import { toast } from "sonner";
import { ApiRequestError } from "@/lib/api/client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  OptionSelect,
  type OptionSelectOption,
} from "@/components/ui/option-select";
import { useUpdateScreenshotScreen } from "@/lib/hooks/use-screenshots";

interface ScreenshotEditDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  projectId: string;
  screen: ScreenshotScreen;
}

const THEME_OPTIONS: OptionSelectOption[] = [
  { value: "", label: "未标注" },
  { value: "light", label: "亮色" },
  { value: "dark", label: "暗色" },
];

/** 编辑截图：标题与三个变体维度；维度空串 = 清除为未标注 */
export function ScreenshotEditDialog({
  open,
  onOpenChange,
  projectId,
  screen,
}: ScreenshotEditDialogProps) {
  const [title, setTitle] = useState(screen.title);
  const [platform, setPlatform] = useState(screen.platform ?? "");
  const [locale, setLocale] = useState(screen.locale ?? "");
  const [theme, setTheme] = useState(screen.theme ?? "");
  const updateScreen = useUpdateScreenshotScreen(projectId);

  useEffect(() => {
    if (!open) return;
    setTitle(screen.title);
    setPlatform(screen.platform ?? "");
    setLocale(screen.locale ?? "");
    setTheme(screen.theme ?? "");
  }, [open, screen]);

  const handleSubmit = async () => {
    try {
      await updateScreen.mutateAsync({
        screenId: screen.id,
        // title/维度空串合法：标题回退显示 screen_key，维度清除为未标注
        payload: {
          title: title.trim(),
          platform: platform.trim(),
          locale: locale.trim(),
          theme,
        },
      });
      toast.success("已保存");
      onOpenChange(false);
    } catch (error) {
      const message =
        error instanceof ApiRequestError && error.code === 40921
          ? "相同界面标识与平台/语言/亮度组合的截图已存在"
          : "保存失败，请稍后重试";
      toast.error(message);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>编辑截图</DialogTitle>
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
            <Label htmlFor="edit-screenshot-platform">平台</Label>
            <Input
              id="edit-screenshot-platform"
              value={platform}
              onChange={(e) => setPlatform(e.target.value)}
              maxLength={100}
              placeholder="如 web / ios；留空为未标注"
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="edit-screenshot-locale">语言</Label>
            <Input
              id="edit-screenshot-locale"
              value={locale}
              onChange={(e) => setLocale(e.target.value)}
              maxLength={35}
              placeholder="BCP 47，如 zh-CN；留空为未标注"
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="edit-screenshot-theme">亮度</Label>
            <OptionSelect
              value={theme}
              onValueChange={(v) => setTheme(v ?? "")}
              options={THEME_OPTIONS}
              className="w-full"
              aria-label="亮度"
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
