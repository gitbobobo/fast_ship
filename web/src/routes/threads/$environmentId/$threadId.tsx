import { Link, useParams } from "react-router";
import { ArrowLeft, Cpu, GitBranch, GitPullRequest } from "lucide-react";
import { Header } from "@/components/layout/header";
import { Skeleton } from "@/components/ui/skeleton";
import { Button } from "@/components/ui/button";
import { ThreadItemView } from "@/components/threads/thread-item-view";
import { ThreadStatusBadge } from "@/components/threads/thread-status-badge";
import { useThreadDetail } from "@/lib/hooks/use-threads";
import { formatRelativeTime } from "@/lib/utils/format";

export default function ThreadDetailPage() {
  const { environmentId = "", threadId = "" } = useParams();
  const { data, isLoading, isError, isFetching, refetch } = useThreadDetail(environmentId, threadId);
  const thread = data?.thread;
  const model = thread
    ? [thread.provider, thread.model].filter(Boolean).join(" / ")
    : "";

  return (
    <>
      <Header title={thread?.title || "线程详情"} />
      <div className="mx-auto flex w-full max-w-3xl flex-1 flex-col gap-5 p-4 md:p-6">
        <Link
          to="/threads"
          className="inline-flex w-fit items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground"
        >
          <ArrowLeft className="h-4 w-4" />
          全部线程
        </Link>

        {isLoading ? (
          <div className="space-y-3">
            <Skeleton className="h-6 w-2/3" />
            <Skeleton className="h-24 rounded-xl" />
            <Skeleton className="h-24 rounded-xl" />
          </div>
        ) : !data || !thread ? (
          <div className="py-16 text-center text-sm text-muted-foreground">
            <p>线程加载失败，可能已被删除，或所在环境暂时不可达。</p>
            <Button
              variant="outline"
              size="sm"
              className="mt-3"
              onClick={() => refetch()}
            >
              重试
            </Button>
          </div>
        ) : (
          <>
            <div className="space-y-2">
              <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 text-xs text-muted-foreground">
                <ThreadStatusBadge status={thread.status} />
                <span>
                  {thread.environment_name} · {thread.project_name}
                </span>
                {thread.branch && (
                  <span className="inline-flex items-center gap-1">
                    <GitBranch className="h-3.5 w-3.5" />
                    {thread.branch}
                  </span>
                )}
                {model && (
                  <span className="inline-flex items-center gap-1">
                    <Cpu className="h-3.5 w-3.5" />
                    {model}
                  </span>
                )}
                {thread.pull_request_count > 0 && (
                  <span className="inline-flex items-center gap-1">
                    <GitPullRequest className="h-3.5 w-3.5" />
                    {thread.pull_request_count}
                  </span>
                )}
                <span>{formatRelativeTime(thread.updated_at)}更新</span>
              </div>
              {isError && (
                <div
                  role="alert"
                  className="flex items-center justify-between gap-2 rounded-lg bg-amber-500/10 px-3 py-2 text-sm text-amber-800 dark:text-amber-300"
                >
                  <span>刷新失败，当前显示的可能不是最新内容</span>
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
              {thread.status === "running" && (
                <p className="text-xs text-muted-foreground">
                  线程运行中，内容会自动更新。
                </p>
              )}
            </div>

            {data.items.length === 0 ? (
              <p className="py-12 text-center text-sm text-muted-foreground">
                这个线程还没有内容
              </p>
            ) : (
              <div className="space-y-4">
                {data.items.map((item) => (
                  <ThreadItemView
                    key={item.id}
                    item={item}
                    environmentId={environmentId}
                    threadId={threadId}
                    revision={item.updated_at || thread.updated_at}
                  />
                ))}
              </div>
            )}
          </>
        )}
      </div>
    </>
  );
}
