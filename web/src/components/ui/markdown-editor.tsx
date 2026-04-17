import { type ClipboardEvent, useMemo, useState } from "react";
import MDEditor from "@uiw/react-md-editor";
import "@uiw/react-md-editor/markdown-editor.css";
import { useThemeStore } from "@/lib/store/theme-store";
import { useAuthStore } from "@/lib/store/auth-store";
import { toProtectedMediaUrl } from "@/lib/utils/github-media-proxy";
import { cn } from "@/lib/utils";

interface MarkdownEditorProps {
  id?: string;
  value?: string;
  onChange?: (value: string) => void;
  onPasteImage?: (file: File) => Promise<string>;
  placeholder?: string;
  rows?: number;
  className?: string;
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
  const [isUploadingImage, setIsUploadingImage] = useState(false);

  const handlePaste = async (event: ClipboardEvent<HTMLTextAreaElement>) => {
    if (!onPasteImage) {
      return;
    }

    const files = Array.from(event.clipboardData?.items ?? [])
      .filter((item) => item.type.startsWith("image/"))
      .map((item) => item.getAsFile())
      .filter((file): file is File => file instanceof File);
    if (files.length === 0) {
      return;
    }

    event.preventDefault();
    const target = event.currentTarget;
    let nextValue = value;
    let selectionStart = target.selectionStart ?? nextValue.length;
    let selectionEnd = target.selectionEnd ?? selectionStart;

    setIsUploadingImage(true);
    try {
      for (const file of files) {
        const markdown = await onPasteImage(file);
        const insertion = `${markdown}\n`;
        nextValue = `${nextValue.slice(0, selectionStart)}${insertion}${nextValue.slice(selectionEnd)}`;
        selectionStart += insertion.length;
        selectionEnd = selectionStart;
        onChange?.(nextValue);
      }

      requestAnimationFrame(() => {
        target.focus();
        target.setSelectionRange(selectionStart, selectionStart);
      });
    } catch {
      return;
    } finally {
      setIsUploadingImage(false);
    }
  };

  return (
    <div
      className={cn("wmde-markdown-var", className)}
      data-color-mode={resolvedTheme}
    >
      <MDEditor
        value={value}
        onChange={(val) => onChange?.(val ?? "")}
        height={height}
        preview="live"
        hideToolbar={false}
        className="w-full"
        previewOptions={{
          components: {
            img: ({ src, ...props }) => (
              <img {...props} src={toProtectedMediaUrl(typeof src === "string" ? src : undefined, token)} />
            ),
          },
        }}
        textareaProps={{
          id,
          placeholder,
          onPaste: (event) => {
            void handlePaste(event);
          },
        }}
      />
      {isUploadingImage ? (
        <p className="mt-2 text-xs text-muted-foreground">正在上传图片并插入 Markdown...</p>
      ) : null}
    </div>
  );
}
