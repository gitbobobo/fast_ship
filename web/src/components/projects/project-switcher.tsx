import { useRef, useState } from "react";
import { Popover } from "@base-ui/react/popover";
import { ChevronDown } from "lucide-react";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { shelvedStatusLabel } from "@/lib/projects";
import { cn } from "@/lib/utils";

// 状态计数点：颜色与看板语义一致——未设置灰、待处理琥珀、开发中天蓝、已完成翠绿
const WORKFLOW_COUNT_SEGMENTS = [
  { key: "unset", label: "未设置", dot: "bg-slate-500" },
  { key: "todo", label: "待处理", dot: "bg-amber-600" },
  { key: "in_progress", label: "开发中", dot: "bg-sky-600" },
  { key: "done", label: "已完成", dot: "bg-emerald-600" },
] as const;

// 卡片第二行展示开启口径（state=open）的内部状态分布，只用于展示，不参与排序：
// 项目顺序统一由 useProjects 按 Issue 总数（含已关闭）降序给出。

interface ProjectSwitcherProps {
  projects: Project[];
  value: string;
  onValueChange: (value: string) => void;
  placeholder?: string;
  disabled?: boolean;
  className?: string;
  /**
   * 触发器形态：default 行内小按钮（表单/页内工具条）；sidebar 占满整行
   * （侧边栏展开态）；sidebar-collapsed 首字母方块（侧边栏收起态，弹层
   * 从右侧弹出）。弹层内容三态共用。
   */
  variant?: "default" | "sidebar" | "sidebar-collapsed";
  [attribute: `data-${string}`]: string | number | boolean | undefined;
}

// 首个用户可见字符（字素）：项目名以复合 emoji 开头时，按码点取首项会拆坏
// 旗帜/ZWJ 序列（🇨🇳→🇨、👩‍💻→👩），用 Intl.Segmenter 按字素切；环境不支持时退回码点。
function firstGrapheme(name: string): string {
  if (typeof Intl !== "undefined" && "Segmenter" in Intl) {
    const segmenter = new Intl.Segmenter(undefined, {
      granularity: "grapheme",
    });
    const first = segmenter.segment(name)[Symbol.iterator]().next();
    if (!first.done) return first.value.segment;
    return "?";
  }
  return Array.from(name)[0] ?? "?";
}

