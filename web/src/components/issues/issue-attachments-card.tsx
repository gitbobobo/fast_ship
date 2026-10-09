import { useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import {
  Download,
  File,
  FileArchive,
  FileAudio,
  FileImage,
  FileText,
  FileVideo,
  Paperclip,
  Trash2,
  Upload,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Card,
  CardAction,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { attachmentApi } from "@/lib/api/attachments";
import {
  useDeleteIssueAttachment,
  useUploadIssueAttachment,
} from "@/lib/hooks/use-issues";
import { cn } from "@/lib/utils";
import { formatFileSize, formatDate } from "@/lib/utils/format";

// 按 MIME 类型挑行首图标，未识别的回落到通用文件图标
function attachmentIcon(mimeType: string) {
  const mime = mimeType.toLowerCase();
  if (mime.startsWith("image/")) return FileImage;
  if (mime.startsWith("video/")) return FileVideo;
  if (mime.startsWith("audio/")) return FileAudio;
  if (/zip|tar|gzip|7z|rar|bzip|x-xz|archive/.test(mime)) return FileArchive;
  if (mime === "application/pdf" || mime.startsWith("text/")) return FileText;
  return File;
}

interface UploadProgressState {
  currentFileName: string;
  currentFileIndex: number;
  totalFiles: number;
  failedFiles: number;
  percent: number;
  status: "uploading" | "completed" | "failed";
}

export function IssueAttachmentsCard({
  issueId,
  attachments,
}: {
  issueId: string;
  attachments?: IssueAttachment[];
}) {
  const fileInputRef = useRef<HTMLInputElement>(null);
  const clearProgressTimerRef = useRef<number | null>(null);
  // 并发批次拦截用 ref 而非 state：state 要等下一次渲染才生效
  const isUploadingRef = useRef(false);
  const [uploadProgress, setUploadProgress] =
    useState<UploadProgressState | null>(null);
  const uploadAttachment = useUploadIssueAttachment(issueId);
  const deleteAttachment = useDeleteIssueAttachment(issueId);

  const items = attachments ?? [];
  const isUploading = uploadProgress?.status === "uploading";

  useEffect(() => {
    return () => {
      if (clearProgressTimerRef.current !== null) {
        window.clearTimeout(clearProgressTimerRef.current);
      }
    };
  }, []);

  // 多选文件逐个上传；单文件失败不中断后续文件
  const handleUploadFiles = async (files: File[]) => {
    if (files.length === 0 || isUploadingRef.current) return;
    isUploadingRef.current = true;

    // 上一批遗留的清理定时器会误清本批进度，开新批次前先作废
    if (clearProgressTimerRef.current !== null) {
      window.clearTimeout(clearProgressTimerRef.current);
      clearProgressTimerRef.current = null;
    }

    let failedFiles = 0;
    try {
      for (let i = 0; i < files.length; i++) {
        const file = files[i];
        setUploadProgress({
          currentFileName: file.name,
          currentFileIndex: i + 1,
          totalFiles: files.length,
          failedFiles,
          percent: 0,
          status: "uploading",
        });
        try {
          const formData = new FormData();
          formData.append("file", file);
          await uploadAttachment.mutateAsync({
            formData,
            onProgress: (percent) => {
              setUploadProgress((prev) =>
                prev ? { ...prev, percent } : null,
              );
            },
          });
          toast.success(`${file.name} 上传成功`);
        } catch {
          // 批次未结束状态保持 uploading：按钮禁用与并发拦截都依赖它
          failedFiles += 1;
          setUploadProgress((prev) =>
            prev ? { ...prev, failedFiles } : null,
          );
          toast.error(`${file.name} 上传失败`);
        }
      }

      setUploadProgress((prev) =>
        prev
          ? {
              ...prev,
              status: failedFiles > 0 ? "failed" : "completed",
              percent: 100,
            }
          : null,
      );
      clearProgressTimerRef.current = window.setTimeout(
        () => setUploadProgress(null),
        2000,
      );
    } finally {
      isUploadingRef.current = false;
    }
  };

  const handleUploadInput = (e: React.ChangeEvent<HTMLInputElement>) => {
    const files = Array.from(e.target.files || []);
    void handleUploadFiles(files);
    e.target.value = "";
  };

  const handleDelete = async (attachment: IssueAttachment) => {
    try {
      await deleteAttachment.mutateAsync(attachment.id);
      toast.success(`${attachment.file_name} 已删除`);
    } catch {
      toast.error("删除失败");
    }
  };

  return (
    <Card className="shadow-md hover:shadow-lg transition-shadow">
      <CardHeader className="items-center gap-3 space-y-0 border-b px-5 pb-4 pt-5">
        <div className="flex min-w-0 flex-wrap items-center gap-2.5">
          <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-primary/10">
            <Paperclip className="h-4 w-4 text-primary" />
          </div>
          <CardTitle className="text-base">附件</CardTitle>
          {items.length > 0 && (
            <Badge
              variant="secondary"
              className="text-xs font-semibold bg-primary/10 text-primary border-primary/20"
            >
              {items.length}
            </Badge>
          )}
        </div>
        <CardAction>
          <Button
            variant="outline"
            size="sm"
            className="shadow-sm"
            onClick={() => fileInputRef.current?.click()}
            disabled={isUploading}
          >
            <Upload className="mr-1.5 h-3.5 w-3.5" />
            {isUploading ? "上传中..." : "上传附件"}
          </Button>
          <input
            ref={fileInputRef}
            type="file"
            multiple
            className="hidden"
            aria-label="选择附件文件"
            onChange={handleUploadInput}
            disabled={isUploading}
          />
        </CardAction>
      </CardHeader>
      <CardContent className="space-y-4 px-5 pb-5 pt-5">
        {uploadProgress && (
          <div className="rounded-lg border px-4 py-3.5 space-y-2.5">
            <div className="flex items-center justify-between gap-3 text-sm">
              <div className="min-w-0">
                <p className="truncate font-medium">
                  {uploadProgress.currentFileName}
                </p>
                <p className="text-xs text-muted-foreground">
                  {uploadProgress.status === "uploading"
                    ? `正在上传第 ${uploadProgress.currentFileIndex}/${uploadProgress.totalFiles} 个文件`
                    : uploadProgress.status === "completed"
                      ? "上传完成"
                      : `上传结束，失败 ${uploadProgress.failedFiles} 个`}
                </p>
              </div>
              <span className="shrink-0 text-sm font-medium">
                {uploadProgress.percent}%
              </span>
            </div>
            <div className="h-2 overflow-hidden rounded-full bg-muted">
              <div
                className={cn(
                  "h-full transition-all",
                  uploadProgress.status === "failed"
                    ? "bg-destructive"
                    : "bg-primary",
                )}
                style={{ width: `${uploadProgress.percent}%` }}
              />
            </div>
          </div>
        )}

        {items.length === 0 ? (
          <div className="flex flex-col items-center justify-center gap-2 py-8 text-center">
            <Paperclip className="h-8 w-8 text-muted-foreground/40" />
            <p className="text-sm text-muted-foreground">暂无附件</p>
            <p className="text-xs text-muted-foreground">
              点击上方「上传附件」按钮添加文件
            </p>
          </div>
        ) : (
          <div className="flex flex-col gap-3">
            {items.map((attachment) => {
              const Icon = attachmentIcon(attachment.mime_type);
              return (
                <div
                  key={attachment.id}
                  className="flex items-center gap-3.5 rounded-xl border border-border/60 px-4 py-3 transition-all hover:border-primary/30 hover:shadow-md"
                >
                  <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-gradient-to-br from-primary/10 to-primary/5 border border-primary/20">
                    <Icon className="h-4 w-4 text-primary" />
                  </div>
                  <div className="min-w-0 flex-1">
                    <p className="truncate text-sm font-semibold">
                      {attachment.file_name}
                    </p>
                    <div className="mt-0.5 flex flex-wrap items-center gap-x-3 gap-y-0.5 text-xs text-muted-foreground">
                      <span className="font-medium">
                        {formatFileSize(attachment.file_size)}
                      </span>
                      <span>{attachment.uploader || "-"}</span>
                      <span>{formatDate(attachment.created_at)}</span>
                    </div>
                  </div>
                  <div className="flex shrink-0 items-center gap-1">
                    <Button
                      variant="ghost"
                      size="icon-xs"
                      aria-label={`下载 ${attachment.file_name}`}
                      render={
                        <a
                          href={attachmentApi.downloadUrl(attachment)}
                          download
                        />
                      }
                    >
                      <Download className="h-3.5 w-3.5" />
                    </Button>
                    <AlertDialog>
                      <AlertDialogTrigger
                        render={
                          <Button
                            variant="ghost"
                            size="icon-xs"
                            aria-label={`删除 ${attachment.file_name}`}
                          />
                        }
                      >
                        <Trash2 className="h-3.5 w-3.5 text-destructive" />
                      </AlertDialogTrigger>
                      <AlertDialogContent>
                        <AlertDialogHeader>
                          <AlertDialogTitle>确认删除附件?</AlertDialogTitle>
                          <AlertDialogDescription>
                            将删除 {attachment.file_name}，删除后不可恢复。
                          </AlertDialogDescription>
                        </AlertDialogHeader>
                        <AlertDialogFooter>
                          <AlertDialogCancel>取消</AlertDialogCancel>
                          <AlertDialogAction
                            onClick={() => void handleDelete(attachment)}
                          >
                            确认删除
                          </AlertDialogAction>
                        </AlertDialogFooter>
                      </AlertDialogContent>
                    </AlertDialog>
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </CardContent>
    </Card>
  );
}
