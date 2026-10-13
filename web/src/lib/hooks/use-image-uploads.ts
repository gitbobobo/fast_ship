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

function attachmentsFrom(images: ComposerImage[], tag?: string) {
  return images.flatMap((i) =>
    i.status === "done" && i.attachment && i.tag === tag ? [i.attachment] : [],
  );
}

/**
 * 输入框里的图片：选中即上传（原始字节），上传完成才可随消息发送。
 * 未发送就移除、或组件卸载（含换环境）时，尽力删除上游还没被消息引用的附件。
 * 正在飞行的请求按附件 id 记下快照：卸载不删，失败且会话已结束再补删。
 */
export function useImageUploads(
  environmentId: string,
  existing: ChatAttachment[] = [],
) {
  const [images, setImages] = useState<ComposerImage[]>(() =>
    existing.map(asExisting),
  );
  const ref = useRef<ComposerImage[]>(images);
  const inflightRef = useRef(new Set<string>());
  const sessionRef = useRef(0);

  const commit = useCallback((next: ComposerImage[]) => {
    ref.current = next;
    setImages(next);
  }, []);

  const discard = useCallback(
    (image: ComposerImage) => {
      dispose(image);
      if (
        image.attachment &&
        !image.existing &&
        !inflightRef.current.has(image.attachment.id)
      ) {
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
              if (!inflightRef.current.has(attachment.id)) {
                void threadApi
                  .deleteAttachment(environmentId, attachment.id)
                  .catch(() => undefined);
              }
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

  /**
   * 把这次请求快照里的附件交给这次 RPC：飞行中卸载不删；成功只丢本地引用；
   * 失败且输入会话已结束则补删非 existing 的 id。
   */
  const submit = useCallback(
    async <T,>(run: () => Promise<T>, sent?: ChatAttachment[]): Promise<T> => {
      const batch = sent ?? attachmentsFrom(ref.current);
      const ids = new Set(batch.map((a) => a.id));
      const existingIds = new Set(
        ref.current.flatMap((i) =>
          i.existing && i.attachment ? [i.attachment.id] : [],
        ),
      );
      const session = sessionRef.current;
      for (const id of ids) inflightRef.current.add(id);
      try {
        const result = await run();
        for (const id of ids) inflightRef.current.delete(id);
        if (sessionRef.current === session) {
          const next: ComposerImage[] = [];
          for (const image of ref.current) {
            if (image.attachment && ids.has(image.attachment.id)) {
              dispose(image);
            } else {
              next.push(image);
            }
          }
          commit(next);
        }
        return result;
      } catch (err) {
        for (const id of ids) inflightRef.current.delete(id);
        if (sessionRef.current !== session) {
          for (const id of ids) {
            if (!existingIds.has(id)) {
              void threadApi
                .deleteAttachment(environmentId, id)
                .catch(() => undefined);
            }
          }
        }
        throw err;
      }
    },
    [commit, environmentId],
  );

  useEffect(() => {
    const session = ++sessionRef.current;
    return () => {
      for (const image of ref.current) discard(image);
      ref.current = [];
      if (sessionRef.current === session) sessionRef.current += 1;
    };
  }, [discard]);

  const uploading = images.some((i) => i.status === "uploading");
  const attachmentsOf = (tag?: string) => attachmentsFrom(images, tag);

  return {
    images,
    add,
    remove,
    clear,
    submit,
    uploading,
    attachmentsOf,
  };
}

export type ImageUploads = ReturnType<typeof useImageUploads>;
