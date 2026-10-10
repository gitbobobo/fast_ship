import { useRef, useState } from "react";
import { Popover } from "@base-ui/react/popover";
import { Check, ChevronDown } from "lucide-react";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";

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
  const [query, setQuery] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);
  const activeProject = projects.find((project) => project.id === value);
  const search = query.trim().toLowerCase();
  // 按 Issue 总数降序展示；计数缺失按 0，同计数靠稳定排序保持服务端顺序
  const filteredProjects = projects
    .filter((project) => {
      const fields = [project.name, project.description ?? ""];
      if (project.github_owner && project.github_repo) {
        fields.push(`${project.github_owner}/${project.github_repo}`);
      }
      return fields.some((field) => field.toLowerCase().includes(search));
    })
    .sort((a, b) => (b.issue_count ?? 0) - (a.issue_count ?? 0));

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
          "flex h-8 w-fit items-center justify-between gap-1.5 rounded-lg border border-input bg-transparent py-2 pr-2 pl-2.5 text-sm whitespace-nowrap transition-colors outline-none select-none focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50 dark:bg-input/30 dark:hover:bg-input/50",
          className,
        )}
      >
        <span
          className={cn(
            "min-w-0 flex-1 truncate text-left",
            !activeProject && "text-muted-foreground",
          )}
        >
          {activeProject?.name ?? placeholder}
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
                    project.description ||
                    (project.github_owner && project.github_repo
                      ? `${project.github_owner}/${project.github_repo}`
                      : undefined);

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
                      <span className="flex items-center gap-1.5">
                        <span className="min-w-0 flex-1 truncate font-medium">
                          {project.name}
                        </span>
                        {selected && (
                          <Check aria-hidden="true" className="size-4 shrink-0 text-primary" />
                        )}
                      </span>
                      <span className="mt-1 block min-h-4 truncate text-xs text-muted-foreground">
                        {secondary}
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
