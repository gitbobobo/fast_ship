import { useEffect, useMemo, useRef } from "react";
import { Link, Outlet, useParams } from "react-router";
import { Plus, RefreshCw } from "lucide-react";
import { MobileNav } from "@/components/layout/sidebar";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { ThreadSidebar } from "@/components/threads/thread-sidebar";
import { OnboardingEmpty, ThreadList } from "@/components/threads/thread-list";
import { useThreads } from "@/lib/hooks/use-threads";
import { useThreadListStore } from "@/lib/store/thread-list-store";
import {
  countThreadsByEnvironment,
  filterThreads,
  isDefaultThreadFilters,
  listProblemEnvironments,
  listProjects,
  sortThreads,
} from "@/lib/threads";
import { cn } from "@/lib/utils";

/**
 * 线程区的主从两栏壳：左栏是环境筛选 + 可独立滚动的线程列表，右栏经
 * <Outlet/> 渲染线程详情（或 index 的空态）。布局随 /threads 下的子路由
 * 切换保持挂载，切线程不会重排左栏或丢滚动位置。
 * 窄于 lg 时只显示一栏：开着线程就只显详情，否则只显列表。
 */
export default function ThreadsLayout() {
  const { environmentId, threadId } = useParams();
  const threadOpen = Boolean(threadId);
  // 同 ID 线程可能存在于不同环境，切换判定必须带上环境。
  const threadKey = `${environmentId ?? ""}/${threadId ?? ""}`;
  const { data, isLoading, isError, isFetching, refetch } = useThreads();
  const {
    filters,
    limit,
    settledOpen,
    setFilters,
    resetFilters,
    setSettledOpen,
    loadMore,
  } = useThreadListStore();

  // 列表在常驻 layout 里不随导航重挂载，切线程时补一次重读（详情重读会
  // 顺带把当前线程摘要回写列表，但其它线程的要靠自己刷新）。
  const lastThreadKey = useRef(threadKey);
  useEffect(() => {
    if (lastThreadKey.current === threadKey) return;
    lastThreadKey.current = threadKey;
    void refetch();
  }, [threadKey, refetch]);

  const environments = data?.environments;
  const allThreads = data?.items;

  const visible = useMemo(
    () => sortThreads(filterThreads(allThreads ?? [], filters)),
    [allThreads, filters],
  );
  // 侧栏每个环境的数量：忽略环境单选，只叠加搜索/项目/状态/完结。
  const counts = useMemo(
    () => countThreadsByEnvironment(allThreads ?? [], filters),
    [allThreads, filters],
  );
  const projects = useMemo(() => listProjects(allThreads ?? []), [allThreads]);
  const problems = useMemo(
    () => listProblemEnvironments(environments ?? []),
    [environments],
  );

  // 默认视图（没选状态/完结筛选）才把已完结线程收进底部折叠区；分页只数
  // 未完结行，切片在 ThreadList 内做。
  const groupSettled = !filters.settled && !filters.status;
  const noEnvironments = !!data && (environments ?? []).length === 0;
  const hasFilters = !isDefaultThreadFilters(filters);
  // degraded 仍返回部分数据，不算「无法读取」（reconnect/sync 中会自行恢复）；
  // 只有完全读不出（unreachable/repair）的环境才算 down，全部 down 才显示
  // 「所有环境都无法读取」。
  const allDown =
    !!environments?.length &&
    environments.every((e) => e.status === "unreachable" || e.status === "repair");
  const scopeName = filters.environmentId
    ? (environments?.find((e) => e.id === filters.environmentId)?.name ??
      "所选环境")
    : "全部环境";

  return (
    <div className="flex h-full min-h-0 flex-1">
      <aside
        className={cn(
          "min-h-0 w-full shrink-0 flex-col border-r bg-sidebar/30 lg:w-80",
          threadOpen ? "hidden lg:flex" : "flex",
        )}
      >
        <div className="flex h-14 shrink-0 items-center gap-1 border-b px-3">
          <MobileNav />
          <h1 className="min-w-0 flex-1 text-base font-semibold">线程</h1>
          <Button
            variant="ghost"
            size="icon"
            className="size-8"
            onClick={() => refetch()}
            disabled={isFetching}
            aria-label="刷新线程"
          >
            <RefreshCw
              className={cn(
                "size-4",
                isFetching && "animate-spin motion-reduce:animate-none",
              )}
            />
          </Button>
          <Button
            variant="ghost"
            size="icon"
            className="size-8"
            render={<Link to="/threads/new" />}
            aria-label="新建线程"
          >
            <Plus className="size-4" />
          </Button>
        </div>
        {isLoading ? (
          <>
            <div aria-hidden className="shrink-0 space-y-1.5 px-3 py-3">
              {Array.from({ length: 4 }).map((_, i) => (
                <Skeleton key={i} className="h-7 rounded-lg" />
              ))}
            </div>
            <div
              aria-hidden
              className="min-h-0 flex-1 space-y-1 overflow-hidden px-2"
            >
              {Array.from({ length: 8 }).map((_, i) => (
                <Skeleton key={i} className="h-12 rounded-lg" />
              ))}
            </div>
          </>
        ) : !data ? (
          <div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-3 px-4 py-16 text-center">
            <p className="text-sm text-muted-foreground">线程加载失败</p>
            <Button variant="outline" size="sm" onClick={() => refetch()}>
              重试
            </Button>
          </div>
        ) : noEnvironments ? (
          <OnboardingEmpty />
        ) : (
          <>
            <div className="max-h-[45vh] shrink-0 overflow-y-auto">
              <ThreadSidebar
                environments={environments ?? []}
                counts={counts}
                filters={filters}
                projects={projects}
                onFiltersChange={setFilters}
                onResetFilters={resetFilters}
              />
            </div>
            <div className="flex shrink-0 items-center justify-between gap-2 border-t px-3 py-2 text-xs text-muted-foreground">
              <span className="truncate">
                {scopeName} · {visible.length} 个线程
              </span>
              {isFetching && (
                <RefreshCw
                  aria-hidden
                  className="size-3.5 shrink-0 animate-spin motion-reduce:animate-none"
                />
              )}
            </div>
            <ThreadList
              threads={visible}
              limit={limit}
              groupSettled={groupSettled}
              settledOpen={settledOpen}
              onToggleSettled={setSettledOpen}
              isError={isError}
              isFetching={isFetching}
              problems={problems}
              allDown={allDown}
              hasFilters={hasFilters}
              onRefetch={() => refetch()}
              onLoadMore={loadMore}
            />
          </>
        )}
      </aside>
      <div
        className={cn(
          "@container min-h-0 min-w-0 flex-1 flex-col overflow-y-auto",
          threadOpen ? "flex" : "hidden lg:flex",
        )}
      >
        <Outlet />
      </div>
    </div>
  );
}
