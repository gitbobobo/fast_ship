import {
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { Popover } from "@base-ui/react/popover";
import { ChevronDown, Search } from "lucide-react";
import { cn } from "@/lib/utils";

// 状态计数点：颜色与看板语义一致——未设置灰、待处理琥珀、开发中天蓝、已完成翠绿
const WORKFLOW_COUNT_SEGMENTS = [
  { key: "unset", label: "未设置", dot: "bg-slate-500" },
  { key: "todo", label: "待处理", dot: "bg-amber-600" },
  { key: "in_progress", label: "开发中", dot: "bg-sky-600" },
  { key: "done", label: "已完成", dot: "bg-emerald-600" },
] as const;

// 行徽标配色板：颜色按项目 id 稳定散列，同一项目每次打开颜色一致
const BADGE_STYLES = [
  "bg-rose-500",
  "bg-orange-500",
  "bg-amber-500",
  "bg-emerald-500",
  "bg-teal-500",
  "bg-sky-500",
  "bg-indigo-500",
  "bg-violet-500",
  "bg-pink-500",
] as const;

// 数字键直选只覆盖前 9 条可见行
const JUMP_LIMIT = 9;

const KBD_CLASS =
  "rounded border border-border bg-muted px-1 py-0.5 font-mono text-[10px] leading-none text-muted-foreground";

// 列表按开启状态 Issue 数排序：issue_workflow_counts 仅统计 state=open，
// 四段合计即开启总数；缺省按 0
const openIssueCount = (project: Project) =>
  WORKFLOW_COUNT_SEGMENTS.reduce(
    (sum, segment) => sum + (project.issue_workflow_counts?.[segment.key] ?? 0),
    0,
  );

// 徽标取色：简单字符串散列，不引入新依赖
function badgeStyle(id: string) {
  let hash = 0;
  for (const ch of id) hash = (hash * 31 + (ch.codePointAt(0) ?? 0)) | 0;
  return BADGE_STYLES[Math.abs(hash) % BADGE_STYLES.length];
}

// 徽标文字：多词名取前两个词的首字母，单词名取前两个字符（中英文都适用）
function projectInitials(name: string) {
  const words = name.trim().split(/\s+/).filter(Boolean);
  if (words.length === 0) return "?";
  const first = [...words[0]][0] ?? "";
  const second =
    words.length > 1 ? ([...words[1]][0] ?? "") : ([...words[0]][1] ?? "");
  return (first + second).toUpperCase() || "?";
}

// macOS 用 ⌘、其他平台用 Ctrl；happy-dom 的 navigator.platform/userAgent 也可用
function isMacPlatform() {
  return /mac|iphone|ipad|ipod/i.test(
    `${navigator.platform} ${navigator.userAgent}`,
  );
}

interface ProjectSwitcherProps {
  projects: Project[];
  value: string;
  onValueChange: (value: string) => void;
  placeholder?: string;
  disabled?: boolean;
  className?: string;
  [attribute: `data-${string}`]: string | number | boolean | undefined;
}

export function ProjectSwitcher({
  projects,
  value,
  onValueChange,
  placeholder = "请选择项目",
  disabled = false,
  className,
  ...triggerProps
}: ProjectSwitcherProps) {
  const [open, setOpen] = useState(false);
  const activeProject = projects.find((project) => project.id === value);

  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Trigger
        {...triggerProps}
        disabled={disabled}
        aria-label={`切换项目：${activeProject?.name ?? placeholder}`}
        className={cn(
          "flex h-8 w-fit items-center justify-between gap-1.5 rounded-lg border border-input bg-transparent py-2 pr-2 pl-2.5 text-sm whitespace-nowrap transition-colors outline-none select-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50 dark:bg-input/30 dark:hover:bg-input/50",
          className,
        )}
      >
        <span className="flex min-w-0 flex-1 items-center gap-1.5">
          {activeProject && (
            <span
              aria-hidden="true"
              className={cn(
                "flex size-4 shrink-0 items-center justify-center rounded-sm text-[9px] font-semibold leading-none text-white",
                badgeStyle(activeProject.id),
              )}
            >
              {projectInitials(activeProject.name)}
            </span>
          )}
          <span
            className={cn(
              "min-w-0 truncate text-left",
              !activeProject && "text-muted-foreground",
            )}
          >
            {activeProject?.name ?? placeholder}
          </span>
        </span>
        <ChevronDown
          aria-hidden="true"
          className="pointer-events-none size-4 shrink-0 text-muted-foreground"
        />
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Positioner align="start" sideOffset={4} className="isolate z-50">
          <Popover.Popup
            aria-label="切换项目"
            className="flex max-h-[min(20rem,var(--available-height,20rem))] w-80 max-w-(--available-width) origin-(--transform-origin) flex-col overflow-hidden rounded-lg bg-popover text-popover-foreground shadow-md ring-1 ring-foreground/10 duration-100 outline-none data-[side=bottom]:slide-in-from-top-2 data-[side=top]:slide-in-from-bottom-2 data-open:animate-in data-open:fade-in-0 data-open:zoom-in-95 data-closed:animate-out data-closed:fade-out-0 data-closed:zoom-out-95"
          >
            <ProjectSwitcherPanel
              projects={projects}
              value={value}
              onPick={(project) => {
                // 已选中项只关闭面板，不重复回调
                if (project.id !== value) onValueChange(project.id);
                setOpen(false);
              }}
              onRequestClose={() => setOpen(false)}
            />
          </Popover.Popup>
        </Popover.Positioner>
      </Popover.Portal>
    </Popover.Root>
  );
}

