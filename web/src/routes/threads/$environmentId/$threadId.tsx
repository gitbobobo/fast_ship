import { useEffect, useState } from "react";
import { HTTPError } from "ky";
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
  const { data, error, isLoading, isError, isFetching, refetch } = useThreadDetail(environmentId, threadId);
  const thread = data?.thread;
  const model = thread
    ? [thread.provider, thread.model].filter(Boolean).join(" / ")
    : "";

  // 首次加载失败的文案按服务端响应区分：404=线程没了，其余带服务端
  // message；凭证失效一类在文案外再给重新配对入口。
  const [errorMessage, setErrorMessage] = useState("");
  useEffect(() => {
    setErrorMessage("");
    if (!(error instanceof HTTPError)) return;
    let cancelled = false;
    void error.response.json<ApiResponse<unknown>>()
      .then((body) => {
        if (!cancelled && body?.message) setErrorMessage(body.message);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [error]);
  const notFound = error instanceof HTTPError && error.response.status === 404;
  const needsRepair = errorMessage.includes("重新配对");
  const failureText = notFound
    ? "线程不存在，可能已被删除或移动。"
    : errorMessage || "线程加载失败，所在环境可能暂时不可达。";

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
            <p>{failureText}</p>
            <div className="mt-3 flex items-center justify-center gap-2">
              <Button variant="outline" size="sm" onClick={() => refetch()}>
                重试
              </Button>
              {needsRepair && (
                <Button
                  variant="outline"
                  size="sm"
                  render={<Link to="/settings/environments" />}
                >
                  去重新配对
                </Button>
              )}
            </div>
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
