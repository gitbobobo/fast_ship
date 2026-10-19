import { useState } from "react";
import { ListFilter, Search } from "lucide-react";
import type {
  Environment,
  EnvironmentStatus,
  ThreadStatus,
} from "@/lib/api/threads";
import {
  ENVIRONMENT_STATUS_LABELS,
  THREAD_STATUS_LABELS,
  isDefaultThreadFilters,
  type EnvironmentCounts,
  type ProjectOption,
  type ThreadFilters,
  type ThreadStatusCounts,
} from "@/lib/threads";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { OptionSelect } from "@/components/ui/option-select";
import { cn } from "@/lib/utils";

const ALL = "all";
const SETTLED_LABELS = {
  unsettled: "未完结",
  settled: "已完结",
} as const;
const FILTER_TRIGGER =
  "h-8 w-full border-0 bg-muted/50 text-sm hover:bg-muted";

/** 环境健康点：degraded 琥珀、彻底读不出（unreachable/repair）红、ok 绿。 */
const ENV_STATUS_DOT: Record<EnvironmentStatus, string> = {
  ok: "bg-emerald-500",
  degraded: "bg-amber-500",
  unreachable: "bg-red-500",
  repair: "bg-red-500",
};

/** 计数 pill 的顺序固定，颜色与状态徽章同语义。 */
const COUNT_ORDER = ["running", "waiting", "idle", "archived"] as const;
const COUNT_DOT: Record<ThreadStatus, string> = {
  running: "bg-sky-500",
  waiting: "bg-amber-500",
  idle: "bg-muted-foreground/50",
  archived: "bg-muted-foreground/30",
};
/** pill 与 aria-label 用的短名（THREAD_STATUS_LABELS 的 waiting 更长）。 */
const COUNT_LABELS: Record<ThreadStatus, string> = {
  running: "运行中",
  waiting: "等待",
  idle: "空闲",
  archived: "已归档",
};
const EMPTY_COUNTS: ThreadStatusCounts = {
  running: 0,
  waiting: 0,
  idle: 0,
  archived: 0,
  total: 0,
};

/** 环境行的可访问名称：只列非 0 状态；全 0 时报「0 个线程」。 */
function envFilterLabel(name: string, counts: ThreadStatusCounts) {
  const detail = COUNT_ORDER.filter((s) => counts[s] > 0)
    .map((s) => `${COUNT_LABELS[s]} ${counts[s]}`)
    .join("、");
  return `筛选环境：${name}（${detail ? `${detail} 个线程` : "0 个线程"}）`;
}

/**
 * 单个环境筛选项：侧栏整行单选 button，由 aria-pressed 表达选中。两行式——
 * 第一行健康点 + 名称，第二行一排「彩点 + 数量」pill（只显示非 0 状态）。
 * pill 上的 title/sr-only 是悬浮提示与兜底文本；按钮的 aria-label 覆盖内部
 * 内容的朗读，不会重复播报。
 */
export function EnvironmentFilterItem({
  name,
  counts,
  status,
  selected,
  onSelect,
}: {
  name: string;
  counts: ThreadStatusCounts;
  /** 环境状态点；「全部环境」没有单一状态，传 undefined。 */
  status?: EnvironmentStatus;
  selected: boolean;
  onSelect: () => void;
}) {
  const statusLabel = status ? ENVIRONMENT_STATUS_LABELS[status] : undefined;
  const dot = status && (
    <span
      role="img"
      aria-label={statusLabel}
      title={statusLabel}
      className={cn("size-1.5 shrink-0 rounded-full", ENV_STATUS_DOT[status])}
    />
  );
  return (
    <button
      type="button"
      aria-pressed={selected}
      aria-label={envFilterLabel(name, counts)}
      onClick={onSelect}
      className={cn(
        "flex w-full flex-col gap-1 rounded-lg px-2.5 py-1.5 text-left text-sm transition-colors",
        selected
          ? "bg-accent text-accent-foreground"
          : "text-muted-foreground hover:bg-accent/60 hover:text-foreground",
      )}
    >
      <span className="flex min-w-0 items-center gap-2">
        {dot}
        <span className="min-w-0 flex-1 truncate">{name}</span>
      </span>
      {counts.total > 0 && (
        <span className="flex items-center gap-2.5 text-xs tabular-nums">
          {COUNT_ORDER.map((s) =>
            counts[s] > 0 ? (
              <span
                key={s}
                title={`${COUNT_LABELS[s]} ${counts[s]}`}
                className="inline-flex items-center gap-1"
              >
                <span
                  aria-hidden
                  className={cn("size-1.5 rounded-full", COUNT_DOT[s])}
                />
                <span aria-hidden>{counts[s]}</span>
                <span className="sr-only">
                  {COUNT_LABELS[s]} {counts[s]}
                </span>
              </span>
            ) : null,
          )}
        </span>
      )}
    </button>
  );
}

/**
 * 项目 / 状态 / 完结三个下拉。作为两列网格的直接子元素渲染：
 * 项目占满两列，状态与完结各占一列。
 */
