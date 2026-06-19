import { useState } from "react";
import {
  Check,
  GitCommit,
  HelpCircle,
  Lightbulb,
  Loader2,
  Pencil,
  Plus,
  Sparkles,
  Trash2,
  X,
} from "lucide-react";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Skeleton } from "@/components/ui/skeleton";
import { GitHubContent } from "@/components/github-content";
import {
  useAnswerCollabQuestion,
  useCreateCollabNote,
  useDeleteCollabNote,
  useIssueCollab,
  useUpdateCollabNote,
} from "@/lib/hooks/use-issue-collab";
import { getInitials } from "@/lib/utils";
import { formatRelativeTime } from "@/lib/utils/format";
import { toast } from "sonner";

const CUSTOM_VALUE = "__collab_custom__";

interface CollaborationAreaProps {
  issueId: string;
  project?: Project | null;
}

function buildCommitUrl(project: Project | null | undefined, sha: string): string | null {
  if (project?.github_owner && project?.github_repo) {
    return `https://github.com/${project.github_owner}/${project.github_repo}/commit/${sha}`;
  }
  return null;
}

function CollabActorBadge({ actor }: { actor: IssueCollabActor }) {
  if (actor.kind === "agent") {
    return (
      <span className="inline-flex items-center gap-1 rounded-full border border-violet-500/20 bg-violet-500/10 px-2 py-0.5 text-xs font-medium text-violet-600 dark:text-violet-400">
        <Sparkles className="h-3 w-3" />
        {actor.login}
      </span>
    );
  }
  return (
    <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
      <Avatar size="sm">
        {actor.avatar_url ? <AvatarImage src={actor.avatar_url} alt={actor.login} /> : null}
        <AvatarFallback>{getInitials(actor.login)}</AvatarFallback>
      </Avatar>
      <span className="font-medium text-foreground">@{actor.login}</span>
    </span>
  );
}

function BackgroundSection({ issueId }: { issueId: string }) {
  const { data } = useIssueCollab(issueId);
  const createNote = useCreateCollabNote(issueId);
  const updateNote = useUpdateCollabNote(issueId);
  const deleteNote = useDeleteCollabNote(issueId);
  const [draft, setDraft] = useState("");
  const [editingId, setEditingId] = useState<string | null>(null);
  const [editDraft, setEditDraft] = useState("");

  const notes = data?.notes ?? [];

  const handleCreate = async () => {
    const body = draft.trim();
    if (!body) {
      toast.error("请输入背景信息");
      return;
    }
    try {
      await createNote.mutateAsync(body);
      setDraft("");
      toast.success("已补充背景信息");
    } catch {
      toast.error("补充失败");
    }
  };

  const handleStartEdit = (note: IssueCollabNote) => {
    setEditingId(note.id);
    setEditDraft(note.body);
  };

  const handleSaveEdit = async (noteId: string) => {
    const body = editDraft.trim();
    if (!body) {
      toast.error("请输入背景信息");
      return;
    }
    try {
      await updateNote.mutateAsync({ noteId, body });
      setEditingId(null);
      toast.success("已更新");
    } catch {
      toast.error("更新失败");
    }
  };

  const handleDelete = async (noteId: string) => {
    try {
      await deleteNote.mutateAsync(noteId);
      toast.success("已删除");
    } catch {
      toast.error("删除失败");
    }
  };

  return (
    <div className="space-y-3">
      <div className="flex items-center gap-2 text-sm font-semibold">
        <Lightbulb className="h-4 w-4 text-amber-500" />
        背景信息
      </div>

      {notes.length === 0 ? (
        <p className="text-sm text-muted-foreground">暂无背景，可补充供代理参考的上下文。</p>
      ) : (
        <div className="space-y-2">
          {notes.map((note) => (
            <div key={note.id} className="rounded-lg border bg-card p-3">
              <div className="mb-1.5 flex items-center justify-between gap-2">
                <CollabActorBadge actor={note.author} />
                <div className="flex items-center gap-1">
                  {editingId === note.id ? (
                    <>
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        aria-label="取消"
                        onClick={() => setEditingId(null)}
                        disabled={updateNote.isPending}
                      >
                        <X className="h-3.5 w-3.5" />
                      </Button>
                      <Button
                        size="icon-sm"
                        aria-label="保存"
                        onClick={() => void handleSaveEdit(note.id)}
                        disabled={updateNote.isPending}
                      >
                        {updateNote.isPending ? (
                          <Loader2 className="h-3.5 w-3.5 animate-spin" />
                        ) : (
                          <Check className="h-3.5 w-3.5" />
                        )}
                      </Button>
                    </>
                  ) : (
                    <>
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        aria-label="编辑背景"
                        onClick={() => handleStartEdit(note)}
                      >
                        <Pencil className="h-3.5 w-3.5" />
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        aria-label="删除背景"
                        onClick={() => void handleDelete(note.id)}
                        disabled={deleteNote.isPending}
                      >
                        <Trash2 className="h-3.5 w-3.5 text-muted-foreground" />
                      </Button>
                    </>
                  )}
                </div>
              </div>
              {editingId === note.id ? (
                <Textarea
                  value={editDraft}
                  onChange={(e) => setEditDraft(e.target.value)}
                  rows={3}
                  autoFocus
                />
              ) : (
                <p className="whitespace-pre-wrap break-words text-sm">{note.body}</p>
              )}
              <p className="mt-1.5 text-xs text-muted-foreground">
                {formatRelativeTime(note.created_at)}
                {note.updated_at !== note.created_at ? " · 已编辑" : ""}
              </p>
            </div>
          ))}
        </div>
      )}

      <Textarea
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        rows={2}
        placeholder="补充背景信息（代理处理时会参考）"
      />
      <div className="flex justify-end">
        <Button size="sm" onClick={() => void handleCreate()} disabled={createNote.isPending}>
          {createNote.isPending ? (
            <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
          ) : (
            <Plus className="mr-1.5 h-3.5 w-3.5" />
          )}
          补充背景
        </Button>
      </div>
    </div>
  );
}

