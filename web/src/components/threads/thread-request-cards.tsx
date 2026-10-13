import { useId, useRef, useState, type FormEvent } from "react";
import { Link } from "react-router";
import { CheckCircle2, CircleHelp, Lock, ShieldAlert, ShieldCheck } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ApiRequestError } from "@/lib/api/client";
import { threadApi, type ThreadItem } from "@/lib/api/threads";
import { useImageUploads } from "@/lib/hooks/use-image-uploads";
import { useRefreshThread, useRespondRequest } from "@/lib/hooks/use-threads";
import {
  READ_ONLY_HINT,
  approvalOptions,
  isRequestAnswerable,
  questionAnswer,
  type AnswerDraft,
} from "@/lib/threads-compose";
import { cn } from "@/lib/utils";
import { ImageStrip } from "./thread-images";
import { actionErrorText } from "./thread-queue";

interface CardProps {
  item: ThreadItem;
  environmentId: string;
  threadId: string;
  /** 环境 access === "operate"。 */
  canOperate: boolean;
  /** 详情刷新失败，目标可能已变：禁用作答。 */
  stale: boolean;
}

const CARD = "space-y-2.5 rounded-xl border bg-card px-4 py-3 text-sm";

const REQUEST_STATUS_LABELS: Record<string, string> = {
  pending: "等待作答",
  resolved: "已处理",
  expired: "已过期",
  cancelled: "已取消",
};

function SourceLink({
  item,
  environmentId,
}: {
  item: ThreadItem;
  environmentId: string;
}) {
  if (!item.source_thread_id) return <span>来自上游线程</span>;
  return (
    <Link
      to={`/threads/${encodeURIComponent(environmentId)}/${encodeURIComponent(item.source_thread_id)}`}
      className="text-primary underline-offset-2 hover:underline"
    >
      查看来源线程
    </Link>
  );
}

/** 不能作答时的原因：继承行指向来源线程，其余给出状态或权限提示。 */
function Unanswerable({
  item,
  environmentId,
  canOperate,
}: {
  item: ThreadItem;
  environmentId: string;
  canOperate: boolean;
}) {
  const pending = item.request_status === "pending";
  return (
    <p className="flex flex-wrap items-center gap-x-2 text-xs text-muted-foreground">
      {item.inherited ? (
        <>
          <span>继承自来源线程，仅展示，无法在此作答</span>
          <SourceLink item={item} environmentId={environmentId} />
        </>
      ) : !pending ? (
        <span>
          {REQUEST_STATUS_LABELS[item.request_status ?? ""] ??
            item.request_status ??
            ""}
        </span>
      ) : !canOperate ? (
        <span>{READ_ONLY_HINT}</span>
      ) : (
        <span>该请求当前无法在此作答</span>
      )}
    </p>
  );
}

export function ApprovalRequestCard(props: CardProps) {
  const { item, environmentId, threadId, canOperate, stale } = props;
  const respond = useRespondRequest(environmentId, threadId);
  const options = approvalOptions(item);
  const answerable = isRequestAnswerable(item) && canOperate;
  const chosen = options.find((o) => o.decision === item.decision);
  const prompt = item.prompt || item.text || item.summary;

  return (
    <section data-testid="thread-item-approval_request" className={CARD}>
      <h3 className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
        <ShieldAlert className="h-3.5 w-3.5" />
        审批请求
        {item.app_name ? ` · ${item.app_name}` : ""}
        {item.request_kind ? ` · ${item.request_kind}` : ""}
      </h3>
      {prompt && <p className="break-words whitespace-pre-wrap">{prompt}</p>}
      {item.decision && (
        <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <CheckCircle2 className="h-3.5 w-3.5" />
          已选择：{chosen?.label ?? item.decision}
        </p>
      )}
      {answerable ? (
        <div className="space-y-2">
          <div className="flex flex-wrap gap-2">
            {options.map((option) => (
              <Button
                key={option.decision}
                size="sm"
                variant={option.decision.startsWith("accept") ? "default" : "outline"}
                disabled={respond.isPending || stale || !item.request_id}
                title={option.warning}
                onClick={() =>
                  respond.mutate({
                    requestId: item.request_id!,
                    input: { type: "approval", decision: option.decision },
                  })
                }
              >
                {option.label}
              </Button>
            ))}
          </div>
          {options
            .filter((o) => o.warning)
            .map((o) => (
              <p
                key={o.decision}
                className="text-xs text-amber-700 dark:text-amber-400"
              >
                {o.label}：{o.warning}
              </p>
            ))}
          {respond.isError && (
            <p role="alert" className="text-xs text-destructive">
              {actionErrorText(respond.error)}
            </p>
          )}
        </div>
      ) : (
        !item.decision && (
          <Unanswerable
            item={item}
            environmentId={environmentId}
            canOperate={canOperate}
          />
        )
      )}
    </section>
  );
}

