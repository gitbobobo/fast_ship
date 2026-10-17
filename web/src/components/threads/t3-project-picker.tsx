import {
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { ChevronDown, Folder, Search } from "lucide-react";
import type { ComposerProject } from "@/lib/api/threads";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { cn } from "@/lib/utils";
import { CHIP } from "./thread-chips";

/** 徽标配色：按项目 id 稳定散列取色，同一项目在任何入口颜色一致。 */
const BADGE_COLORS = [
  "bg-red-500/15 text-red-700 dark:text-red-300",
  "bg-orange-500/15 text-orange-700 dark:text-orange-300",
  "bg-amber-500/15 text-amber-700 dark:text-amber-300",
  "bg-emerald-500/15 text-emerald-700 dark:text-emerald-300",
  "bg-sky-500/15 text-sky-700 dark:text-sky-300",
  "bg-blue-500/15 text-blue-700 dark:text-blue-300",
  "bg-violet-500/15 text-violet-700 dark:text-violet-300",
  "bg-pink-500/15 text-pink-700 dark:text-pink-300",
];

function hashProjectId(id: string): number {
  let hash = 0;
  for (let i = 0; i < id.length; i++) {
    hash = (hash * 31 + id.charCodeAt(i)) | 0;
  }
  return Math.abs(hash);
}

function badgeColor(id: string): string {
  return BADGE_COLORS[hashProjectId(id) % BADGE_COLORS.length];
}

/** 徽标字符：多词名取前两个词的首字母，单词名取前两个字符。 */
function projectInitials(title: string): string {
  const words = title.trim().split(/\s+/).filter(Boolean);
  const chars =
    words.length >= 2
      ? [Array.from(words[0])[0], Array.from(words[1])[0]]
      : Array.from(title.trim()).slice(0, 2);
  return chars.join("").toUpperCase();
}

/**
 * 超长路径省略中间段，保留开头与结尾（结尾是项目目录，信息量最大）。
 * 例：/Users/me/work/projects/fast_ship → /Users/…/projects/fast_ship。
 */
export function shortenPath(path: string, maxLength = 30): string {
  if (path.length <= maxLength) return path;
  const segments = path.split("/").filter(Boolean);
  if (segments.length < 3) return path;
  const head = segments[0];
  const tail = segments[segments.length - 1];
  const candidates = [
    `/${head}/…/${segments.slice(-2).join("/")}`,
    `/${head}/…/${tail}`,
    `…/${tail}`,
  ];
  return (
    candidates.find((candidate) => candidate.length <= maxLength) ??
    candidates[candidates.length - 1]
  );
}

export interface T3ProjectPickerProps {
  projects: ComposerProject[];
  /** 当前项目 id；不在 projects 里时触发器回退到 triggerLabel/placeholder。 */
  value: string;
  /** 仅在选中项变化时回调；点当前项只关闭面板。 */
  onValueChange: (value: string) => void;
  disabled?: boolean;
  /** 触发器 aria-label，默认 "项目"。 */
  ariaLabel?: string;
  /** 覆盖触发器显示文本（如「加载中…」「xx（已失效）」）。 */
  triggerLabel?: ReactNode;
  /** 无选中值时的占位文本，默认「选择项目」。 */
  placeholder?: string;
  className?: string;
}

/**
 * T3 项目选择器：对齐 T3 Code 新建线程的项目选择交互——chip 触发器 +
 * Portal 面板（搜索 + 单列项目列表 + ↑↓/Enter/Esc 键盘导航，无数字快捷键）。
 * Fast Ship 里所有「选 T3 Code 项目」的入口统一走这个组件。
 */
export function T3ProjectPicker({
  projects,
  value,
  onValueChange,
  disabled,
  ariaLabel,
  triggerLabel,
  placeholder = "选择项目",
  className,
}: T3ProjectPickerProps) {
  const [open, setOpen] = useState(false);
  const current = projects.find((p) => p.id === value);
  const label = triggerLabel ?? current?.title ?? placeholder;
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        className={cn(CHIP, className)}
        aria-label={ariaLabel ?? "项目"}
        disabled={disabled}
      >
        {current ? (
          <ProjectBadge project={current} className="size-4 rounded text-[9px]" />
        ) : (
          <Folder className="size-3.5 shrink-0 text-muted-foreground" />
        )}
        <span className="truncate">{label}</span>
        <ChevronDown className="size-3 shrink-0 text-muted-foreground" />
      </PopoverTrigger>
      <PopoverContent className="w-auto overflow-hidden p-0">
        <T3ProjectPickerPanel
          projects={projects}
          value={value}
          onSelect={(project) => {
            if (project.id !== value) onValueChange(project.id);
            setOpen(false);
          }}
          onRequestClose={() => setOpen(false)}
        />
      </PopoverContent>
    </Popover>
  );
}

/** 彩色首字母徽标：触发器与列表行共用，尺寸由调用方给。 */
function ProjectBadge({
  project,
  className,
}: {
  project: ComposerProject;
  className?: string;
}) {
  return (
    <span
      aria-hidden
      className={cn(
        "flex shrink-0 select-none items-center justify-center font-semibold",
        badgeColor(project.id),
        className,
      )}
    >
      {projectInitials(project.title)}
    </span>
  );
}

