import { useMemo, useRef } from "react";
import { ArrowLeft } from "lucide-react";
import { Link, useParams } from "react-router";
import { Header } from "@/components/layout/header";
import { Skeleton } from "@/components/ui/skeleton";
import { Button } from "@/components/ui/button";
import { ThreadChat } from "@/components/threads/thread-chat";
import { ThreadInfoCard } from "@/components/threads/thread-info-card";
import { ThreadItemView } from "@/components/threads/thread-item-view";
import { ThreadStatusBadge } from "@/components/threads/thread-status-badge";
import { ThreadToolGroup } from "@/components/threads/thread-tool-group";
import { ApiRequestError } from "@/lib/api/client";
import { useThreadDetail } from "@/lib/hooks/use-threads";
import { useScrollFollow } from "@/lib/hooks/use-scroll-follow";
import { groupTimeline } from "@/lib/threads-timeline";

/** 按线程 key 渲染：切线程时输入框、编辑状态、密钥输入等局部状态一并销毁。 */
export default function ThreadDetailPage() {
  const { environmentId = "", threadId = "" } = useParams();
  return (
    <ThreadDetailView
      key={`${environmentId}/${threadId}`}
      environmentId={environmentId}
      threadId={threadId}
    />
  );
}

function ThreadDetailView({
  environmentId,
  threadId,
}: {
  environmentId: string;
  threadId: string;
}) {
  const { data, error, isLoading, isError, isFetching, refetch } = useThreadDetail(environmentId, threadId);
  const canOperate = data?.access === "operate";
  const thread = data?.thread;
  const items = data?.items;
  const timeline = useMemo(() => groupTimeline(items ?? []), [items]);
  const chatAnchor = useRef<HTMLDivElement>(null);
  const lastItem = items?.[items.length - 1];
  useScrollFollow(
    chatAnchor,
    `${items?.length ?? 0}|${lastItem?.id}|${lastItem?.updated_at ?? ""}|${lastItem?.status ?? ""}|${lastItem?.text.length ?? 0}`,
    Boolean(data),
  );

  // 首次加载失败的文案按服务端响应区分：404=线程没了，其余带服务端
  // message；凭证失效一类在文案外再给重新配对入口。响应体在请求边界
  // 已解码成 ApiRequestError（见 client.ts），这里同步读即可。
  const notFound = error instanceof ApiRequestError && error.status === 404;
  const errorMessage = error instanceof ApiRequestError ? error.message : "";
  const needsRepair = errorMessage.includes("重新配对");
  const failureText = notFound
    ? "线程不存在，可能已被删除或移动。"
    : errorMessage || "线程加载失败，所在环境可能暂时不可达。";

  return (
    <>
      <Header
        title={thread?.title || "线程详情"}
        backTo="/threads"
        actions={
          <>
            {thread && (
              <>
                {/* 右栏够宽时父线程入口在信息卡（@4xl 起显示）；信息卡缺席时这里补一个 */}
                {thread.parent_thread_id && (
                  <Link
                    to={`/threads/${encodeURIComponent(environmentId)}/${encodeURIComponent(thread.parent_thread_id)}`}
                    className="inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground @4xl:hidden"
                  >
                    <ArrowLeft className="size-4" />
                    父线程
                  </Link>
                )}
                <ThreadStatusBadge
                  status={thread.status}
                  runStartedAt={thread.run_started_at}
                />
              </>
            )}
          </>
        }
      />
      <div className="flex w-full flex-1 gap-6 px-4 md:px-6">
        <div
          ref={chatAnchor}
          className="mx-auto flex w-full max-w-3xl min-w-0 flex-1 flex-col gap-5 py-4 md:py-6"
        >
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
              {isError && (
                <div
                  role="alert"
                  className="flex items-center justify-between gap-2 rounded-lg bg-amber-500/10 px-3 py-2 text-sm text-amber-800 dark:text-amber-300"
                >
                  <span>
                    {errorMessage
                      ? `${errorMessage}，当前显示的可能不是最新内容`
                      : "刷新失败，当前显示的可能不是最新内容"}
                  </span>
                  <div className="flex shrink-0 items-center gap-2">
                    {needsRepair && (
                      <Button
                        variant="outline"
                        size="sm"
                        render={<Link to="/settings/environments" />}
                      >
                        去重新配对
                      </Button>
                    )}
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => refetch()}
                      disabled={isFetching}
                    >
                      重试
                    </Button>
                  </div>
                </div>
              )}

              {data.items.length === 0 ? (
                <p className="py-12 text-center text-sm text-muted-foreground">
                  这个线程还没有内容
                </p>
              ) : (
                <div className="space-y-4">
                  {timeline.map((entry) =>
                    entry.kind === "group" ? (
                      <ThreadToolGroup
                        key={entry.id}
                        items={entry.items}
                        live={thread.status === "running"}
                        environmentId={environmentId}
                        threadId={threadId}
                        fallbackRevision={thread.updated_at}
                      />
                    ) : (
                      <ThreadItemView
                        key={entry.item.id}
                        item={entry.item}
                        environmentId={environmentId}
                        threadId={threadId}
                        revision={entry.item.updated_at || thread.updated_at}
                        canOperate={canOperate}
                        stale={isError}
                      />
                    ),
                  )}
                </div>
              )}
              <div className="sticky bottom-0 mt-auto -mx-1 bg-background/95 px-1 pt-2 pb-3 backdrop-blur">
                <ThreadChat
                  detail={data}
                  environmentId={environmentId}
                  threadId={threadId}
                  canOperate={canOperate}
                  stale={isError}
                />
              </div>
            </>
          )}
        </div>
        {thread && (
          <ThreadInfoCard thread={thread} environmentId={environmentId} />
        )}
      </div>
    </>
  );
}