const emptyDraft: AnswerDraft = { picked: [], custom: "" };

export function UserInputRequestCard(props: CardProps) {
  const answerable = isRequestAnswerable(props.item) && props.canOperate;
  if (!answerable) return <UserInputReadOnly {...props} />;
  return <UserInputAnswerForm {...props} />;
}

function UserInputReadOnly({ item, environmentId, canOperate }: CardProps) {
  const questions = item.questions ?? [];
  const given = item.answers ?? {};
  return (
    <section data-testid="thread-item-user_input_request" className={CARD}>
      <UserInputHeading />
      {questions.map((q) => (
        <div key={q.id} className="space-y-0.5">
          <p className="break-words whitespace-pre-wrap">{q.question}</p>
          {given[q.id] !== undefined && (
            <p className="text-xs text-muted-foreground">
              回答：{[given[q.id]].flat().map(String).join("、")}
            </p>
          )}
        </div>
      ))}
      {questions.length === 0 && <p>{item.text || item.summary}</p>}
      <Unanswerable item={item} environmentId={environmentId} canOperate={canOperate} />
    </section>
  );
}

function UserInputHeading() {
  return (
    <h3 className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
      <CircleHelp className="h-3.5 w-3.5" />
      需要你的回答
    </h3>
  );
}

function UserInputAnswerForm({
  item,
  environmentId,
  threadId,
  stale,
}: CardProps) {
  const uploads = useImageUploads(environmentId);
  const respond = useRespondRequest(environmentId, threadId, uploads.clear);
  const questions = item.questions ?? [];
  const [drafts, setDrafts] = useState<Record<string, AnswerDraft>>(() =>
    Object.fromEntries(
      questions.map((q) => [q.id, { picked: [], custom: q.initial_answer ?? "" }]),
    ),
  );
  const draftOf = (id: string) => drafts[id] ?? emptyDraft;
  const patch = (id: string, next: Partial<AnswerDraft>) =>
    setDrafts((d) => ({ ...d, [id]: { ...(d[id] ?? emptyDraft), ...next } }));
  const fileInputs = useRef<Record<string, HTMLInputElement | null>>({});

  const answers: Record<string, string | string[]> = {};
  for (const q of questions) {
    const value = questionAnswer(Boolean(q.multi_select), draftOf(q.id));
    if (value !== undefined) answers[q.id] = value;
  }
  const missingRequired = questions.some(
    (q) => q.required === true && answers[q.id] === undefined,
  );
  const canSend =
    Object.keys(answers).length > 0 &&
    !missingRequired &&
    !uploads.uploading &&
    !respond.isPending &&
    !stale;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!canSend || !item.request_id) return;
    const byQuestion: Record<string, ReturnType<typeof uploads.attachmentsOf>> = {};
    for (const q of questions) {
      const files = uploads.attachmentsOf(q.id);
      if (files.length > 0) byQuestion[q.id] = files;
    }
    respond.mutate({
      requestId: item.request_id,
      input: {
        type: "user_input",
        answers,
        ...(Object.keys(byQuestion).length > 0
          ? { attachments_by_question_id: byQuestion }
          : {}),
      },
    });
  };

  return (
    <form
      onSubmit={submit}
      aria-label="回答提问"
      data-testid="thread-item-user_input_request"
      className={CARD}
    >
      <UserInputHeading />
      {questions.map((q) => {
        const draft = draftOf(q.id);
        const multi = Boolean(q.multi_select);
        const showCustom = q.allow_custom_answer || q.options.length === 0;
        const mine = uploads.images.filter((i) => i.tag === q.id);
        return (
          <fieldset key={q.id} className="space-y-1.5">
            <legend className="text-xs text-muted-foreground">{q.header}</legend>
            <p className="break-words whitespace-pre-wrap">
              {q.question}
              {q.required === true && <span className="text-destructive"> *</span>}
            </p>
            {q.options.map((option) => {
              const value = option.value ?? option.label;
              const checked = draft.picked.includes(value);
              return (
                <label
                  key={value}
                  className="flex cursor-pointer items-start gap-2 text-sm"
                >
                  <input
                    type={multi ? "checkbox" : "radio"}
                    name={`${item.id}:${q.id}`}
                    checked={checked}
                    className="mt-1"
                    onChange={() =>
                      multi
                        ? patch(q.id, {
                            picked: checked
                              ? draft.picked.filter((v) => v !== value)
                              : [...draft.picked, value],
                          })
                        : patch(q.id, { picked: [value], custom: "" })
                    }
                  />
                  <span>
                    {option.label}
                    {option.description && (
                      <span className="block text-xs text-muted-foreground">
                        {option.description}
                      </span>
                    )}
                  </span>
                </label>
              );
            })}
            {showCustom && (
              <Input
                value={draft.custom}
                aria-label={`${q.header || q.question} 自定义回答`}
                placeholder="输入你的回答"
                onChange={(e) =>
                  patch(q.id, {
                    custom: e.target.value,
                    ...(multi ? {} : { picked: [] }),
                  })
                }
              />
            )}
            <ImageStrip
              environmentId={environmentId}
              images={mine}
              onRemove={uploads.remove}
            />
            <input
              ref={(el) => {
                fileInputs.current[q.id] = el;
              }}
              type="file"
              accept="image/png,image/jpeg,image/gif,image/webp"
              multiple
              hidden
              aria-label={`为 ${q.header || q.question} 添加图片`}
              onChange={(e) => {
                uploads.add([...(e.target.files ?? [])], q.id);
                e.target.value = "";
              }}
            />
            <Button
              type="button"
              size="xs"
              variant="ghost"
              onClick={() => fileInputs.current[q.id]?.click()}
            >
              添加图片
            </Button>
          </fieldset>
        );
      })}
      {respond.isError && (
        <p role="alert" className="text-xs text-destructive">
          {actionErrorText(respond.error)}
        </p>
      )}
      <Button type="submit" size="sm" disabled={!canSend}>
        提交回答
      </Button>
    </form>
  );
}