function QuestionItem({ issueId, question }: { issueId: string; question: IssueCollabQuestion }) {
  const answerQuestion = useAnswerCollabQuestion(issueId);
  const [editing, setEditing] = useState(question.answer === null);
  const currentAnswer = question.answer?.value ?? "";
  const isCustomAnswer =
    currentAnswer !== "" && !question.options.includes(currentAnswer);

  const [selected, setSelected] = useState<string>(
    question.answer
      ? isCustomAnswer
        ? CUSTOM_VALUE
        : currentAnswer
      : "",
  );
  const [customText, setCustomText] = useState(isCustomAnswer ? currentAnswer : "");

  const handleStartEdit = () => {
    setSelected(isCustomAnswer ? CUSTOM_VALUE : currentAnswer);
    setCustomText(isCustomAnswer ? currentAnswer : "");
    setEditing(true);
  };

  const canSubmit =
    selected !== "" && (selected !== CUSTOM_VALUE || customText.trim() !== "");

  const handleSubmit = async () => {
    let answer = "";
    if (selected === CUSTOM_VALUE) {
      answer = customText.trim();
    } else if (selected !== "") {
      answer = selected;
    }
    if (!answer) {
      toast.error("请选择或输入答案");
      return;
    }
    try {
      await answerQuestion.mutateAsync({ questionId: question.id, answer });
      setEditing(false);
      toast.success("已提交回答");
    } catch {
      toast.error("提交失败");
    }
  };

  return (
    <div className="rounded-lg border bg-card p-3">
      <div className="mb-2 flex items-start justify-between gap-2">
        <p className="text-sm font-medium">{question.body}</p>
        <CollabActorBadge actor={question.author} />
      </div>

      {editing ? (
        <div className="space-y-2">
          {question.options.length > 0 ? (
            <RadioGroup value={selected} onValueChange={(value) => setSelected((value as string) ?? "")}>
              {question.options.map((option) => (
                <RadioGroupItem key={option} value={option}>
                  {option}
                </RadioGroupItem>
              ))}
              <RadioGroupItem value={CUSTOM_VALUE}>
                <span className="text-muted-foreground">其他（自填）</span>
              </RadioGroupItem>
            </RadioGroup>
          ) : null}
          {(selected === CUSTOM_VALUE || question.options.length === 0) && (
            <Input
              value={customText}
              onChange={(e) => {
                setCustomText(e.target.value);
                setSelected(CUSTOM_VALUE);
              }}
              placeholder="输入你的答案"
              autoFocus={question.options.length === 0}
            />
          )}
          <div className="flex justify-end gap-2">
            {question.answer && (
              <Button variant="ghost" size="sm" onClick={() => setEditing(false)}>
                取消
              </Button>
            )}
            <Button
              size="sm"
              onClick={() => void handleSubmit()}
              disabled={!canSubmit || answerQuestion.isPending}
            >
              {answerQuestion.isPending ? (
                <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
              ) : null}
              提交回答
            </Button>
          </div>
        </div>
      ) : (
        <div className="flex items-center justify-between gap-2 rounded-md bg-muted/40 px-3 py-2">
          <div className="min-w-0 text-sm">
            <span className="text-muted-foreground">回答：</span>
            <span className="font-medium">{currentAnswer}</span>
            {question.answer && (
              <span className="ml-2 text-xs text-muted-foreground">
                · {question.answer.author.login} · {formatRelativeTime(question.answer.answered_at)}
              </span>
            )}
          </div>
          <Button variant="ghost" size="sm" onClick={handleStartEdit}>
            <Pencil className="mr-1.5 h-3.5 w-3.5" />
            改
          </Button>
        </div>
      )}
    </div>
  );
}

