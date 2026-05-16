import { useCallback, useEffect, useEffectEvent, useMemo, useRef, useState, type ReactElement } from "react";
import MDEditor, { commands, type ICommand } from "@uiw/react-md-editor";
import "@uiw/react-md-editor/markdown-editor.css";
import { ImageIcon, Loader2 } from "lucide-react";
import { useThemeStore } from "@/lib/store/theme-store";
import { useAuthStore } from "@/lib/store/auth-store";
import { toProtectedMediaUrl } from "@/lib/utils/github-media-proxy";
import { cn } from "@/lib/utils";

async function readClipboardImageFiles() {
  if (typeof navigator === "undefined" || typeof navigator.clipboard?.read !== "function") {
    return [];
  }

  try {
    const items = await navigator.clipboard.read();
    const files: File[] = [];

    for (const item of items) {
      const imageType = item.types.find((type) => type.startsWith("image/"));
      if (!imageType) {
        continue;
      }

      const blob = await item.getType(imageType);
      const extension = imageType.split("/")[1] || "png";
      files.push(new File([blob], `pasted-image.${extension}`, { type: imageType }));
    }

    return files;
  } catch {
    return [];
  }
}

interface MarkdownEditorProps {
  id?: string;
  value?: string;
  onChange?: (value: string) => void;
  onPasteImage?: (file: File) => Promise<string>;
  placeholder?: string;
  rows?: number;
  className?: string;
}

function findTextarea(root: HTMLElement | null, target?: EventTarget | null): HTMLTextAreaElement | null {
  if (target instanceof HTMLTextAreaElement) {
    return target;
  }
  if (!root) return null;
  const activeElement = root.ownerDocument.activeElement;
  if (activeElement instanceof HTMLTextAreaElement && root.contains(activeElement)) {
    return activeElement;
  }
  return root.querySelector("textarea");
}

function useImageUploadCommand(
  rootRef: React.RefObject<HTMLDivElement | null>,
  valueRef: React.MutableRefObject<string>,
  onChangeRef: React.MutableRefObject<((value: string) => void) | undefined>,
  onPasteImageRef: React.MutableRefObject<((file: File) => Promise<string>) | undefined>,
  setUploadingImageCount: React.Dispatch<React.SetStateAction<number>>,
) {
  const [uploading, setUploading] = useState(false);

  const uploadFiles = useCallback(async (files: File[]) => {
    if (!onPasteImageRef.current || files.length === 0) return;

    const textarea = findTextarea(rootRef.current);
    if (!textarea) return;

    let nextValue = valueRef.current;
    let selectionStart = textarea.selectionStart ?? nextValue.length;
    let selectionEnd = textarea.selectionEnd ?? selectionStart;

    setUploadingImageCount((c) => c + 1);
    setUploading(true);
    try {
      for (const file of files) {
        const markdown = await onPasteImageRef.current(file);
        const insertion = `${markdown}\n`;
        nextValue = `${nextValue.slice(0, selectionStart)}${insertion}${nextValue.slice(selectionEnd)}`;
        selectionStart += insertion.length;
        selectionEnd = selectionStart;
        valueRef.current = nextValue;
        onChangeRef.current?.(nextValue);
      }
      requestAnimationFrame(() => {
        const ta = findTextarea(rootRef.current) ?? textarea;
        ta.focus();
        ta.setSelectionRange(selectionStart, selectionStart);
      });
    } catch {
      return;
    } finally {
      setUploadingImageCount((c) => Math.max(0, c - 1));
      setUploading(false);
    }
  }, [rootRef, onPasteImageRef, setUploadingImageCount, valueRef, onChangeRef]);

  const command = useMemo<ICommand>(() => ({
    name: "upload-image",
    keyCommand: "upload-image",
    buttonProps: { "aria-label": "上传图片", title: "上传图片" },
    icon: (uploading ? <Loader2 className="h-[14px] w-[14px] animate-spin" /> : <ImageIcon className="h-[14px] w-[14px]" />) as ReactElement,
    execute: () => {
      const input = document.createElement("input");
      input.type = "file";
      input.accept = "image/*";
      input.multiple = true;
      input.style.display = "none";
      input.onchange = () => {
        const files = Array.from(input.files ?? []).filter((f) => f.type.startsWith("image/"));
        void uploadFiles(files);
        input.remove();
      };
      const cleanup = () => { if (input.parentNode) input.remove(); };
      input.addEventListener("cancel", cleanup);
      document.body.appendChild(input);
      input.click();
      setTimeout(cleanup, 300_000);
    },
  }), [uploading, uploadFiles]);

  return command;
}