export const SECRET_PRIVACY_NOTE =
  "密钥只会发送给运行该线程的 T3 环境，不会保存在 Fast Ship，也不会写入对话记录。";

const SECRET_STATUS_LABELS: Record<string, string> = {
  saved: "已保存",
  declined: "已拒绝",
  cancelled: "已取消",
};

/** 固定文案，不透传上游信息，更不会带出密钥值。 */
function secretFailure(error: unknown) {
  if (error instanceof ApiRequestError) {
    if (error.status === 403) return READ_ONLY_HINT;
    if (error.status === 404) return "该密钥请求已不存在";
    if (error.status === 409) return "该请求已被回答或代理已停止";
  }
  return "提交失败，请稍后重试";
}

export function SecretRequestCard(props: CardProps) {
  const { item } = props;
  const pending = item.secret_status === "pending";
  const label = item.label || item.summary || "密钥";

  if (!pending || item.inherited) {
    return (
      <section
        data-testid="thread-item-secret_request"
        className="flex flex-wrap items-center gap-x-2 rounded-xl border bg-card px-4 py-2.5 text-sm text-muted-foreground"
      >
        <Lock className="h-3.5 w-3.5" />
        <span>
          {label} ·{" "}
          {item.inherited
            ? "继承自来源线程，无法在此作答"
            : (SECRET_STATUS_LABELS[item.secret_status ?? ""] ??
              item.secret_status ??
              "")}
        </span>
        {item.inherited && (
          <SourceLink item={item} environmentId={props.environmentId} />
        )}
      </section>
    );
  }
  return <PendingSecretForm {...props} label={label} />;
}

