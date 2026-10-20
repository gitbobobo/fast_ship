import { Link, useParams } from "react-router";
import { usePersistedScroll } from "@/lib/hooks/use-persisted-scroll";
import {
  AlertTriangle,
  ChevronDown,
  MessagesSquare,
  Settings,
} from "lucide-react";
import type { Environment, ThreadSummary } from "@/lib/api/threads";
import { ENVIRONMENT_STATUS_LABELS } from "@/lib/threads";
import { Button } from "@/components/ui/button";
import { ThreadRow } from "@/components/threads/thread-row";
import { cn } from "@/lib/utils";

/** 环境异常横幅：列出还在同步或读不出的环境，其余环境的线程仍可见。 */
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
            : "部分环境正在同步或暂时无法读取，其余环境的线程仍然可见："}
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

/** 还没配对环境时的左栏引导：入口去环境设置页。 */
export function OnboardingEmpty() {
  return (
    <div className="mx-auto flex max-w-sm flex-1 flex-col items-center justify-center gap-3 px-4 py-16 text-center">
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

/**
 * 左栏的线程列表区：自身是唯一滚动容器（`overflow-y-auto`），页面其余部分
 * 不随列表滚动。内部依次是刷新/环境异常横幅、线程条目或空态、「加载更多」。
 */
export function ThreadList({
  threads,
  limit,
  groupSettled,
  settledOpen,
  onToggleSettled,
  isError,
  isFetching,
  problems,
  allDown,
  hasFilters,
  onRefetch,
  onLoadMore,
}: {
  /** 筛选 + 排序后的全量命中；分页切片在本组件内做。 */
  threads: ThreadSummary[];
  /** 未完结行的分页上限；已完结行不参与分页。 */
  limit: number;
  /** 默认视图（未选状态/完结筛选）：已完结线程收进底部折叠区。 */
  groupSettled: boolean;
  settledOpen: boolean;
  onToggleSettled: (open: boolean) => void;
  isError: boolean;
  isFetching: boolean;
  problems: Environment[];
  allDown: boolean;
  hasFilters: boolean;
  onRefetch: () => void;
  onLoadMore: () => void;
}) {
  const { environmentId = "", threadId = "" } = useParams();
  // 滚动容器在 layout 卸载后（去新建页、离开线程区）位置会丢，按固定 key 存取。
  const scrollRef = usePersistedScroll<HTMLDivElement>("threads:list", {
    ready: true,
  });
  // limit 只作用于未完结行；已完结展开后一次性全部渲染。
  const unresolved = groupSettled ? threads.filter((t) => !t.settled) : threads;
  const settled = groupSettled ? threads.filter((t) => t.settled) : [];
  const shown = unresolved.slice(0, limit);
  const row = (thread: ThreadSummary) => (
    <li key={`${thread.environment_id}:${thread.id}`}>
      <ThreadRow
        thread={thread}
        active={thread.id === threadId && thread.environment_id === environmentId}
      />
    </li>
  );
  return (
    <div
      ref={scrollRef}
      data-testid="thread-list-scroll"
      className="min-h-0 flex-1 space-y-2 overflow-y-auto pt-2"
    >
      {isError && (
        <div
          role="alert"
          className="mx-3 flex flex-wrap items-center justify-between gap-2 rounded-lg bg-amber-500/10 px-3 py-2 text-sm text-amber-800 dark:text-amber-300"
        >
          <span>刷新失败，当前显示的可能不是最新数据</span>
          <Button
            variant="outline"
            size="sm"
            onClick={onRefetch}
            disabled={isFetching}
          >
            重试
          </Button>
        </div>
      )}
      {problems.length > 0 && (
        <div className="px-3">
          <EnvironmentProblems environments={problems} allDown={allDown} />
        </div>
      )}
      {threads.length === 0 ? (
        hasFilters ? (
          <p className="px-3 py-16 text-center text-sm text-muted-foreground">
            没有符合筛选条件的线程
          </p>
        ) : allDown ? (
          <div className="flex flex-col items-center gap-3 px-3 py-16 text-center">
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
        ) : problems.length > 0 ? (
          // 有环境 degraded（同步中/重连中）时列表可能只是还没长出来，
          // 不能断言「暂无线程」。
          <p className="px-3 py-16 text-center text-sm text-muted-foreground">
            环境尚未同步完成，稍后会自动显示线程。
          </p>
        ) : (
          <p className="px-3 py-16 text-center text-sm text-muted-foreground">
            暂无线程
          </p>
        )
      ) : (
        <>
          <ul role="list" data-testid="thread-grid" className="space-y-0.5 px-2 pb-3">
            {shown.map(row)}
            {settled.length > 0 && (
              <li>
                <button
                  type="button"
                  aria-expanded={settledOpen}
                  aria-label={`已完结线程（${settled.length} 条）`}
                  onClick={() => onToggleSettled(!settledOpen)}
                  className="flex w-full items-center gap-2 rounded-md px-2.5 py-1.5 text-xs text-muted-foreground transition-colors hover:text-foreground"
                >
                  <span className="shrink-0">已完结 · {settled.length}</span>
                  <span aria-hidden className="h-px flex-1 bg-border" />
                  <ChevronDown
                    aria-hidden
                    className={cn(
                      "size-3.5 shrink-0 transition-transform",
                      settledOpen && "rotate-180",
                    )}
                  />
                </button>
              </li>
            )}
            {settledOpen && settled.map(row)}
          </ul>
          {unresolved.length > shown.length && (
            <div className="flex justify-center pb-3">
              <Button variant="outline" onClick={onLoadMore}>
                加载更多（还有 {unresolved.length - shown.length} 条）
              </Button>
            </div>
          )}
        </>
      )}
    </div>
  );
}