export function MarkdownEditor({
  id,
  value = "",
  onChange,
  onPasteImage,
  placeholder,
  rows = 14,
  className,
}: MarkdownEditorProps) {
  const { resolvedTheme } = useThemeStore();
  const token = useAuthStore((state) => state.token);
  const height = useMemo(() => `${rows * 24 + 64}px`, [rows]);
  const rootRef = useRef<HTMLDivElement>(null);
  const valueRef = useRef(value);
  const onChangeRef = useRef(onChange);
  const onPasteImageRef = useRef(onPasteImage);
  const [uploadingImageCount, setUploadingImageCount] = useState(0);

  valueRef.current = value;
  onChangeRef.current = onChange;
  onPasteImageRef.current = onPasteImage;

  const uploadImageCommand = useImageUploadCommand(
    rootRef,
    valueRef,
    onChangeRef,
    onPasteImageRef,
    setUploadingImageCount,
  );

  const editorCommands = useMemo(() => {
    const defaultCommands = commands.getCommands();
    const linkIdx = defaultCommands.findIndex((c) => c.name === "link");
    const insertAt = linkIdx >= 0 ? linkIdx + 1 : defaultCommands.length;
    const result = [...defaultCommands];
    result.splice(insertAt, 0, uploadImageCommand);
    return result;
  }, [uploadImageCommand]);

  const handlePaste = useEffectEvent(async (event: ClipboardEvent | globalThis.ClipboardEvent) => {
    if (!onPasteImageRef.current) {
      return;
    }

    const itemFiles = Array.from(event.clipboardData?.items ?? [])
      .filter((item) => item.type.startsWith("image/"))
      .map((item) => item.getAsFile())
      .filter((file): file is File => file instanceof File);
    const directFiles = Array.from(event.clipboardData?.files ?? []).filter((file) =>
      file.type.startsWith("image/"),
    );
    const files = itemFiles.length > 0
      ? itemFiles
      : directFiles.length > 0
        ? directFiles
        : await readClipboardImageFiles();
    if (files.length === 0) {
      return;
    }

    const target = findTextarea(rootRef.current, event.target);
    if (!target) {
      return;
    }

    event.preventDefault();
    let nextValue = valueRef.current;
    let selectionStart = target.selectionStart ?? nextValue.length;
    let selectionEnd = target.selectionEnd ?? selectionStart;

    setUploadingImageCount((count) => count + 1);
    try {
      for (const file of files) {
        const markdown = await onPasteImageRef.current(file);
        const insertion = `${markdown}\n`;
        nextValue = `${nextValue.slice(0, selectionStart)}${insertion}${nextValue.slice(selectionEnd)}`;
        selectionStart += insertion.length;
        selectionEnd = selectionStart;
        valueRef.current = nextValue;
        onChangeRef.current?.(nextValue);
      }

      requestAnimationFrame(() => {
        const textarea = findTextarea(rootRef.current, target) ?? target;
        textarea.focus();
        textarea.setSelectionRange(selectionStart, selectionStart);
      });
    } catch {
      return;
    } finally {
      setUploadingImageCount((count) => Math.max(0, count - 1));
    }
  });

  useEffect(() => {
    const handleDocumentPaste = (event: globalThis.ClipboardEvent) => {
      const root = rootRef.current;
      if (!root) {
        return;
      }

      const activeElement = document.activeElement;
      if (!(activeElement instanceof HTMLElement) || !root.contains(activeElement)) {
        return;
      }

      void handlePaste(event);
    };

    document.addEventListener("paste", handleDocumentPaste, true);
    return () => {
      document.removeEventListener("paste", handleDocumentPaste, true);
    };
  }, []);

  return (
    <div
      ref={rootRef}
      className={cn("wmde-markdown-var", className)}
      data-color-mode={resolvedTheme}
    >
      <MDEditor
        value={value}
        onChange={(val) => onChange?.(val ?? "")}
        height={height}
        preview="live"
        commands={editorCommands}
        className="w-full"
        previewOptions={{
          components: {
            a: ({ href, children, ...props }) => <a {...props} href={href} target="_blank" rel="noopener noreferrer">{children}</a>,
            img: ({ src, ...props }) => (
              <img {...props} src={toProtectedMediaUrl(typeof src === "string" ? src : undefined, token)} />
            ),
          },
        }}
        textareaProps={{
          id,
          placeholder,
        }}
      />
      {uploadingImageCount > 0 ? (
        <p className="mt-2 text-xs text-muted-foreground">正在上传图片并插入 Markdown...</p>
      ) : null}
    </div>
  );
}