/**
 * 密钥只存在于本组件的输入 state 和那一次请求里：不写 console、不弹 toast、
 * 不经 react-query（mutation 会缓存变量与错误），成功后立刻清空。
 * 卡片按线程 key 渲染，切线程即销毁。
 */
function PendingSecretForm({
  item,
  environmentId,
  threadId,
  canOperate,
  stale,
  label,
}: CardProps & { label: string }) {
  const inputId = useId();
  const refresh = useRefreshThread(environmentId, threadId);
  const [secret, setSecret] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");
  const inFlight = useRef(false);

  const send = async (
    reply: { type: "save"; secret: string } | { type: "decline" },
  ) => {
    if (inFlight.current) return;
    inFlight.current = true;
    setSubmitting(true);
    setError("");
    try {
      await threadApi.answerSecret(environmentId, threadId, item.id, reply);
      setSecret("");
    } catch (err) {
      setError(secretFailure(err));
    } finally {
      inFlight.current = false;
      setSubmitting(false);
      void refresh();
    }
  };

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (secret.trim()) void send({ type: "save", secret });
  };

  const disabled = submitting || stale || !canOperate;

  return (
    <form
      onSubmit={onSubmit}
      autoComplete="off"
      aria-label={`提供密钥 ${label}`}
      data-testid="thread-item-secret_request"
      className={cn(CARD, "space-y-3")}
    >
      <div className="space-y-1">
        <label htmlFor={inputId} className="text-sm font-medium">
          {label}
        </label>
        {item.reason?.trim() && (
          <p className="text-muted-foreground">{item.reason}</p>
        )}
      </div>
      <div className="flex items-center gap-2">
        <Input
          id={inputId}
          // 用遮蔽文本而非 password：浏览器会对 password 字段弹出保存提示。
          type="text"
          className="[-webkit-text-security:disc]"
          autoComplete="off"
          autoCorrect="off"
          autoCapitalize="off"
          spellCheck={false}
          data-1p-ignore
          data-lpignore="true"
          data-bwignore
          placeholder={item.placeholder || "粘贴密钥"}
          value={secret}
          disabled={disabled}
          onChange={(e) => setSecret(e.target.value)}
        />
        <Button type="submit" size="sm" disabled={disabled || !secret.trim()}>
          安全保存
        </Button>
      </div>
      {!canOperate && (
        <p className="text-xs text-muted-foreground">{READ_ONLY_HINT}</p>
      )}
      {error && (
        <p role="alert" className="text-xs text-destructive">
          {error}
        </p>
      )}
      <div className="flex items-center justify-between gap-2">
        <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <ShieldCheck className="h-3.5 w-3.5 shrink-0" />
          {SECRET_PRIVACY_NOTE}
        </p>
        <Button
          type="button"
          size="xs"
          variant="ghost"
          disabled={disabled}
          onClick={() => void send({ type: "decline" })}
        >
          拒绝
        </Button>
      </div>
    </form>
  );
}
