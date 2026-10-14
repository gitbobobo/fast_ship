import { useState, type ReactNode } from "react";
import { Link } from "react-router";
import {
  AlertCircle,
  Bot,
  Brain,
  ChevronRight,
  FileDiff,
  GitFork,
  Terminal,
  Wrench,
} from "lucide-react";
import type { ThreadItem, ThreadItemType } from "@/lib/api/threads";
import { itemStatusLabel } from "@/lib/threads";
import { Button } from "@/components/ui/button";
import { useThreadItemDetail } from "@/lib/hooks/use-threads";
import { cn } from "@/lib/utils";
import { AttachmentThumbs } from "./thread-images";
import { ThreadMarkdown } from "./thread-markdown";
import {
  ApprovalRequestCard,
  SecretRequestCard,
  UserInputRequestCard,
} from "./thread-request-cards";

export const COLLAPSED_META: Partial<
  Record<ThreadItemType, { label: string; icon: typeof Terminal }>
> = {
  reasoning: { label: "推理", icon: Brain },
  command_execution: { label: "命令", icon: Terminal },
  dynamic_tool: { label: "工具", icon: Wrench },
  file_change: { label: "文件变更", icon: FileDiff },
  subagent: { label: "子代理", icon: GitFork },
};

/** 工具调用正文多为单行 JSON，能解析就缩进成多行，读不了的按原文显示。 */
function prettyJson(text: string) {
  try {
    return JSON.stringify(JSON.parse(text), null, 2);
  } catch {
    return text;
  }
}

function ItemBody({ item, text }: { item: ThreadItem; text: string }) {
  if (item.type === "reasoning" || item.type === "subagent") {
    return text ? <ThreadMarkdown text={text} /> : null;
  }
  return (
    <pre className="max-h-96 overflow-auto font-mono text-xs break-words whitespace-pre-wrap">
      {item.type === "dynamic_tool" ? prettyJson(text) : text}
    </pre>
  );
}

/** 展开后读取完整正文：加载中、失败可重试，运行中更新时先保留旧内容。 */
function LoadedItemBody({
  item,
  environmentId,
  threadId,
  revision,
}: {
  item: ThreadItem;
  environmentId: string;
  threadId: string;
  revision: string;
}) {
  const { data, isPending, isError, isFetching, refetch } = useThreadItemDetail(
    environmentId,
    threadId,
    item.id,
    revision,
    true,
  );

  if (data) {
    return (
      <>
        {isError && (
          <p role="alert" className="flex items-center gap-2 text-xs text-destructive">
            更新失败，显示的可能不是最新内容
            <Button variant="outline" size="xs" onClick={() => refetch()} disabled={isFetching}>
              重试
            </Button>
          </p>
        )}
        <ItemBody item={item} text={data.text} />
      </>
    );
  }
  if (isError) {
    return (
      <p role="alert" className="flex items-center gap-2 text-xs text-destructive">
        详情加载失败
        <Button variant="outline" size="xs" onClick={() => refetch()} disabled={isFetching}>
          重试
        </Button>
      </p>
    );
  }
  return isPending ? (
    <p role="status" className="text-xs text-muted-foreground">
      加载中…
    </p>
  ) : null;
}

