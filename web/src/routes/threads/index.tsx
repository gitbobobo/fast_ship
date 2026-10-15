import { useMemo } from "react";
import { Link } from "react-router";
import {
  AlertTriangle,
  ChevronDown,
  MessagesSquare,
  Plus,
  RefreshCw,
  Search,
  Settings,
} from "lucide-react";
import { Header } from "@/components/layout/header";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { OptionSelect } from "@/components/ui/option-select";
import { ThreadCard } from "@/components/threads/thread-card";
import { useThreads } from "@/lib/hooks/use-threads";
import { useThreadListStore } from "@/lib/store/thread-list-store";
import type { Environment, ThreadStatus } from "@/lib/api/threads";
import {
  ENVIRONMENT_STATUS_LABELS,
  THREAD_STATUS_LABELS,
  filterThreads,
  isDefaultThreadFilters,
  listProblemEnvironments,
  listProjects,
  sortThreads,
} from "@/lib/threads";
import { cn } from "@/lib/utils";

const ALL = "all";
const SETTLED_LABELS = {
  unsettled: "未完结",
  settled: "已完结",
} as const;
const FILTER_TRIGGER =
  "h-8 w-full border-0 bg-muted/50 text-sm hover:bg-muted sm:w-40";

function EnvironmentFilter({
  environments,
  selected,
  onChange,
}: {
  environments: Environment[];
  selected: string[];
  onChange: (ids: string[]) => void;
}) {
  const label =
    selected.length === 0
      ? "全部环境"
      : selected.length === 1
        ? (environments.find((e) => e.id === selected[0])?.name ?? "1 个环境")
        : `${selected.length} 个环境`;

  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        aria-label="筛选环境"
        className={cn(
          "inline-flex h-8 w-full items-center justify-between gap-2 rounded-lg bg-muted/50 px-2.5 text-sm transition-colors hover:bg-muted focus-visible:outline-hidden focus-visible:ring-2 focus-visible:ring-ring sm:w-40",
          selected.length > 0 && "text-primary",
        )}
      >
        <span className="truncate">{label}</span>
        <ChevronDown className="h-4 w-4 shrink-0 text-muted-foreground" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-56">
        {environments.map((env) => (
          <DropdownMenuCheckboxItem
            key={env.id}
            checked={selected.includes(env.id)}
            onCheckedChange={(checked) =>
              onChange(
                checked
                  ? [...selected, env.id]
                  : selected.filter((id) => id !== env.id),
              )
            }
          >
            <span className="truncate">{env.name}</span>
          </DropdownMenuCheckboxItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function EnvironmentProblems({
  environments,
  allDown,
}: {
  environments: Environment[];
  allDown: boolean;
}) {
  if (environments.length === 0) return null;
  return (
    <div
      role="alert"
      className="flex items-start gap-2 rounded-lg bg-amber-500/10 px-3 py-2 text-sm text-amber-800 dark:text-amber-300"
    >
      <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
      <div className="min-w-0 flex-1">
        <p>
          {allDown
            ? "所有环境都无法读取："
            : "部分环境暂时无法读取，其余环境的线程仍然可见："}
        </p>
        <ul className="mt-1 space-y-0.5">
          {environments.map((env) => (
            <li key={env.id} className="break-words">
              <span className="font-medium">{env.name}</span>
              {" — "}
              {ENVIRONMENT_STATUS_LABELS[env.status] ?? env.status}
              {env.error ? `（${env.error}）` : ""}
            </li>
          ))}
        </ul>
      </div>
      <Link
        to="/settings/environments"
        className="shrink-0 underline underline-offset-2"
      >
        管理环境
      </Link>
    </div>
  );
}

function OnboardingEmpty() {
  return (
    <div className="mx-auto flex max-w-sm flex-col items-center gap-3 py-20 text-center">
      <MessagesSquare className="h-8 w-8 text-muted-foreground" />
      <h2 className="text-base font-medium">还没有连接环境</h2>
      <p className="text-sm text-muted-foreground">
        在设置中粘贴 T3 Code 的配对链接，添加环境后，这里会汇总各环境里的线程。
      </p>
      <Button render={<Link to="/settings/environments" />}>
        <Settings className="mr-1.5 h-4 w-4" />
        去设置配对
      </Button>
    </div>
  );
}

export default function ThreadsPage() {
  const { data, isLoading, isError, isFetching, refetch } = useThreads();
  const { filters, limit, setFilters, resetFilters, loadMore } =
    useThreadListStore();

  const environments = data?.environments;
  const allThreads = data?.items;

  const visible = useMemo(
    () => sortThreads(filterThreads(allThreads ?? [], filters)),
    [allThreads, filters],
  );
  const projects = useMemo(() => listProjects(allThreads ?? []), [allThreads]);
  const selectedProject = projects.find((p) => p.key === filters.project);
  const problems = useMemo(
    () => listProblemEnvironments(environments ?? []),
    [environments],
  );

  const shown = visible.slice(0, limit);
  const noEnvironments = !!data && (environments ?? []).length === 0;
  const hasFilters = !isDefaultThreadFilters(filters);
  // degraded 仍返回部分数据，不算「无法读取」；只有完全读不出（unreachable/
  // repair）的环境才算 down，全部 down 才显示「所有环境都无法读取」。
  const allDown =
    !!environments?.length &&
    environments.every((e) => e.status === "unreachable" || e.status === "repair");

  return (
    <>
      <Header
        title="线程"
        actions={
          <div className="flex items-center gap-2">
            <Button
              variant="outline"
              size="sm"
              onClick={() => refetch()}
              disabled={isFetching}
              aria-label="刷新线程"
            >
              <RefreshCw
                className={cn("mr-1.5 h-3.5 w-3.5", isFetching && "animate-spin")}
              />
              刷新
            </Button>
            <Button size="sm" render={<Link to="/threads/new" />}>
              <Plus className="mr-1.5 h-3.5 w-3.5" />
              新建线程
            </Button>
          </div>
        }
      />
      <div className="flex flex-1 flex-col gap-4 p-4 md:p-6">
        {isLoading ? (
          <div className="grid gap-3 sm:grid-cols-[repeat(auto-fill,minmax(18rem,1fr))]">
            {Array.from({ length: 6 }).map((_, i) => (
              <Skeleton key={i} className="h-36 rounded-xl" />
            ))}
          </div>
        ) : !data ? (
          <div className="py-16 text-center text-sm text-muted-foreground">
            <p>线程加载失败</p>
            <Button
              variant="outline"
              size="sm"
              className="mt-3"
              onClick={() => refetch()}
            >
              重试
            </Button>
          </div>
        ) : noEnvironments ? (
          <OnboardingEmpty />
        ) : (
          <>
            {isError && (
              <div
                role="alert"
                className="flex items-center justify-between gap-2 rounded-lg bg-amber-500/10 px-3 py-2 text-sm text-amber-800 dark:text-amber-300"
              >
                <span>刷新失败，当前显示的可能不是最新数据</span>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => refetch()}
                  disabled={isFetching}
                >
                  重试
                </Button>
              </div>
            )}
            <EnvironmentProblems environments={problems} allDown={allDown} />
            <div className="flex flex-col gap-2 sm:flex-row sm:flex-wrap sm:items-center">
              <div className="relative sm:w-64">
                <Search className="pointer-events-none absolute top-1/2 left-2.5 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
                <Input
                  value={filters.query}
                  onChange={(e) => setFilters({ query: e.target.value })}
                  placeholder="搜索标题"
                  aria-label="搜索标题"
                  className="h-8 pl-8"
                />
              </div>
              <EnvironmentFilter
                environments={environments ?? []}
                selected={filters.environmentIds}
                onChange={(environmentIds) => setFilters({ environmentIds })}
              />
              <OptionSelect
                value={filters.project || ALL}
                onValueChange={(value) =>
                  setFilters({ project: !value || value === ALL ? "" : value })
                }
                options={[
                  { value: ALL, label: "全部项目" },
                  ...projects.map((project) => ({
                    value: project.key,
                    label: project.label,
                  })),
                ]}
                valueLabel={
                  filters.project
                    ? (selectedProject?.label ?? "已选项目")
                    : "全部项目"
                }
                aria-label="筛选项目"
                className={FILTER_TRIGGER}
              />
              <OptionSelect
                value={filters.status || ALL}
                onValueChange={(value) =>
                  setFilters({
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
                  filters.status
                    ? THREAD_STATUS_LABELS[filters.status]
                    : "全部状态"
                }
                aria-label="筛选状态"
                className={FILTER_TRIGGER}
              />
              <OptionSelect
                value={filters.settled || ALL}
                onValueChange={(value) =>
                  setFilters({
                    settled:
                      value === "settled" || value === "unsettled" ? value : "",
                  })
                }
                options={[
                  { value: ALL, label: "全部" },
                  { value: "unsettled", label: "未完结" },
                  { value: "settled", label: "已完结" },
                ]}
                valueLabel={
                  filters.settled ? SETTLED_LABELS[filters.settled] : "全部"
                }
                aria-label="筛选完结状态"
                className={FILTER_TRIGGER}
              />
              {hasFilters && (
                <Button variant="ghost" size="sm" onClick={resetFilters}>
                  清除筛选
                </Button>
              )}
            </div>

            {shown.length === 0 ? (
              hasFilters ? (
                <p className="py-16 text-center text-sm text-muted-foreground">
                  没有符合筛选条件的线程
                </p>
              ) : allDown ? (
                <div className="flex flex-col items-center gap-3 py-16 text-center">
                  <p className="text-sm text-muted-foreground">
                    暂时读不到任何线程，请先修复环境连接。
                  </p>
                  <Button
                    variant="outline"
                    size="sm"
                    render={<Link to="/settings/environments" />}
                  >
                    去重新配对
                  </Button>
                </div>
              ) : (
                <p className="py-16 text-center text-sm text-muted-foreground">
                  暂无线程
                </p>
              )
            ) : (
              <>
                <div
                  data-testid="thread-grid"
                  className="grid gap-3 sm:grid-cols-[repeat(auto-fill,minmax(18rem,1fr))]"
                >
                  {shown.map((thread) => (
                    <ThreadCard
                      key={`${thread.environment_id}:${thread.id}`}
                      thread={thread}
                    />
                  ))}
                </div>
                {visible.length > shown.length && (
                  <div className="flex justify-center">
                    <Button variant="outline" onClick={loadMore}>
                      加载更多（还有 {visible.length - shown.length} 条）
                    </Button>
                  </div>
                )}
              </>
            )}
          </>
        )}
      </div>
    </>
  );
}