export function ProjectSwitcher({
  projects,
  value,
  onValueChange,
  placeholder = "请选择项目",
  disabled = false,
  className,
  variant = "default",
  ...triggerProps
}: ProjectSwitcherProps) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);
  const activeProject = projects.find((project) => project.id === value);
  const search = query.trim().toLowerCase();
  // 顺序由 useProjects 统一提供（Issue 总数降序，含已关闭）；这里只按搜索词过滤，
  // 过滤不改变先后。卡片第二行仍展示开启口径的状态分布。
  // 搁置项目默认不列出；输入搜索词后可被搜到，卡片上带搁置标记。
  const filteredProjects = projects.filter((project) => {
    if (!search && project.is_shelved) return false;
    const fields = [project.name, project.description ?? ""];
    if (project.github_owner && project.github_repo) {
      fields.push(`${project.github_owner}/${project.github_repo}`);
    }
    return fields.some((field) => field.toLowerCase().includes(search));
  });

  // 已选中项只关闭面板，不重复回调
  const handleSelect = (project: Project) => {
    if (project.id !== value) onValueChange(project.id);
    setOpen(false);
    setQuery("");
  };

  return (
    <Popover.Root
      open={open}
      onOpenChange={(nextOpen) => {
        setOpen(nextOpen);
        if (!nextOpen) setQuery("");
      }}
    >
      <Popover.Trigger
        {...triggerProps}
        disabled={disabled}
        aria-label={`切换项目：${activeProject?.name ?? placeholder}`}
        className={cn(
          "flex items-center gap-1.5 rounded-lg border border-input bg-transparent text-sm transition-colors outline-none select-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50 dark:bg-input/30 dark:hover:bg-input/50",
          variant === "default" &&
            "h-8 w-fit justify-between py-2 pr-2 pl-2.5 whitespace-nowrap",
          variant === "sidebar" &&
            "h-8 w-full justify-between py-2 pr-2 pl-2.5 whitespace-nowrap",
          variant === "sidebar-collapsed" &&
            "size-8 shrink-0 justify-center font-medium",
          className,
        )}
      >
        {variant === "sidebar-collapsed" ? (
          // 收起态：项目首字素方块，完整名称交给 tooltip（侧边栏负责渲染）
          <span aria-hidden="true" className="leading-none">
            {firstGrapheme(activeProject?.name ?? "?")}
          </span>
        ) : (
          <>
            <span
              className={cn(
                "min-w-0 flex-1 truncate text-left",
                !activeProject && "text-muted-foreground",
              )}
            >
              {activeProject?.name ?? placeholder}
            </span>
            {activeProject?.is_shelved && (
              <Badge variant="secondary">
                {shelvedStatusLabel(activeProject)}
              </Badge>
            )}
            <ChevronDown
              aria-hidden="true"
              className="pointer-events-none size-4 shrink-0 text-muted-foreground"
            />
          </>
        )}
      </Popover.Trigger>
      <Popover.Portal>
        {/* 收起态方块挂在窄侧栏旁，弹层从右侧展开；其余从下方 */}
        <Popover.Positioner
          align="start"
          side={variant === "sidebar-collapsed" ? "right" : "bottom"}
          sideOffset={4}
          className="isolate z-50"
        >
          <Popover.Popup
            aria-label="切换项目"
            initialFocus={inputRef}
            className="flex max-h-(--available-height) w-96 max-w-(--available-width) origin-(--transform-origin) flex-col gap-3 rounded-lg bg-popover p-3 text-popover-foreground shadow-md ring-1 ring-foreground/10 duration-100 outline-none data-[side=bottom]:slide-in-from-top-2 data-[side=top]:slide-in-from-bottom-2 data-open:animate-in data-open:fade-in-0 data-open:zoom-in-95 data-closed:animate-out data-closed:fade-out-0 data-closed:zoom-out-95"
          >
            <Input
              ref={inputRef}
              aria-label="搜索项目"
              placeholder="搜索项目名称、描述或仓库"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              onKeyDown={(event) => {
                const composing =
                  event.nativeEvent.isComposing || event.keyCode === 229;
                if (
                  event.key === "Enter" &&
                  !composing &&
                  search !== "" &&
                  filteredProjects.length > 0
                ) {
                  event.preventDefault();
                  handleSelect(filteredProjects[0]);
                }
              }}
              className="shrink-0"
            />
            <div className="grid min-h-0 max-h-80 grid-cols-2 gap-2 overflow-y-auto p-1">
              {filteredProjects.length === 0 ? (
                <p className="col-span-2 py-6 text-center text-sm text-muted-foreground">
                  无匹配项目
                </p>
              ) : (
                filteredProjects.map((project) => {
                  const selected = project.id === value;
                  const secondary =
                    project.description?.trim() ||
                    (project.github_owner && project.github_repo
                      ? `${project.github_owner}/${project.github_repo}`
                      : undefined);
                  const counts = project.issue_workflow_counts;

                  return (
                    <button
                      key={project.id}
                      type="button"
                      aria-current={selected || undefined}
                      onClick={() => handleSelect(project)}
                      className={cn(
                        "min-w-0 rounded-lg border border-border p-3 text-left text-sm transition-colors outline-none hover:bg-accent focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50",
                        selected && "border-primary bg-primary/5 ring-1 ring-primary",
                      )}
                    >
                      <span className="flex min-w-0 items-baseline font-medium">
                        <span className="max-w-[66%] truncate">
                          {project.name}
                        </span>
                        {secondary && (
                          <span
                            title={secondary}
                            className="min-w-0 flex-1 truncate font-normal text-muted-foreground"
                          >
                            （{secondary}）
                          </span>
                        )}
                      </span>
                      <span className="mt-1 flex min-h-4 items-center gap-2.5 text-xs text-muted-foreground">
                        {project.is_shelved && (
                          <Badge variant="secondary">
                            {shelvedStatusLabel(project)}
                          </Badge>
                        )}
                        {counts &&
                          WORKFLOW_COUNT_SEGMENTS.map((segment) => {
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
                    </button>
                  );
                })
              )}
            </div>
          </Popover.Popup>
        </Popover.Positioner>
      </Popover.Portal>
    </Popover.Root>
  );
}
