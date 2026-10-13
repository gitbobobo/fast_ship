import { useCallback, useEffect, useRef, useState } from "react";
import { threadApi, type ChatAttachment } from "@/lib/api/threads";

export const IMAGE_MIME_TYPES = [
  "image/png",
  "image/jpeg",
  "image/gif",
  "image/webp",
];
export const IMAGE_MAX_BYTES = 10 * 1024 * 1024;

export interface ComposerImage {
  key: string;
  name: string;
  status: "uploading" | "done" | "error";
  /** 本地文件的预览地址；已存在的附件没有，改走代理读取。 */
  previewUrl?: string;
  attachment?: ChatAttachment;
  /** 已被消息引用的附件：移除时只从本次编辑里去掉，不删除上游文件。 */
  existing?: boolean;
  error?: string;
  /** 用于把图片归到某个提问（每题各自附图）。 */
  tag?: string;
}

let counter = 0;
const nextKey = () => `img-${Date.now()}-${counter++}`;

function validate(file: File) {
  if (!IMAGE_MIME_TYPES.includes(file.type)) return "仅支持 PNG/JPEG/GIF/WebP 图片";
  if (file.size > IMAGE_MAX_BYTES) return "图片超过 10MiB 上限";
  return "";
}

function preview(file: File) {
  return typeof URL.createObjectURL === "function"
    ? URL.createObjectURL(file)
    : undefined;
}

function dispose(image: ComposerImage) {
  if (image.previewUrl) URL.revokeObjectURL?.(image.previewUrl);
}

function asExisting(attachment: ChatAttachment): ComposerImage {
  return {
    key: nextKey(),
    name: attachment.name,
    status: "done",
    attachment,
    existing: true,
  };
}

/**
 * 输入框里的图片：选中即上传（原始字节），上传完成才可随消息发送。
 * 未发送就移除、或组件卸载（含换环境）时，尽力删除上游还没被消息引用的附件。
 * 用 key 重挂载组件来丢弃一个环境下的所有上传引用。
 */
export function useImageUploads(
  environmentId: string,
  existing: ChatAttachment[] = [],
) {
  const [images, setImages] = useState<ComposerImage[]>(() =>
    existing.map(asExisting),
  );
  const ref = useRef<ComposerImage[]>(images);

  const commit = useCallback((next: ComposerImage[]) => {
    ref.current = next;
    setImages(next);
  }, []);

  const discard = useCallback(
    (image: ComposerImage) => {
      dispose(image);
      if (image.attachment && !image.existing) {
        void threadApi
          .deleteAttachment(environmentId, image.attachment.id)
          .catch(() => undefined);
      }
    },
    [environmentId],
  );

  const add = useCallback(
    (files: File[], tag?: string) => {
      const added = files.map((file): [File, ComposerImage] => {
        const error = validate(file);
        return [
          file,
          {
            key: nextKey(),
            name: file.name || "image",
            status: error ? "error" : "uploading",
            previewUrl: preview(file),
            error: error || undefined,
            tag,
          },
        ];
      });
      commit([...ref.current, ...added.map(([, image]) => image)]);
      for (const [file, image] of added) {
        if (image.status === "error") continue;
        threadApi.uploadAttachment(environmentId, file).then(
          (attachment) => {
            if (!ref.current.some((i) => i.key === image.key)) {
              void threadApi
                .deleteAttachment(environmentId, attachment.id)
                .catch(() => undefined);
              return;
            }
            commit(
              ref.current.map((i) =>
                i.key === image.key ? { ...i, status: "done", attachment } : i,
              ),
            );
          },
          (err: unknown) =>
            commit(
              ref.current.map((i) =>
                i.key === image.key
                  ? {
                      ...i,
                      status: "error",
                      error: err instanceof Error ? err.message : "上传失败",
                    }
                  : i,
              ),
            ),
        );
      }
    },
    [environmentId, commit],
  );

  const remove = useCallback(
    (key: string) => {
      const target = ref.current.find((i) => i.key === key);
      if (!target) return;
      commit(ref.current.filter((i) => i.key !== key));
      discard(target);
    },
    [commit, discard],
  );

  /** 消息已发出：附件已被引用，只丢本地引用，不删上游文件。 */
  const clear = useCallback(() => {
    for (const image of ref.current) dispose(image);
    commit([]);
  }, [commit]);

  useEffect(
    () => () => {
      for (const image of ref.current) discard(image);
      ref.current = [];
    },
    [discard],
  );

  const uploading = images.some((i) => i.status === "uploading");
  const attachmentsOf = (tag?: string) =>
    images.flatMap((i) =>
      i.status === "done" && i.attachment && i.tag === tag ? [i.attachment] : [],
    );

  return {
    images,
    add,
    remove,
    clear,
    uploading,
    attachmentsOf,
  };
}

export type ImageUploads = ReturnType<typeof useImageUploads>;
