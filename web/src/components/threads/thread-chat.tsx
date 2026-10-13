import { useState, type ReactNode } from "react";
import { Square } from "lucide-react";
import { Button } from "@/components/ui/button";
import type {
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

/**
 * 详情页底部：队列 + 输入框 + Stop。草稿和排队编辑各持有一套上传，
 * 生命周期跟组件挂载走：切编辑目标或退出未保存即卸载，未引用的附件会被删掉。
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

  const [editingRunId, setEditingRunId] = useState<string | null>(null);
  const [savingEdit, setSavingEdit] = useState(false);
  const [heldEditingItem, setHeldEditingItem] = useState<ThreadQueueItem>();
  const edit = useEditQueued(environmentId, threadId);
  const stop = useStopThread(environmentId, threadId);
  const stopping = stop.isPending || activity.stopping;
  const liveEditingItem = editingRunId
    ? queue.items.find((i) => i.run_id === editingRunId)
    : undefined;
  // 保存中详情可能先把该 run 移出队列；仍挂着编辑器，好让 clear() 先于卸载执行。
  const editingItem =
    liveEditingItem ?? (savingEdit ? heldEditingItem : undefined);

  const startEdit = (item: ThreadQueueItem) => {
    if (edit.isPending || savingEdit) return;
    if (editingRunId === item.run_id) return;
    setSavingEdit(false);
    setHeldEditingItem(item);
    setEditingRunId(item.run_id);
    edit.reset();
  };

  const exitEdit = () => {
    setSavingEdit(false);
    setHeldEditingItem(undefined);
    setEditingRunId(null);
    edit.reset();
  };

  const stopButton =
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
    );

  return (
    <div className="space-y-3">
      <ThreadQueuePanel
        environmentId={environmentId}
        threadId={threadId}
        queue={queue}
        canOperate={canOperate}
        stale={stale}
        editingRunId={editingRunId ?? undefined}
        actionLocked={edit.isPending}
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
        <>
          <DraftSession
            hidden={Boolean(editingRunId)}
            environmentId={environmentId}
            threadId={threadId}
            active={active}
            stopError={stop.error}
            stopIsError={stop.isError}
            actions={stopButton}
          />
          {editingItem && (
            <QueuedEditComposer
              key={editingItem.run_id}
              item={editingItem}
              environmentId={environmentId}
              stale={stale}
              edit={edit}
              stopError={stop.error}
              stopIsError={stop.isError}
              actions={stopButton}
              onSaving={setSavingEdit}
              onExit={exitEdit}
            />
          )}
          {editingRunId && !editingItem && (
            <div className="space-y-2">
              <p className="rounded-lg bg-muted/50 px-3 py-2 text-center text-sm text-muted-foreground">
                该排队消息已开始发送或已被取消
                <Button
                  type="button"
                  size="xs"
                  variant="ghost"
                  className="ml-2"
                  onClick={exitEdit}
                >
                  退出编辑
                </Button>
              </p>
              {stopButton}
            </div>
          )}
        </>
      )}
    </div>
  );
}

function DraftSession({
  hidden,
  environmentId,
  threadId,
  active,
  stopError,
  stopIsError,
  actions,
}: {
  hidden: boolean;
  environmentId: string;
  threadId: string;
  active: boolean;
  stopError: unknown;
  stopIsError: boolean;
  actions: ReactNode;
}) {
  const [text, setText] = useState("");
  const [tracker] = useState(createIdTracker);
  const uploads = useImageUploads(environmentId);
  const send = useSendThreadMessage(environmentId, threadId);

  const submit = async () => {
    const attachments = uploads.attachmentsOf();
    const ids = tracker.idsFor(
      JSON.stringify([text, attachments.map((a) => a.id)]),
    );
    try {
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

  if (hidden) return null;

  return (
    <ThreadComposer
      environmentId={environmentId}
      text={text}
      onTextChange={setText}
      uploads={uploads}
      submitLabel={active ? "加入队列" : "发送"}
      submitting={send.isPending}
      allowImageOnly
      disabled={send.isPending}
      onSubmit={() => void submit()}
      error={
        send.isError || stopIsError
          ? actionErrorText(send.error || stopError)
          : undefined
      }
      actions={actions}
    />
  );
}

function QueuedEditComposer({
  item,
  environmentId,
  stale,
  edit,
  stopError,
  stopIsError,
  actions,
  onSaving,
  onExit,
}: {
  item: ThreadQueueItem;
  environmentId: string;
  stale: boolean;
  edit: ReturnType<typeof useEditQueued>;
  stopError: unknown;
  stopIsError: boolean;
  actions: ReactNode;
  onSaving: (saving: boolean) => void;
  onExit: () => void;
}) {
  const [text, setText] = useState(item.text);
  const [original] = useState(item.attachments ?? []);
  const uploads = useImageUploads(environmentId, original);

  const submit = async () => {
    const attachments = uploads.attachmentsOf();
    onSaving(true);
    try {
      await edit.mutateAsync({
        runId: item.run_id,
        text,
        attachments: sameAttachmentIds(original, attachments)
          ? undefined
          : attachments,
      });
      uploads.clear();
      onExit();
    } catch {
      onSaving(false);
    }
  };

  return (
    <ThreadComposer
      environmentId={environmentId}
      text={text}
      onTextChange={setText}
      uploads={uploads}
      submitLabel="保存修改"
      submitting={edit.isPending}
      blocked={stale}
      allowImageOnly
      disabled={edit.isPending}
      onSubmit={() => void submit()}
      error={
        edit.isError || stopIsError
          ? actionErrorText(edit.error || stopError)
          : undefined
      }
      banner={
        <div className="flex items-center justify-between gap-2 rounded-lg bg-primary/5 px-3 py-1.5 text-xs">
          <span>正在编辑排队消息</span>
          <Button
            type="button"
            size="xs"
            variant="ghost"
            disabled={edit.isPending}
            onClick={onExit}
          >
            退出编辑
          </Button>
        </div>
      }
      actions={actions}
    />
  );
}
