import { useEffect, useState } from "react";
import { AlertCircle, Loader2, X } from "lucide-react";
import type { ChatAttachment } from "@/lib/api/threads";
import type { ComposerImage } from "@/lib/hooks/use-image-uploads";
import { useAttachmentBlob } from "@/lib/hooks/use-threads";
import { cn } from "@/lib/utils";

const THUMB = "h-16 w-16 rounded-lg object-cover ring-1 ring-border";

/** Blob 转 object URL，卸载或换 Blob 时回收。 */
function useObjectUrl(blob?: Blob) {
  const [url, setUrl] = useState<string>();
  useEffect(() => {
    if (!blob || typeof URL.createObjectURL !== "function") return;
    const next = URL.createObjectURL(blob);
    setUrl(next);
    return () => {
      URL.revokeObjectURL(next);
      setUrl(undefined);
    };
  }, [blob]);
  return url;
}

/** 已发送的附件缩略图：带 JWT 经 Fast Ship 代理读取，不使用 T3 签名地址。 */
export function AttachmentThumb({
  environmentId,
  attachment,
}: {
  environmentId: string;
  attachment: ChatAttachment;
}) {
  const { data, isError } = useAttachmentBlob(environmentId, attachment.id);
  const url = useObjectUrl(data);
  if (url) {
    return <img src={url} alt={attachment.name} className={THUMB} />;
  }
  return (
    <div
      title={attachment.name}
      className={cn(
        THUMB,
        "flex items-center justify-center bg-muted text-muted-foreground",
      )}
    >
      {isError ? (
        <AlertCircle className="h-4 w-4" aria-label="图片加载失败" />
      ) : (
        <Loader2 className="h-4 w-4 animate-spin" aria-label="图片加载中" />
      )}
    </div>
  );
}

export function AttachmentThumbs({
  environmentId,
  attachments,
  className,
}: {
  environmentId: string;
  attachments?: ChatAttachment[];
  className?: string;
}) {
  if (!attachments?.length) return null;
  return (
    <div
      className={cn("flex flex-wrap gap-2", className)}
      data-testid="attachment-thumbs"
    >
      {attachments.map((a) => (
        <AttachmentThumb key={a.id} environmentId={environmentId} attachment={a} />
      ))}
    </div>
  );
}

/** 输入框里的待发送图片预览：上传中/失败状态 + 移除。 */
export function ImageStrip({
  environmentId,
  images,
  onRemove,
  locked = false,
}: {
  environmentId: string;
  images: ComposerImage[];
  onRemove: (key: string) => void;
  /** 请求还在飞时禁用移除，避免删掉已经写进命令体的附件。 */
  locked?: boolean;
}) {
  if (images.length === 0) return null;
  return (
    <ul className="flex flex-wrap gap-2" aria-label="待发送图片">
      {images.map((image) => (
        <li key={image.key} className="relative">
          {image.previewUrl ? (
            <img src={image.previewUrl} alt={image.name} className={THUMB} />
          ) : image.attachment ? (
            <AttachmentThumb
              environmentId={environmentId}
              attachment={image.attachment}
            />
          ) : (
            <div
              className={cn(
                THUMB,
                "flex items-center justify-center bg-muted text-xs text-muted-foreground",
              )}
            >
              {image.name}
            </div>
          )}
          {image.status === "uploading" && (
            <span
              role="status"
              className="absolute inset-0 flex items-center justify-center rounded-lg bg-background/60"
            >
              <Loader2 className="h-4 w-4 animate-spin" aria-label="上传中" />
            </span>
          )}
          {image.status === "error" && (
            <span
              role="alert"
              title={image.error}
              className="absolute inset-0 flex items-center justify-center rounded-lg bg-destructive/20 px-1 text-center text-[10px] text-destructive"
            >
              {image.error || "上传失败"}
            </span>
          )}
          <button
            type="button"
            aria-label={`移除图片 ${image.name}`}
            disabled={locked}
            onClick={() => onRemove(image.key)}
            className="absolute -top-1.5 -right-1.5 flex h-5 w-5 items-center justify-center rounded-full bg-foreground text-background shadow disabled:pointer-events-none disabled:opacity-50"
          >
            <X className="h-3 w-3" />
          </button>
        </li>
      ))}
    </ul>
  );
}
