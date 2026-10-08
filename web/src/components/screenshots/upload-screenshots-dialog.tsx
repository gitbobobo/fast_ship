import { useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import { ImagePlus, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { useUploadScreenshots } from "@/lib/hooks/use-screenshots";
import { normalizeScreenKey } from "@/lib/screenshot-upload";
import { formatFileSize } from "@/lib/utils/format";

interface UploadScreenshotsDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  projectId: string;
}

export function UploadScreenshotsDialog({
  open,
  onOpenChange,
  projectId,
}: UploadScreenshotsDialogProps) {
  const fileInputRef = useRef<HTMLInputElement>(null);
  const [files, setFiles] = useState<File[]>([]);
  const [group, setGroup] = useState("");
  const [screenKey, setScreenKey] = useState("");
  const [screenKeyEdited, setScreenKeyEdited] = useState(false);
  const [title, setTitle] = useState("");
  const [note, setNote] = useState("");
  const [progress, setProgress] = useState<{ done: number; total: number } | null>(
    null,
  );

  const upload = useUploadScreenshots(projectId);
  const single = files.length === 1;

  // 单文件时 screen_key 默认取规范化后的文件名，用户手动改过就不再覆盖
  useEffect(() => {
    if (single && !screenKeyEdited) {
      setScreenKey(normalizeScreenKey(files[0].name));
    }
  }, [files, single, screenKeyEdited]);

  useEffect(() => {
    if (!open) return;
    setFiles([]);
    setGroup("");
    setScreenKey("");
    setScreenKeyEdited(false);
    setTitle("");
    setNote("");
    setProgress(null);
  }, [open]);

  const fileIdentity = (f: File) => `${f.name}:${f.size}:${f.lastModified}`;

  const addFiles = (list: FileList | null) => {
    if (!list) return;
    const images = Array.from(list).filter((f) =>
      f.type.startsWith("image/"),
    );
    setFiles((prev) => {
      const existing = new Set(prev.map(fileIdentity));
      return [
        ...prev,
        ...images.filter((f) => !existing.has(fileIdentity(f))),
      ];
    });
    if (fileInputRef.current) fileInputRef.current.value = "";
  };

  const screenKeyMissing = single && !screenKey.trim();
  const canSubmit =
    files.length > 0 && !screenKeyMissing && !upload.isPending;

  const handleSubmit = async () => {
    if (!canSubmit) return;
    try {
      const result = await upload.mutateAsync({
        files,
        meta: {
          // 留空不传该字段，保留界面原分组；置为未分组走编辑对话框
          group: group.trim() || undefined,
          screenKey: screenKey.trim() || undefined,
          title: title.trim() || undefined,
          note: note.trim() || undefined,
        },
        onProgress: (done, total) => setProgress({ done, total }),
      });
      if (result.failed.length === 0) {
        toast.success(`已上传 ${result.succeeded.length} 张截图`);
        onOpenChange(false);
      } else if (result.succeeded.length === 0) {
        toast.error(`上传失败：${result.failed[0].message}`);
      } else {
        toast.warning(
          `成功 ${result.succeeded.length} 张，失败 ${result.failed.length} 张（${result.failed
            .map((f) => f.file.name)
            .join("、")}）`,
        );
        onOpenChange(false);
      }
    } catch {
      toast.error("上传失败，请稍后重试");
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg max-h-[90vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>上传截图</DialogTitle>
        </DialogHeader>

        <div className="space-y-4">
          <div className="space-y-2">
            <Label>图片文件</Label>
            <Button
              type="button"
              variant="outline"
              className="w-full"
              onClick={() => fileInputRef.current?.click()}
              disabled={upload.isPending}
            >
              <ImagePlus className="mr-1.5 h-4 w-4" />
              选择图片（可多选）
            </Button>
            <input
              ref={fileInputRef}
              type="file"
              multiple
              accept="image/*"
              className="hidden"
              onChange={(e) => addFiles(e.target.files)}
              disabled={upload.isPending}
            />
            {files.length > 0 && (
              <ul className="max-h-40 space-y-1 overflow-y-auto rounded-md border p-2">
                {files.map((file, index) => (
                  <li
                    key={`${file.name}-${index}`}
                    className="flex items-center justify-between gap-2 text-xs"
                  >
                    <span className="min-w-0 truncate font-mono">
                      {file.name}
                    </span>
                    <span className="flex shrink-0 items-center gap-2 text-muted-foreground">
                      {formatFileSize(file.size)}
                      <button
                        type="button"
                        className="rounded p-0.5 hover:bg-accent hover:text-foreground"
                        onClick={() =>
                          setFiles((prev) =>
                            prev.filter((_, i) => i !== index),
                          )
                        }
                        disabled={upload.isPending}
                        aria-label={`移除 ${file.name}`}
                      >
                        <X className="h-3 w-3" />
                      </button>
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </div>

          <div className="space-y-2">
            <Label htmlFor="screenshot-group">分组（可选）</Label>
            <Input
              id="screenshot-group"
              value={group}
              onChange={(e) => setGroup(e.target.value)}
              maxLength={100}
              placeholder="留空保持原分组；多文件时本批共用"
              disabled={upload.isPending}
            />
          </div>

          {single && (
            <>
              <div className="space-y-2">
                <Label htmlFor="screenshot-key">界面标识</Label>
                <Input
                  id="screenshot-key"
                  value={screenKey}
                  onChange={(e) => {
                    setScreenKey(e.target.value);
                    setScreenKeyEdited(true);
                  }}
                  maxLength={100}
                  placeholder="screen_key，如 home / settings"
                  disabled={upload.isPending}
                />
                {screenKeyMissing ? (
                  <p className="text-xs text-destructive">界面标识不能为空</p>
                ) : (
                  <p className="text-xs text-muted-foreground">
                    相同界面标识的截图会聚合为同一界面的不同版本
                  </p>
                )}
              </div>

              <div className="space-y-2">
                <Label htmlFor="screenshot-title">标题（可选）</Label>
                <Input
                  id="screenshot-title"
                  value={title}
                  onChange={(e) => setTitle(e.target.value)}
                  maxLength={200}
                  placeholder="留空时显示界面标识"
                  disabled={upload.isPending}
                />
              </div>

              <div className="space-y-2">
                <Label htmlFor="screenshot-note">版本备注（可选）</Label>
                <Input
                  id="screenshot-note"
                  value={note}
                  onChange={(e) => setNote(e.target.value)}
                  maxLength={1000}
                  placeholder="如「改版前」「v2.0 新布局」"
                  disabled={upload.isPending}
                />
              </div>
            </>
          )}

          {files.length > 1 && (
            <p className="text-xs text-muted-foreground">
              已选 {files.length} 张图片，每张将按文件名归入对应界面标识
            </p>
          )}

          {progress && upload.isPending && (
            <p className="text-xs text-muted-foreground">
              正在上传 {progress.done}/{progress.total} ...
            </p>
          )}

          <div className="flex gap-3 pt-2">
            <Button
              type="button"
              onClick={() => void handleSubmit()}
              disabled={!canSubmit}
            >
              {upload.isPending ? "上传中..." : "上传"}
            </Button>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
              disabled={upload.isPending}
            >
              取消
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
