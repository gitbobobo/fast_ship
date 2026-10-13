import { ArrowDown, ArrowUp, Pause, Pencil, Play, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { ApiRequestError } from "@/lib/api/client";
import type { ThreadQueue, ThreadQueueItem } from "@/lib/api/threads";
import {
  useCancelQueued,
  useReorderQueued,
  useResumeQueue,
  useSteerQueued,
} from "@/lib/hooks/use-threads";
import { reorderTarget } from "@/lib/threads-compose";
import { cn } from "@/lib/utils";
import { AttachmentThumbs } from "./thread-images";

export function actionErrorText(error: unknown, fallback = "操作失败，请重试") {
  return error instanceof ApiRequestError && error.message
    ? error.message
    : fallback;
}

/**
 * 排队消息：在输入框上方展示正文/缩略图。只读环境只展示；陈旧时（详情刷新失败）
 * 所有依赖队列目标的按钮禁用，避免对已不存在的 run 下命令。
 */
export function ThreadQueuePanel({
  environmentId,
  threadId,
  queue,
  canOperate,
  stale,
  editingRunId,
  actionLocked = false,
  onEdit,
}: {
  environmentId: string;
  threadId: string;
  queue: ThreadQueue;
  canOperate: boolean;
  stale: boolean;
  editingRunId?: string;
  /** 保存排队编辑进行中时锁住队列操作，避免卸载正在提交的附件。 */
  actionLocked?: boolean;
  onEdit: (item: ThreadQueueItem) => void;
}) {
  const steer = useSteerQueued(environmentId, threadId);
  const cancel = useCancelQueued(environmentId, threadId);
  const reorder = useReorderQueued(environmentId, threadId);
  const resume = useResumeQueue(environmentId, threadId);

  if (queue.items.length === 0) return null;

  const mutations = [steer, cancel, reorder, resume];
  const busy = mutations.some((m) => m.isPending);
  const failed = mutations.find((m) => m.isError);
  const locked = busy || stale || !canOperate || actionLocked;

  return (
    <section aria-label="消息队列" className="space-y-2">
      <div className="flex items-center justify-between gap-2 text-xs text-muted-foreground">
        <span>队列 · {queue.items.length} 条待发送</span>
        {queue.held && (
          <span className="inline-flex items-center gap-2">
            <span className="inline-flex items-center gap-1 text-amber-700 dark:text-amber-400">
              <Pause className="h-3.5 w-3.5" />
              队列已暂停
            </span>
            {canOperate && (
              <Button
                size="xs"
                variant="outline"
                disabled={locked}
                onClick={() => resume.mutate()}
              >
                <Play className="mr-1 h-3 w-3" />
                恢复队列
              </Button>
            )}
          </span>
        )}
      </div>
      <ul className="space-y-1.5">
        {queue.items.map((item, index) => (
          <li
            key={item.run_id}
            data-testid="queue-item"
            className={cn(
              "space-y-1.5 rounded-xl border bg-muted/30 px-3 py-2 text-sm",
              editingRunId === item.run_id && "border-primary/50",
            )}
          >
            <p className="break-words whitespace-pre-wrap">{item.text}</p>
            <AttachmentThumbs
              environmentId={environmentId}
              attachments={item.attachments}
            />
            {canOperate && (
              <div className="flex flex-wrap items-center gap-1.5">
                {queue.can_steer && (
                  <Button
                    size="xs"
                    variant="outline"
                    disabled={locked}
                    onClick={() => steer.mutate(item.run_id)}
                  >
                    Steer
                  </Button>
                )}
                <Button
                  size="xs"
                  variant="ghost"
                  disabled={locked}
                  onClick={() => onEdit(item)}
                >
                  <Pencil className="mr-1 h-3 w-3" />
                  编辑
                </Button>
                <Button
                  size="xs"
                  variant="ghost"
                  disabled={locked}
                  onClick={() => cancel.mutate(item.run_id)}
                >
                  <Trash2 className="mr-1 h-3 w-3" />
                  取消
                </Button>
                {queue.can_reorder && (
                  <>
                    <Button
                      size="icon-xs"
                      variant="ghost"
                      aria-label="上移"
                      disabled={
                        locked || reorderTarget(queue.items, index, "up") === undefined
                      }
                      onClick={() => {
                        const before = reorderTarget(queue.items, index, "up");
                        if (before !== undefined) {
                          reorder.mutate({ runId: item.run_id, beforeRunId: before });
                        }
                      }}
                    >
                      <ArrowUp className="h-3 w-3" />
                    </Button>
                    <Button
                      size="icon-xs"
                      variant="ghost"
                      aria-label="下移"
                      disabled={
                        locked || reorderTarget(queue.items, index, "down") === undefined
                      }
                      onClick={() => {
                        const before = reorderTarget(queue.items, index, "down");
                        if (before !== undefined) {
                          reorder.mutate({ runId: item.run_id, beforeRunId: before });
                        }
                      }}
                    >
                      <ArrowDown className="h-3 w-3" />
                    </Button>
                  </>
                )}
              </div>
            )}
          </li>
        ))}
      </ul>
      {failed && (
        <p role="alert" className="text-xs text-destructive">
          {actionErrorText(failed.error)}
        </p>
      )}
    </section>
  );
}