/** 折叠项：默认只显示一行摘要，展开后才渲染正文。 */
export function CollapsedItem({
  item,
  environmentId,
  threadId,
  revision,
}: {
  item: ThreadItem;
  environmentId: string;
  threadId: string;
  revision: string;
}) {
  const [open, setOpen] = useState(false);
  const meta = COLLAPSED_META[item.type]!;
  const Icon = meta.icon;
  const summary = item.summary || meta.label;

  return (
    <details
      data-testid={`thread-item-${item.type}`}
      open={open}
      onToggle={(e) => setOpen(e.currentTarget.open)}
      className="group rounded-lg bg-muted/40 text-sm"
    >
      <summary className="flex cursor-pointer list-none items-center gap-2 px-3 py-2 text-muted-foreground select-none [&::-webkit-details-marker]:hidden">
        <ChevronRight className="h-3.5 w-3.5 shrink-0 transition-transform group-open:rotate-90" />
        <Icon className="h-3.5 w-3.5 shrink-0" />
        <span className="min-w-0 flex-1 truncate">{summary}</span>
        {item.status && (
          <span className="shrink-0 text-xs">{itemStatusLabel(item.status)}</span>
        )}
      </summary>
      {open && (
        <div className="space-y-2 border-t border-border/60 px-3 py-2">
          {item.detail_available ? (
            <LoadedItemBody
              item={item}
              environmentId={environmentId}
              threadId={threadId}
              revision={`${revision}|${item.status ?? ""}|${item.summary}`}
            />
          ) : (
            <ItemBody item={item} text={item.text} />
          )}
          {item.type === "subagent" && item.child_thread_id && (
            <Link
              to={`/threads/${encodeURIComponent(environmentId)}/${encodeURIComponent(item.child_thread_id)}`}
              className="inline-flex items-center gap-1 text-xs text-primary underline-offset-2 hover:underline"
            >
              查看子线程
            </Link>
          )}
        </div>
      )}
    </details>
  );
}

function Bubble({
  label,
  icon,
  className,
  children,
}: {
  label: string;
  icon?: ReactNode;
  className?: string;
  children: ReactNode;
}) {
  return (
    <section className={cn("min-w-0 space-y-1.5", className)}>
      <h3 className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
        {icon}
        {label}
      </h3>
      {children}
    </section>
  );
}

/** revision 标识该条目的版本（条目自身更新时间，缺失时为线程更新时间），变化后已展开的条目会重新读取正文。 */
export function ThreadItemView({
  item,
  environmentId,
  threadId,
  revision,
  canOperate = false,
  stale = false,
}: {
  item: ThreadItem;
  environmentId: string;
  threadId: string;
  revision: string;
  /** 环境 access === "operate" 时审批/提问/密钥卡片才可作答。 */
  canOperate?: boolean;
  /** 详情刷新失败：交互卡片禁用，避免对可能已变化的目标下命令。 */
  stale?: boolean;
}) {
  switch (item.type) {
    case "user_message":
      return (
        <Bubble label="你" className="sm:pl-12" >
          <div
            data-testid="thread-item-user_message"
            className="rounded-xl bg-primary/5 px-4 py-3 ring-1 ring-primary/10"
          >
            <ThreadMarkdown text={item.text} />
            <AttachmentThumbs
              environmentId={environmentId}
              attachments={item.attachments}
              className="mt-2"
            />
          </div>
        </Bubble>
      );
    case "approval_request":
    case "user_input_request":
    case "secret_request": {
      const Card = {
        approval_request: ApprovalRequestCard,
        user_input_request: UserInputRequestCard,
        secret_request: SecretRequestCard,
      }[item.type];
      return (
        <Card
          item={item}
          environmentId={environmentId}
          threadId={threadId}
          canOperate={canOperate}
          stale={stale}
        />
      );
    }
    case "assistant_message":
      return (
        <Bubble label="助手" icon={<Bot className="h-3.5 w-3.5" />}>
          <div data-testid="thread-item-assistant_message" className="px-1">
            <ThreadMarkdown text={item.text} />
          </div>
        </Bubble>
      );
    case "error":
      return (
        <div
          role="alert"
          data-testid="thread-item-error"
          className="flex items-start gap-2 rounded-lg bg-destructive/10 px-3 py-2 text-sm text-destructive"
        >
          <AlertCircle className="mt-0.5 h-4 w-4 shrink-0" />
          <span className="min-w-0 break-words whitespace-pre-wrap">
            {item.text || item.summary}
          </span>
        </div>
      );
    default:
      return COLLAPSED_META[item.type] ? (
        <CollapsedItem
          item={item}
          environmentId={environmentId}
          threadId={threadId}
          revision={revision}
        />
      ) : null;
  }
}
