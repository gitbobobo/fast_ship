import { useState } from "react";
import { Square } from "lucide-react";
import { Button } from "@/components/ui/button";
import type {
  ChatAttachment,
  ThreadActivity,
  ThreadDetail,
  ThreadQueue,
  ThreadQueueItem,
} from "@/lib/api/threads";
import { useImageUploads } from "@/lib/hooks/use-image-uploads";
import {
  useEditQueued,
  useSendThreadMessage,
  useStopThread,
} from "@/lib/hooks/use-threads";
import {
  READ_ONLY_HINT,
  createIdTracker,
  isThreadActive,
  sameAttachmentIds,
} from "@/lib/threads-compose";
import { ThreadComposer } from "./thread-composer";
import { ThreadQueuePanel, actionErrorText } from "./thread-queue";

interface EditingState {
  runId: string;
  original: ChatAttachment[];
  /** 进入编辑前输入框里的草稿，退出时放回去。 */
  draft: string;
}

/**
 * 详情页底部：队列 + 输入框 + Stop。所有写操作之后由 hooks 立刻重读详情；
 * 这里不乐观改状态，Stop 的「停止中」以 mutation 进行中或服务端 activity.stopping 为准。
 */
export function ThreadChat({
  detail,
  environmentId,
  threadId,
  canOperate,
  stale,
}: {
  detail: ThreadDetail;
  environmentId: string;
  threadId: string;
  canOperate: boolean;
  stale: boolean;
}) {
  const queue: ThreadQueue = detail.queue ?? {
    held: false,
    can_reorder: false,
    can_steer: false,
    items: [],
  };
  const activity: ThreadActivity = detail.activity ?? {
    stopping: false,
    has_pending_requests: false,
    pending_background_task_count: 0,
  };
  const active = isThreadActive(detail);
  const archived = detail.thread.status === "archived";

  const [text, setText] = useState("");
  const [editing, setEditing] = useState<EditingState | null>(null);
  const [tracker] = useState(createIdTracker);
  const uploads = useImageUploads(environmentId);
  const send = useSendThreadMessage(environmentId, threadId);
  const edit = useEditQueued(environmentId, threadId);
  const stop = useStopThread(environmentId, threadId);

  const editingItem = editing
    ? queue.items.find((i) => i.run_id === editing.runId)
    : undefined;

  const startEdit = (item: ThreadQueueItem) => {
    setEditing({
      runId: item.run_id,
      original: item.attachments ?? [],
      draft: editing ? editing.draft : text,
    });
    setText(item.text);
    uploads.parkAndShowExisting(item.attachments ?? []);
    edit.reset();
  };

  const exitEdit = () => {
    if (!editing) return;
    setText(editing.draft);
    uploads.restoreParked();
    setEditing(null);
    edit.reset();
  };

  const submit = async () => {
    const attachments = uploads.attachmentsOf();
    try {
      if (editing) {
        await edit.mutateAsync({
          runId: editing.runId,
          text,
          // 缺省不传 = 保留原附件；变了才整组替换（[] = 清空）。
          attachments: sameAttachmentIds(editing.original, attachments)
            ? undefined
            : attachments,
        });
        setText(editing.draft);
        uploads.restoreParked();
        setEditing(null);
        return;
      }
      const ids = tracker.idsFor(
        JSON.stringify([text, attachments.map((a) => a.id)]),
      );
      await send.mutateAsync({
        command_id: ids.command_id,
        message_id: ids.message_id,
        text,
        attachments,
      });
      tracker.reset();
      setText("");
      uploads.clear();
    } catch {
      // 错误由 mutation 状态展示；失败重试沿用同一组 ID。
    }
  };

  const stopping = stop.isPending || activity.stopping;
  const failure = editing ? edit.error : send.error || stop.error;
  const pending = editing ? edit.isPending : send.isPending;

  return (
    <div className="space-y-3">
      <ThreadQueuePanel
        environmentId={environmentId}
        threadId={threadId}
        queue={queue}
        canOperate={canOperate}
        stale={stale}
        editingRunId={editing?.runId}
        onEdit={startEdit}
      />
      {!canOperate ? (
        <p
          role="note"
          className="rounded-lg bg-muted/50 px-3 py-2 text-center text-sm text-muted-foreground"
        >
          {READ_ONLY_HINT}
        </p>
      ) : archived ? (
        <p className="rounded-lg bg-muted/50 px-3 py-2 text-center text-sm text-muted-foreground">
          线程已归档，无法发送消息
        </p>
      ) : (
        <ThreadComposer
          environmentId={environmentId}
          text={text}
          onTextChange={setText}
          uploads={uploads}
          submitLabel={editing ? "保存修改" : active ? "加入队列" : "发送"}
          submitting={pending}
          blocked={Boolean(editing) && (stale || !editingItem)}
          allowImageOnly
          disabled={pending}
          onSubmit={() => void submit()}
          error={
            (editing ? edit.isError : send.isError || stop.isError)
              ? actionErrorText(failure)
              : undefined
          }
          banner={
            editing && (
              <div className="flex items-center justify-between gap-2 rounded-lg bg-primary/5 px-3 py-1.5 text-xs">
                <span>
                  正在编辑排队消息
                  {!editingItem && "（该消息已开始发送或已被取消）"}
                </span>
                <Button type="button" size="xs" variant="ghost" onClick={exitEdit}>
                  退出编辑
                </Button>
              </div>
            )
          }
          actions={
            (active || activity.stopping) && (
              <Button
                type="button"
                size="sm"
                variant="outline"
                disabled={stopping || stale}
                onClick={() => stop.mutate()}
              >
                <Square className="mr-1.5 h-3.5 w-3.5" />
                {stopping ? "停止中…" : "停止"}
              </Button>
            )
          }
        />
      )}
    </div>
  );
}