interface ProjectSwitcherPanelProps {
  projects: Project[];
  /** 当前选中的项目 id，用于行 aria-selected 与背景高亮。 */
  value: string;
  onPick: (project: Project) => void;
  onRequestClose: () => void;
}

/**
 * 弹出面板（T3 风格）：顶部搜索 + 竖向项目列表 + 底部键盘提示。
 * 键盘：↑/↓ 环绕移动高亮、Enter 选中高亮行、Esc 关闭、⌘N/Ctrl+N 直选
 * 前 9 条可见行。面板随弹层卸载，关闭即重置搜索词与高亮。
 */
function ProjectSwitcherPanel({
  projects,
  value,
  onPick,
  onRequestClose,
}: ProjectSwitcherPanelProps) {
  const [query, setQuery] = useState("");
  const [highlight, setHighlight] = useState(0);
  const searchRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);

  // 按开启状态 Issue 数降序展示；计数缺失按 0，同计数靠稳定排序保持传入顺序
  const visible = useMemo(() => {
    const search = query.trim().toLowerCase();
    return projects
      .filter((project) => {
        const fields = [project.name, project.description ?? ""];
        if (project.github_owner && project.github_repo) {
          fields.push(`${project.github_owner}/${project.github_repo}`);
        }
        return fields.some((field) => field.toLowerCase().includes(search));
      })
      .sort((a, b) => openIssueCount(b) - openIssueCount(a));
  }, [projects, query]);
  const focused = Math.min(highlight, Math.max(visible.length - 1, 0));

  const focusSearch = () => searchRef.current?.focus({ preventScroll: true });
  // 打开后焦点落在搜索框；rAF/timeout 兜底等 Popover 自身的焦点转移结束
  useLayoutEffect(() => {
    focusSearch();
    const frame = window.requestAnimationFrame(focusSearch);
    const timer = window.setTimeout(focusSearch, 0);
    return () => {
      window.cancelAnimationFrame(frame);
      window.clearTimeout(timer);
    };
  }, []);

  // 搜索词变化后高亮回到第一行
  useLayoutEffect(() => {
    setHighlight(0);
  }, [query]);

  // 高亮行滚进可视区
  useLayoutEffect(() => {
    listRef.current
      ?.querySelector('[data-highlighted="true"]')
      ?.scrollIntoView?.({ block: "nearest" });
  }, [focused]);

  // 键盘监听挂 window（捕获阶段）：覆盖搜索框/列表任一焦点位置；
  // Esc 直接 stopPropagation，避免连带关闭外层 Dialog
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      // 输入法组合态（keyCode 229 兼容旧内核）与长按 repeat 不响应
      if (
        event.defaultPrevented ||
        event.repeat ||
        event.isComposing ||
        event.keyCode === 229
      ) {
        return;
      }
      const onButton =
        event.target instanceof HTMLElement &&
        event.target.closest("button") !== null;
      if (
        (event.metaKey || event.ctrlKey) &&
        !event.altKey &&
        !event.shiftKey
      ) {
        if (/^[1-9]$/.test(event.key)) {
          // 识别到直选快捷键就先拦截：目标行越界时也要拦，否则浏览器接管
          // （Chromium 的 ⌘1..8 是切换标签页）
          event.preventDefault();
          event.stopPropagation();
          const project = visible[Number(event.key) - 1];
          if (project) onPick(project);
        }
        return;
      }
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
        // 按钮上的 Enter 交给按钮自身（触发其点击）
        if (onButton) return;
        const project = visible[focused];
        if (project) {
          event.preventDefault();
          onPick(project);
        }
      } else if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        onRequestClose();
      }
    };
    window.addEventListener("keydown", onKeyDown, true);
    return () => window.removeEventListener("keydown", onKeyDown, true);
  }, [visible, focused, onPick, onRequestClose]);

  return (
    <>
      <div className="flex shrink-0 items-center gap-2 border-b border-border/70 px-2.5">
        <Search
          aria-hidden="true"
          className="size-3.5 shrink-0 text-muted-foreground"
        />
        <input
          ref={searchRef}
          type="text"
          aria-label="搜索项目"
          placeholder="搜索项目名称、描述或仓库"
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
          const secondary =
            project.description?.trim() ||
            (project.github_owner && project.github_repo
              ? `${project.github_owner}/${project.github_repo}`
              : undefined);
          const counts = project.issue_workflow_counts;
          const jumpLabel =
            index < JUMP_LIMIT
              ? isMacPlatform()
                ? `⌘${index + 1}`
                : `Ctrl+${index + 1}`
              : null;
          return (
            <div
              key={project.id}
              role="option"
              aria-selected={project.id === value}
              data-highlighted={index === focused || undefined}
              onMouseDown={(event) => event.preventDefault()}
              onMouseEnter={() => setHighlight(index)}
              onClick={() => onPick(project)}
              className="flex cursor-pointer items-center gap-2.5 rounded-md px-2 py-1.5 select-none data-[highlighted]:bg-accent aria-selected:bg-accent"
            >
              <span
                aria-hidden="true"
                className={cn(
                  "flex size-8 shrink-0 items-center justify-center rounded-md text-xs font-semibold text-white",
                  badgeStyle(project.id),
                )}
              >
                {projectInitials(project.name)}
              </span>
              <span className="min-w-0 flex-1 text-left">
                <span className="block truncate text-xs font-medium leading-snug">
                  {project.name}
                </span>
                {secondary && (
                  <span
                    title={secondary}
                    className="mt-0.5 block truncate text-xs leading-snug text-muted-foreground"
                  >
                    {secondary}
                  </span>
                )}
              </span>
              <span className="flex shrink-0 items-center gap-2">
                {counts && (
                  <span className="flex items-center gap-2 text-xs text-muted-foreground">
                    {WORKFLOW_COUNT_SEGMENTS.map((segment) => {
                      const count = counts[segment.key];
                      return (
                        <span
                          key={segment.key}
                          title={`${segment.label} ${count}`}
                          aria-label={`${segment.label} ${count}`}
                          className={cn(
                            "inline-flex items-center gap-1",
                            count === 0 && "opacity-40",
                          )}
                        >
                          <span
                            aria-hidden="true"
                            className={cn("size-1.5 rounded-full", segment.dot)}
                          />
                          <span className="text-foreground/70 tabular-nums">
                            {count}
                          </span>
                        </span>
                      );
                    })}
                  </span>
                )}
                {jumpLabel && <kbd className={KBD_CLASS}>{jumpLabel}</kbd>}
              </span>
            </div>
          );
        })}
        {visible.length === 0 && (
          <p className="px-2 py-6 text-center text-xs text-muted-foreground">
            无匹配项目
          </p>
        )}
      </div>
      <div
        aria-hidden="true"
        className="flex shrink-0 items-center gap-3 border-t border-border/70 px-3 py-1.5 text-[10px] text-muted-foreground"
      >
        <span className="flex items-center gap-1">
          <kbd className={KBD_CLASS}>↑↓</kbd>移动
        </span>
        <span className="flex items-center gap-1">
          <kbd className={KBD_CLASS}>Enter</kbd>选中
        </span>
        <span className="flex items-center gap-1">
          <kbd className={KBD_CLASS}>Esc</kbd>关闭
        </span>
      </div>
    </>
  );
}
