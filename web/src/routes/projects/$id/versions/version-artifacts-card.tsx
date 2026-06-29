import { useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import { Upload, Trash2, Download, Package } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
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
import { artifactApi } from "@/lib/api/artifacts";
import { downloadAllArtifacts } from "@/lib/utils/download-artifacts";
import { formatFileSize, formatDate } from "@/lib/utils/format";
import { cn } from "@/lib/utils";
import { PlatformIcon } from "./version-shared";

interface UploadProgressState {
  currentFileName: string;
  currentFileIndex: number;
  totalFiles: number;
  completedFiles: number;
  failedFiles: number;
  percent: number;
  status: "uploading" | "completed" | "failed";
}

interface VersionArtifactsCardProps {
  artifacts: Artifact[];
  isEditable: boolean;
  isUploading: boolean;
  uploadProgress: UploadProgressState | null;
  isDragOver: boolean;
  uploadPlatform: string;
  setUploadPlatform: (v: string) => void;
  setIsDragOver: (v: boolean) => void;
  onUpload: (e: React.ChangeEvent<HTMLInputElement>) => void;
  onUploadFiles: (files: File[]) => Promise<void>;
  onDeleteArtifact: (id: string, name: string) => Promise<void>;
}

export function VersionArtifactsCard({
  artifacts,
  isEditable,
  isUploading,
  uploadProgress,
  isDragOver,
  uploadPlatform,
  setUploadPlatform,
  setIsDragOver,
  onUpload,
  onUploadFiles,
  onDeleteArtifact,
}: VersionArtifactsCardProps) {
  const fileInputRef = useRef<HTMLInputElement>(null);
  const downloadAbortRef = useRef<AbortController | null>(null);
  const [isDownloadingAll, setIsDownloadingAll] = useState(false);

  useEffect(() => {
    return () => {
      downloadAbortRef.current?.abort();
    };
  }, []);

  const handleDownloadAll = async () => {
    const urls = artifacts.map((artifact) => artifactApi.downloadUrl(artifact.id));

    downloadAbortRef.current?.abort();
    const controller = new AbortController();
    downloadAbortRef.current = controller;
    setIsDownloadingAll(true);

    toast.info(
      `正在依次下载全部 ${artifacts.length} 个安装包。若浏览器提示拦截多文件下载，请点击允许；被拦截时可再次点击补发。`,
    );

    try {
      await downloadAllArtifacts(urls, { signal: controller.signal });
    } finally {
      if (downloadAbortRef.current === controller) {
        downloadAbortRef.current = null;
        setIsDownloadingAll(false);
      }
    }
  };

  return (
    <Card className="shadow-md hover:shadow-lg transition-shadow">
      <CardHeader className="items-center gap-3 space-y-0 border-b px-5 pb-4 pt-5">
        <div className="flex min-w-0 flex-wrap items-center gap-2.5">
          <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-primary/10">
            <Package className="h-4 w-4 text-primary" />
          </div>
          <CardTitle className="text-base">安装包</CardTitle>
          {artifacts.length > 0 && (
            <Badge variant="secondary" className="text-xs font-semibold bg-primary/10 text-primary border-primary/20">
              {artifacts.length}
            </Badge>
          )}
        </div>
        {(isEditable || artifacts.length > 0) && (
          <CardAction>
            {artifacts.length > 0 && (
              <Button
                variant="outline"
                size="sm"
                className="shadow-sm"
                aria-label={`下载全部 ${artifacts.length} 个安装包`}
                disabled={isDownloadingAll || isUploading}
                onClick={() => void handleDownloadAll()}
              >
                <Download className="mr-1.5 h-3.5 w-3.5" />
                下载全部
              </Button>
            )}
            {isEditable && (
              <>
                <Button
                  variant="outline"
                  size="sm"
                  className="shadow-sm"
                  onClick={() => fileInputRef.current?.click()}
                  disabled={isUploading}
                >
                  <Upload className="mr-1.5 h-3.5 w-3.5" />
                  {isUploading ? "上传中..." : "上传文件"}
                </Button>
                <input
                  ref={fileInputRef}
                  type="file"
                  multiple
                  className="hidden"
                  onChange={onUpload}
                  disabled={isUploading}
                />
              </>
            )}
          </CardAction>
        )}
      </CardHeader>
      <CardContent className="space-y-5 px-5 pb-6 pt-5">
        {isEditable && (
          <div className="space-y-3">
            <div className="flex flex-col gap-1.5 sm:max-w-xs">
              <span className="text-sm font-medium text-muted-foreground">
                平台标识（可选）
              </span>
              <Input
                value={uploadPlatform}
                onChange={(e) => setUploadPlatform(e.target.value)}
                placeholder="如 android / ios / macos"
              />
              <p className="text-xs leading-relaxed text-muted-foreground">
                同名文件会按替换处理，并更新平台与大小信息
              </p>
            </div>
            <div
              role="button"
              tabIndex={0}
              className={cn(
                "rounded-lg border border-dashed px-5 py-7 text-center transition-colors",
                isUploading
                  ? "cursor-not-allowed border-muted-foreground/30 bg-muted/40"
                  : isDragOver
                    ? "border-primary bg-primary/5"
                    : "border-muted-foreground/30 hover:border-primary/50 hover:bg-muted/30",
              )}
              onClick={() => {
                if (isUploading) return;
                fileInputRef.current?.click();
              }}
              onKeyDown={(e) => {
                if (isUploading) return;
                if (e.key === "Enter" || e.key === " ") {
                  e.preventDefault();
                  fileInputRef.current?.click();
                }
              }}
              onDragEnter={(e) => {
                e.preventDefault();
                if (!isUploading) setIsDragOver(true);
              }}
              onDragOver={(e) => {
                e.preventDefault();
                if (!isUploading) setIsDragOver(true);
              }}
              onDragLeave={(e) => {
                e.preventDefault();
                const nextTarget = e.relatedTarget;
                if (!e.currentTarget.contains(nextTarget as Node | null)) {
                  setIsDragOver(false);
                }
              }}
              onDrop={(e) => {
                e.preventDefault();
                setIsDragOver(false);
                if (isUploading) return;
                const droppedFiles = Array.from(e.dataTransfer.files || []);
                void onUploadFiles(droppedFiles);
              }}
            >
              <Upload className="mx-auto mb-2.5 h-8 w-8 text-muted-foreground" />
              <p className="text-sm font-medium leading-snug">
                拖拽文件到这里，或点击选择安装包
              </p>
              <p className="mt-2 text-xs leading-relaxed text-muted-foreground">
                支持多文件上传，上传期间会禁止重复提交
              </p>
            </div>
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
                          ? `上传完成，共 ${uploadProgress.completedFiles} 个文件`
                          : `上传结束，成功 ${uploadProgress.completedFiles} 个，失败 ${uploadProgress.failedFiles} 个`}
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
          </div>
        )}

        {artifacts.length === 0 ? (
          <div className="flex flex-col items-center justify-center gap-2 py-8 text-center">
            <Package className="h-8 w-8 text-muted-foreground/40" />
            <p className="text-sm text-muted-foreground">暂无安装包</p>
            {isEditable && (
              <p className="text-xs text-muted-foreground">
                使用上方上传区域添加安装包
              </p>
            )}
          </div>
        ) : (
          <div className="flex flex-col gap-3">
            {artifacts.map((artifact) => (
              <div
                key={artifact.id}
                className="flex items-center gap-3.5 rounded-xl border border-border/60 px-4 py-3.5 transition-all hover:border-primary/30 hover:shadow-md"
              >
                <div className="flex h-12 w-12 shrink-0 items-center justify-center rounded-xl bg-gradient-to-br from-primary/10 to-primary/5 border-2 border-primary/20">
                  <PlatformIcon platform={artifact.platform} />
                </div>
                <div className="min-w-0 flex-1">
                  <p className="truncate text-sm font-semibold font-mono">
                    {artifact.file_name}
                  </p>
                  <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-0.5 text-xs text-muted-foreground">
                    <span className="font-medium">{formatFileSize(artifact.file_size)}</span>
                    {artifact.platform && (
                      <Badge variant="outline" className="text-[10px] font-semibold h-5 px-2 border-primary/30 bg-primary/5">
                        {artifact.platform}
                      </Badge>
                    )}
                    <span>{artifact.uploaded_by || "-"}</span>
                    <span>{formatDate(artifact.uploaded_at)}</span>
                  </div>
                </div>
                <div className="flex shrink-0 items-center gap-1">
                  <Button
                    variant="ghost"
                    size="icon-xs"
                    render={
                      <a
                        href={artifactApi.downloadUrl(artifact.id)}
                        download
                      />
                    }
                  >
                    <Download className="h-3.5 w-3.5" />
                  </Button>
                  {isEditable && (
                    <AlertDialog>
                      <AlertDialogTrigger
                        render={
                          <Button variant="ghost" size="icon-xs" />
                        }
                      >
                        <Trash2 className="h-3.5 w-3.5 text-destructive" />
                      </AlertDialogTrigger>
                      <AlertDialogContent>
                        <AlertDialogHeader>
                          <AlertDialogTitle>确认删除安装包?</AlertDialogTitle>
                          <AlertDialogDescription>
                            删除后不可恢复。
                          </AlertDialogDescription>
                        </AlertDialogHeader>
                        <AlertDialogFooter>
                          <AlertDialogCancel>取消</AlertDialogCancel>
                          <AlertDialogAction onClick={() => void onDeleteArtifact(artifact.id, artifact.file_name)}>
                            确认删除
                          </AlertDialogAction>
                        </AlertDialogFooter>
                      </AlertDialogContent>
                    </AlertDialog>
                  )}
                </div>
              </div>
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  );
}
