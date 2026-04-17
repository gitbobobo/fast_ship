import { useMemo } from "react";
import MDEditor from "@uiw/react-md-editor";
import "@uiw/react-md-editor/markdown-editor.css";
import { useThemeStore } from "@/lib/store/theme-store";
import { cn } from "@/lib/utils";

interface MarkdownEditorProps {
  id?: string;
  value?: string;
  onChange?: (value: string) => void;
  placeholder?: string;
  rows?: number;
  className?: string;
}

export function MarkdownEditor({
  id,
  value = "",
  onChange,
  placeholder,
  rows = 14,
  className,
}: MarkdownEditorProps) {
  const { resolvedTheme } = useThemeStore();
  const height = useMemo(() => `${rows * 24 + 64}px`, [rows]);

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
        textareaProps={{
          id,
          placeholder,
        }}
      />
    </div>
  );
}