interface T3ProjectPickerPanelProps {
  projects: ComposerProject[];
  value: string;
  onSelect: (project: ComposerProject) => void;
  onRequestClose: () => void;
}

/**
 * 弹出面板：搜索框 + 单列项目列表 + 底部键盘提示。
 * 键盘监听挂 window 捕获阶段（覆盖搜索框/列表任一焦点位置）：
 * ↑/↓ 环绕移动高亮、Enter 选中、Esc 关闭并 stopPropagation（不连累外层
 * Dialog）。不做数字快捷键直选。
 */
function T3ProjectPickerPanel({
  projects,
  value,
  onSelect,
  onRequestClose,
}: T3ProjectPickerPanelProps) {
  const [query, setQuery] = useState("");
  const [highlight, setHighlight] = useState(0);
  const searchRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);

  const visible = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return projects;
    return projects.filter((p) => p.title.toLowerCase().includes(q));
  }, [projects, query]);
  const focused = Math.min(highlight, Math.max(visible.length - 1, 0));

  const focusSearch = () => searchRef.current?.focus({ preventScroll: true });
  // 打开后焦点落在搜索框；rAF/timeout 兜底等 Popover 自身的焦点转移结束。
  useLayoutEffect(() => {
    focusSearch();
    const frame = window.requestAnimationFrame(focusSearch);
    const timer = window.setTimeout(focusSearch, 0);
    return () => {
      window.cancelAnimationFrame(frame);
      window.clearTimeout(timer);
    };
  }, []);

  // 搜索词变化后高亮回到第一行。
  useLayoutEffect(() => {
    setHighlight(0);
  }, [query]);

  // 高亮行滚进可视区。
  useLayoutEffect(() => {
    listRef.current
      ?.querySelector('[data-highlighted="true"]')
      ?.scrollIntoView?.({ block: "nearest" });
  }, [focused]);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.repeat) return;
      // IME 组合中（中日韩输入未确认）的 Enter/方向键不当作选择操作。
      if (event.isComposing || event.keyCode === 229) return;
      if (event.metaKey || event.ctrlKey || event.altKey) return;
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        if (visible.length === 0) return;
        event.preventDefault();
        const delta = event.key === "ArrowDown" ? 1 : -1;
        setHighlight(
          (h) =>
            (Math.min(h, visible.length - 1) + delta + visible.length) %
            visible.length,
        );
      } else if (event.key === "Enter") {
        const item = visible[focused];
        if (item) {
          event.preventDefault();
          onSelect(item);
        }
      } else if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        onRequestClose();
      }
    };
    window.addEventListener("keydown", onKeyDown, true);
    return () => window.removeEventListener("keydown", onKeyDown, true);
  }, [visible, focused, onSelect, onRequestClose]);

  return (
    <div
      className="flex max-h-80 w-80 flex-col overflow-hidden"
      aria-label="选择项目"
      data-slot="t3-project-picker-panel"
    >
      <div className="flex items-center gap-2 border-b border-border/70 px-2.5">
        <Search className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
        <input
          ref={searchRef}
          type="text"
          aria-label="搜索项目"
          placeholder="搜索项目…"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          className="h-8 w-full bg-transparent text-xs outline-none placeholder:text-muted-foreground"
        />
      </div>
      <div
        ref={listRef}
        role="listbox"
        aria-label="项目列表"
        className="min-h-0 flex-1 overflow-y-auto overscroll-contain p-1"
      >
        {visible.map((project, index) => {
          const selected = project.id === value;
          return (
            <div
              key={project.id}
              role="option"
              aria-selected={selected}
              data-highlighted={index === focused || undefined}
              className={cn(
                "flex cursor-pointer items-center gap-2.5 rounded-md px-2 py-1.5 select-none",
                index === focused
                  ? "bg-accent"
                  : selected && "bg-accent/50",
              )}
              onMouseDown={(event) => event.preventDefault()}
              onMouseEnter={() => setHighlight(index)}
              onClick={() => onSelect(project)}
            >
              <ProjectBadge
                project={project}
                className="size-7 rounded-md text-[11px]"
              />
              <div className="min-w-0 flex-1 text-left">
                <div className="truncate text-xs font-medium leading-snug">
                  {project.title}
                </div>
                {project.workspace_root && (
                  <div
                    title={project.workspace_root}
                    className="mt-0.5 truncate text-xs leading-snug text-muted-foreground"
                  >
                    Local · {shortenPath(project.workspace_root)}
                  </div>
                )}
              </div>
            </div>
          );
        })}
        {visible.length === 0 && (
          <p className="px-2 py-6 text-center text-xs text-muted-foreground">
            无匹配项目
          </p>
        )}
      </div>
      <div className="border-t border-border/70 px-2.5 py-1.5 text-[10px] leading-none text-muted-foreground">
        ↑↓ 移动 · Enter 选择 · Esc 关闭
      </div>
    </div>
  );
}