function SummarySection({
  project,
  summary,
}: {
  project: Project | null | undefined;
  summary: IssueCollabSummary;
}) {
  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-2 text-sm font-semibold">
          <Check className="h-4 w-4 text-emerald-500" />
          完成总结
        </div>
        <CollabActorBadge actor={summary.author} />
      </div>
      <div className="rounded-lg border bg-card p-3">
        <div className="markdown-body text-sm">
          <GitHubContent markdown={summary.body} />
        </div>
        {summary.commit_ids.length > 0 && (
          <div className="mt-3 flex flex-wrap items-center gap-2 border-t pt-3">
            <span className="text-xs text-muted-foreground">提交：</span>
            {summary.commit_ids.map((sha) => {
              const url = buildCommitUrl(project, sha);
              return url ? (
                <a
                  key={sha}
                  href={url}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="inline-flex items-center gap-1 rounded-full border bg-muted/40 px-2 py-0.5 font-mono text-xs hover:bg-muted"
                >
                  <GitCommit className="h-3 w-3" />
                  {sha.slice(0, 7)}
                </a>
              ) : (
                <span
                  key={sha}
                  className="inline-flex items-center gap-1 rounded-full border bg-muted/40 px-2 py-0.5 font-mono text-xs"
                >
                  <GitCommit className="h-3 w-3" />
                  {sha.slice(0, 7)}
                </span>
              );
            })}
          </div>
        )}
        <p className="mt-2 text-xs text-muted-foreground">
          更新于 {formatRelativeTime(summary.updated_at)}
        </p>
      </div>
    </div>
  );
}

export function CollaborationArea({ issueId, project }: CollaborationAreaProps) {
  const { data, isLoading } = useIssueCollab(issueId);
  const questions = data?.questions ?? [];
  const summary = data?.summary ?? null;

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-2">
        <HelpCircle className="h-4 w-4 text-primary" />
        <h2 className="text-sm font-semibold">人机协作区</h2>
      </div>

      {isLoading ? (
        <div className="space-y-3">
          <Skeleton className="h-20 rounded-lg" />
          <Skeleton className="h-20 rounded-lg" />
        </div>
      ) : (
        <>
          <BackgroundSection issueId={issueId} />

          {questions.length > 0 && (
            <div className="space-y-3">
              <div className="flex items-center gap-2 text-sm font-semibold">
                <HelpCircle className="h-4 w-4 text-primary" />
                问题（{questions.length}）
              </div>
              <div className="space-y-2">
                {questions.map((question) => (
                  <QuestionItem key={question.id} issueId={issueId} question={question} />
                ))}
              </div>
            </div>
          )}

          {summary && <SummarySection project={project} summary={summary} />}
        </>
      )}
    </div>
  );
}
