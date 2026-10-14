import { useState } from "react";
import { ChevronRight, Wrench } from "lucide-react";
import type { ThreadItem } from "@/lib/api/threads";
import { summarizeGroup } from "@/lib/threads-timeline";
import { cn } from "@/lib/utils";
import { COLLAPSED_META, CollapsedItem } from "./thread-item-view";

/**
 * 连续的工具调用/推理合并成的一行摘要（T3 风格：chevron + 图标 + 文案），
 * 展开后列出组内每条，每条仍可再展开读正文。
 */
export function ThreadToolGroup({
  items,
  live,
  environmentId,
  threadId,
  fallbackRevision,
}: {
  items: ThreadItem[];
  /** 线程仍在运行：组内进行中的条目才会以进行式文案呈现。 */
  live: boolean;
  environmentId: string;
  threadId: string;
  /** 条目自身没有 updated_at 时用线程更新时间作为版本。 */
  fallbackRevision: string;
}) {
  const [open, setOpen] = useState(false);
  const { text, active } = summarizeGroup(items, live);
  const types = new Set(items.map((i) => i.type));
  const Icon =
    (types.size === 1 && COLLAPSED_META[items[0].type]?.icon) || Wrench;

  return (
    <div data-testid="thread-tool-group" className="min-w-0 text-sm">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        className="group flex max-w-full items-center gap-2 rounded-md py-1 text-muted-foreground transition-colors select-none hover:text-foreground focus-visible:outline-hidden focus-visible:ring-2 focus-visible:ring-ring"
      >
        <ChevronRight
          aria-hidden
          className={cn("size-3.5 shrink-0 transition-transform", open && "rotate-90")}
        />
        <Icon aria-hidden className="size-4 shrink-0" />
        <span
          className={cn(
            "min-w-0 truncate",
            active && "animate-pulse motion-reduce:animate-none",
          )}
        >
          {text}
        </span>
      </button>
      {open && (
        <div className="mt-1 ml-2 space-y-1.5 border-l border-border/60 pl-3">
          {items.map((item) => (
            <CollapsedItem
              key={item.id}
              item={item}
              environmentId={environmentId}
              threadId={threadId}
              revision={item.updated_at || fallbackRevision}
            />
          ))}
        </div>
      )}
    </div>
  );
}