export function ThreadFilterControls({
  filters,
  projects,
  onFiltersChange,
}: {
  filters: ThreadFilters;
  projects: ProjectOption[];
  onFiltersChange: (patch: Partial<ThreadFilters>) => void;
}) {
  const selectedProject = projects.find((p) => p.key === filters.project);
  return (
    <>
      <OptionSelect
        value={filters.project || ALL}
        onValueChange={(value) =>
          onFiltersChange({ project: !value || value === ALL ? "" : value })
        }
        options={[
          { value: ALL, label: "全部项目" },
          ...projects.map((project) => ({
            value: project.key,
            label: project.label,
          })),
        ]}
        valueLabel={
          filters.project ? (selectedProject?.label ?? "已选项目") : "全部项目"
        }
        aria-label="筛选项目"
        className={cn(FILTER_TRIGGER, "col-span-2")}
      />
      <OptionSelect
        value={filters.status || ALL}
        onValueChange={(value) =>
          onFiltersChange({
            status: !value || value === ALL ? "" : (value as ThreadStatus),
          })
        }
        options={[
          { value: ALL, label: "全部状态" },
          ...(Object.keys(THREAD_STATUS_LABELS) as ThreadStatus[]).map(
            (status) => ({
              value: status,
              label: THREAD_STATUS_LABELS[status],
            }),
          ),
        ]}
        valueLabel={
          filters.status ? THREAD_STATUS_LABELS[filters.status] : "全部状态"
        }
        aria-label="筛选状态"
        className={FILTER_TRIGGER}
      />
      <OptionSelect
        value={filters.settled || ALL}
        onValueChange={(value) =>
          onFiltersChange({
            settled: value === "settled" || value === "unsettled" ? value : "",
          })
        }
        options={[
          { value: ALL, label: "全部" },
          { value: "unsettled", label: "未完结" },
          { value: "settled", label: "已完结" },
        ]}
        valueLabel={filters.settled ? SETTLED_LABELS[filters.settled] : "全部"}
        aria-label="筛选完结状态"
        className={FILTER_TRIGGER}
      />
    </>
  );
}

/**
 * 线程页左栏的筛选区：单选环境列表（含分面计数，环境多时内部滚动）
 * + 标题搜索 + 收起的项目/状态/完结筛选面板（有筛选时按钮上留一个点）。
 */
export function ThreadSidebar({
  environments,
  counts,
  filters,
  projects,
  onFiltersChange,
  onResetFilters,
}: {
  environments: Environment[];
  counts: EnvironmentCounts;
  filters: ThreadFilters;
  projects: ProjectOption[];
  onFiltersChange: (patch: Partial<ThreadFilters>) => void;
  onResetFilters: () => void;
}) {
  const hasFilters = !isDefaultThreadFilters(filters);
  // 面板收起时把生效的项目/状态/完结数带进可访问名称，读屏不靠那个视觉小点。
  const activeFilterCount = [filters.project, filters.status, filters.settled].filter(
    Boolean,
  ).length;
  const [filtersOpen, setFiltersOpen] = useState(false);
  return (
    <div className="shrink-0 space-y-2 px-3 py-3">
      <nav
        aria-label="环境筛选"
        className="max-h-[min(15rem,34vh)] space-y-0.5 overflow-y-auto"
      >
        <EnvironmentFilterItem
          name="全部环境"
          counts={counts.all}
          selected={!filters.environmentId}
          onSelect={() => onFiltersChange({ environmentId: "" })}
        />
        {environments.map((env) => (
          <EnvironmentFilterItem
            key={env.id}
            name={env.name}
            status={env.status}
            counts={counts.byEnvironment.get(env.id) ?? EMPTY_COUNTS}
            selected={filters.environmentId === env.id}
            onSelect={() => onFiltersChange({ environmentId: env.id })}
          />
        ))}
      </nav>
      <div className="flex items-center gap-1.5">
        <div className="relative min-w-0 flex-1">
          <Search
            aria-hidden
            className="pointer-events-none absolute top-1/2 left-2.5 h-4 w-4 -translate-y-1/2 text-muted-foreground"
          />
          <Input
            value={filters.query}
            onChange={(e) => onFiltersChange({ query: e.target.value })}
            placeholder="搜索标题"
            aria-label="搜索标题"
            className="h-8 pl-8"
          />
        </div>
        <Button
          variant="outline"
          size="icon"
          className="relative size-8 shrink-0"
          aria-label={
            activeFilterCount > 0
              ? `筛选线程（${activeFilterCount} 项生效）`
              : "筛选线程"
          }
          aria-expanded={filtersOpen}
          onClick={() => setFiltersOpen((open) => !open)}
        >
          <ListFilter className="size-4" />
          {hasFilters && (
            <span
              aria-hidden
              className="absolute top-1 right-1 size-1.5 rounded-full bg-primary"
            />
          )}
        </Button>
      </div>
      {filtersOpen && (
        <div className="grid grid-cols-2 gap-2 rounded-lg border bg-card/60 p-2">
          <ThreadFilterControls
            filters={filters}
            projects={projects}
            onFiltersChange={onFiltersChange}
          />
        </div>
      )}
      {hasFilters && (
        <Button
          variant="ghost"
          size="sm"
          className="w-full"
          onClick={onResetFilters}
        >
          清除筛选
        </Button>
      )}
    </div>
  );
}
