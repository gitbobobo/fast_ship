import { useEffect, useRef, useState, type ReactElement } from "react";
import {
  Check,
  CheckCircle2,
  Inbox,
  Lightbulb,
  ListChecks,
  Loader2,
  Pencil,
  Plus,
  RefreshCw,
  Sparkles,
  Trash2,
  Wand2,
  X,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { uuid } from "@/lib/uuid";
import { cn } from "@/lib/utils";
import { formatRelativeTime } from "@/lib/utils/format";
import { toast } from "sonner";

export type IssueChecklistDraftItem = {
  localId: string;
  id?: string;
  title: string;
  isCompleted: boolean;
};

type SuggestedChecklistDraftItem = {
  id: string;
  title: string;
  selected: boolean;
};

export type IssueChecklistControllerOptions = {
  /** 已持久化清单（issue.internal_meta.checklist 归一化后的形态） */
  items: IssueChecklistDraftItem[];
  updatedAt?: string | null;
  isSaving: boolean;
  /** 入参是归一化后的提交体；resolve true = 保存成功。
   *  successMessage 由调用方决定（不同入口文案不同），保存失败的 toast 也归调用方 */
  onSave: (
    items: { id?: string; title: string; is_completed: boolean }[],
    options?: { successMessage?: string },
  ) => Promise<boolean>;
  /** 调用 AI 清单建议；失败原样抛出（模块据 error.response.status===404 判「尚未配置 AI」） */
  requestSuggestions: () => Promise<{ items: { title: string }[] }>;
  /** 建议弹框里「前往 AI 配置」按钮的回调 */
  onConfigureAi: () => void;
};

export type IssueChecklistController = {
  // 展示态
  items: IssueChecklistDraftItem[];
  updatedAt?: string | null;
  isSaving: boolean;
  isEditing: boolean;
  draft: IssueChecklistDraftItem[];
  draftProgress: number | null;
  completedCount: number;
  isDirty: boolean;
  isSuggestionDialogOpen: boolean;
  suggestionState: "idle" | "loading" | "ready" | "missing-settings" | "error";
  suggestedItems: SuggestedChecklistDraftItem[];
  onConfigureAi: () => void;
  // 动作
  startEdit: () => void;
  cancelEdit: () => void;
  addItem: () => void;
  changeItem: (
    localId: string,
    updates: Partial<Pick<IssueChecklistDraftItem, "title" | "isCompleted">>,
  ) => void;
  removeItem: (localId: string) => void;
  save: () => Promise<void>;
  toggleCompleted: (localId: string) => Promise<void>;
  openSuggestions: () => Promise<void>;
  setSuggestionDialogOpen: (open: boolean) => void;
  toggleSuggestion: (id: string) => void;
  appendSuggestions: () => Promise<void>;
};

function normalizeChecklistDraft(items: IssueChecklistDraftItem[]) {
  return items.map((item) => ({
    id: item.id ?? "",
    title: item.title.trim(),
    is_completed: item.isCompleted,
  }));
}

function calculateChecklistProgress(items: Array<{ isCompleted: boolean }>) {
  if (items.length === 0) {
    return null;
  }
  const completed = items.filter((item) => item.isCompleted).length;
  return Math.round((completed * 100) / items.length);
}

/** 任务清单状态控制器：draft、编辑态与「清单建议」弹框状态都在这里创建。
 *  详情页桌面 aside 与移动 Sheet 各渲染一份卡片，但 controller 只在 route 调用一次，
 *  两份 UI 因此共享同一份未保存状态。持久化通过 onSave 委托给调用方，
 *  只根据返回的 boolean 决定退出编辑/关闭弹框/回滚。 */
export function useIssueChecklistController(
  options: IssueChecklistControllerOptions,
): IssueChecklistController {
  const { items, updatedAt, isSaving, onSave, requestSuggestions, onConfigureAi } =
    options;

  const [isEditing, setIsEditing] = useState(false);
  const [draft, setDraft] = useState<IssueChecklistDraftItem[]>([]);
  const [isSuggestionDialogOpen, setIsSuggestionDialogOpen] = useState(false);
  const [suggestionState, setSuggestionState] = useState<
    "idle" | "loading" | "ready" | "missing-settings" | "error"
  >("idle");
  const [suggestedItems, setSuggestedItems] = useState<
    SuggestedChecklistDraftItem[]
  >([]);
  const isEditingRef = useRef(false);

  const draftProgress = calculateChecklistProgress(draft);
  const completedCount = draft.filter((item) => item.isCompleted).length;
  const isDirty =
    JSON.stringify(normalizeChecklistDraft(draft)) !==
    JSON.stringify(normalizeChecklistDraft(items));

  useEffect(() => {
    isEditingRef.current = isEditing;
  }, [isEditing]);

  // 非编辑态才跟随持久化数据，避免 refetch 覆盖未保存的编辑
  useEffect(() => {
    if (isEditingRef.current) {
      return;
    }
    setDraft(items);
  }, [items]);

  const addItem = () => {
    setDraft((current) => [
      ...current,
      {
        localId: uuid(),
        title: "",
        isCompleted: false,
      },
    ]);
  };

  const changeItem = (
    localId: string,
    updates: Partial<Pick<IssueChecklistDraftItem, "title" | "isCompleted">>,
  ) => {
    setDraft((current) =>
      current.map((item) =>
        item.localId === localId ? { ...item, ...updates } : item,
      ),
    );
  };

  const removeItem = (localId: string) => {
    setDraft((current) => current.filter((item) => item.localId !== localId));
  };

  const persistDraft = async (
    nextDraft: IssueChecklistDraftItem[],
    persistOptions?: {
      successMessage?: string;
      rollbackDraft?: IssueChecklistDraftItem[];
      exitEditMode?: boolean;
    },
  ) => {
    const normalized = nextDraft.map((item) => ({
      id: item.id,
      title: item.title.trim(),
      is_completed: item.isCompleted,
    }));
    if (normalized.some((item) => !item.title)) {
      toast.error("请先填写所有任务清单标题");
      return false;
    }

    const success = await onSave(
      normalized,
      persistOptions?.successMessage
        ? { successMessage: persistOptions.successMessage }
        : undefined,
    );
    if (!success) {
      if (persistOptions?.rollbackDraft) {
        setDraft(persistOptions.rollbackDraft);
      }
      return false;
    }
    if (persistOptions?.exitEditMode) {
      setIsEditing(false);
    }
    return true;
  };

  const save = async () => {
    await persistDraft(draft, {
      successMessage: "任务清单已保存",
      exitEditMode: true,
    });
  };

  const toggleCompleted = async (localId: string) => {
    const rollbackDraft = draft;
    const nextDraft = draft.map((item) =>
      item.localId === localId ? { ...item, isCompleted: !item.isCompleted } : item,
    );
    setDraft(nextDraft);
    await persistDraft(nextDraft, { rollbackDraft });
  };

  const cancelEdit = () => {
    setDraft(items);
    setIsEditing(false);
  };

  const openSuggestions = async () => {
    setIsSuggestionDialogOpen(true);
    setSuggestionState("loading");
    setSuggestedItems([]);

    try {
      const result = await requestSuggestions();
      setSuggestedItems(
        result.items.map((item, index) => ({
          id: `${index}-${item.title}`,
          title: item.title,
          selected: true,
        })),
      );
      setSuggestionState("ready");
    } catch (error) {
      const status = (error as { response?: { status?: number } })?.response?.status;
      setSuggestionState(status === 404 ? "missing-settings" : "error");
    }
  };

  const toggleSuggestion = (id: string) => {
    setSuggestedItems((current) =>
      current.map((item) =>
        item.id === id ? { ...item, selected: !item.selected } : item,
      ),
    );
  };

  const appendSuggestions = async () => {
    const selectedItems = suggestedItems.filter((item) => item.selected);
    if (selectedItems.length === 0) {
      toast.error("请至少选择一项");
      return;
    }

    const rollbackDraft = draft;
    const nextDraft = [
      ...draft,
      ...selectedItems.map((item) => ({
        localId: uuid(),
        title: item.title,
        isCompleted: false,
      })),
    ];
    setDraft(nextDraft);

    const success = await persistDraft(nextDraft, {
      rollbackDraft,
      successMessage: "已补充到任务清单",
    });
    if (success) {
      setIsSuggestionDialogOpen(false);
    }
  };

  return {
    items,
    updatedAt,
    isSaving,
    isEditing,
    draft,
    draftProgress,
    completedCount,
    isDirty,
    isSuggestionDialogOpen,
    suggestionState,
    suggestedItems,
    onConfigureAi,
    startEdit: () => setIsEditing(true),
    cancelEdit,
    addItem,
    changeItem,
    removeItem,
    save,
    toggleCompleted,
    openSuggestions,
    setSuggestionDialogOpen: setIsSuggestionDialogOpen,
    toggleSuggestion,
    appendSuggestions,
  };
}

/** 清单卡片（只读/编辑两态、进度、增删改）：只从 controller 取状态与动作，
 *  自身不持有任何 draft/编辑态，可安全地在桌面与移动侧栏各渲染一份。 */
export function IssueChecklistCard({
  controller,
}: {
  controller: IssueChecklistController;
}): ReactElement {
  const {
    updatedAt,
    isSaving,
    isEditing,
    draft,
    draftProgress,
    completedCount,
    isDirty,
    startEdit,
    cancelEdit,
    addItem,
    changeItem,
    removeItem,
    save,
    toggleCompleted,
    openSuggestions,
  } = controller;

  return (
    <Card>
      <CardContent className="space-y-4 p-4">
        <div className="flex items-center justify-between">
          <h3 className="text-sm font-semibold">任务清单</h3>
          {isEditing ? (
            <div className="flex items-center gap-1">
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label="取消编辑"
                onClick={cancelEdit}
                disabled={isSaving}
              >
                <X className="h-3.5 w-3.5" />
              </Button>
              <Button
                size="icon-sm"
                aria-label="保存"
                title="保存任务清单"
                onClick={() => void save()}
                disabled={!isDirty || isSaving}
              >
                {isSaving ? (
                  <Loader2 className="h-3.5 w-3.5 animate-spin" />
                ) : (
                  <Check className="h-3.5 w-3.5" />
                )}
              </Button>
            </div>
          ) : (
            <div className="flex items-center gap-1">
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label="智能识别任务清单建议"
                onClick={() => void openSuggestions()}
              >
                <Sparkles className="h-3.5 w-3.5" />
              </Button>
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label="编辑"
                title="编辑任务清单"
                onClick={startEdit}
              >
                <Pencil className="h-3.5 w-3.5" />
              </Button>
            </div>
          )}
        </div>

        {draft.length > 0 && (
          <div className="space-y-2">
            <div className="flex items-center justify-between text-sm">
              <span className="font-semibold">{draftProgress ?? 0}%</span>
              <span className="text-xs text-muted-foreground">
                {completedCount}/{draft.length} 项完成
                {updatedAt && (
                  <span className="ml-1">· 更新于 {formatRelativeTime(updatedAt)}</span>
                )}
              </span>
            </div>
            <div className="h-2 overflow-hidden rounded-full bg-muted">
              <div
                className={cn(
                  "h-full rounded-full transition-all",
                  (draftProgress ?? 0) >= 100 ? "bg-emerald-500" : "bg-amber-500",
                )}
                style={{ width: `${draftProgress ?? 0}%` }}
              />
            </div>
          </div>
        )}

        <div className="space-y-2">
          {draft.length === 0 ? (
            <div className="text-sm text-muted-foreground">
              还没有任务清单。
            </div>
          ) : (
            draft.map((item, index) => (
              <div
                key={item.localId}
                className={cn(
                  "group flex items-center gap-3 rounded-lg border p-2.5 transition-colors",
                  isEditing ? "bg-card" : "bg-card hover:bg-muted/50",
                )}
              >
                {isEditing ? (
                  <>
                    <button
                      type="button"
                      onClick={() => changeItem(item.localId, { isCompleted: !item.isCompleted })}
                      className={cn(
                        "inline-flex h-5 w-5 shrink-0 items-center justify-center rounded border transition-colors",
                        item.isCompleted
                          ? "border-emerald-500 bg-emerald-500 text-white"
                          : "border-input bg-background text-transparent hover:border-muted-foreground",
                      )}
                    >
                      <CheckCircle2 className="h-3.5 w-3.5" />
                    </button>
                    <Input
                      value={item.title}
                      onChange={(event) =>
                        changeItem(item.localId, { title: event.target.value })
                      }
                      placeholder={`任务 ${index + 1}`}
                      className={cn("h-8 flex-1", item.isCompleted && "text-muted-foreground line-through")}
                    />
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-8 w-8 shrink-0"
                      aria-label={`删除任务清单 ${index + 1}`}
                      onClick={() => removeItem(item.localId)}
                    >
                      <Trash2 className="h-4 w-4 text-muted-foreground" />
                    </Button>
                  </>
                ) : (
                  <>
                    <button
                      type="button"
                      disabled={isSaving}
                      onClick={() => void toggleCompleted(item.localId)}
                      aria-label={
                        item.isCompleted
                          ? `取消完成任务清单 ${index + 1}`
                          : `标记完成任务清单 ${index + 1}`
                      }
                      className={cn(
                        "inline-flex h-5 w-5 shrink-0 cursor-pointer items-center justify-center rounded border transition-colors",
                        item.isCompleted
                          ? "border-emerald-500 bg-emerald-500 text-white"
                          : "border-input bg-background text-transparent hover:border-muted-foreground",
                      )}
                    >
                      <CheckCircle2 className="h-3.5 w-3.5" />
                    </button>
                    <p
                      className={cn(
                        "min-w-0 flex-1 text-sm",
                        item.isCompleted && "text-muted-foreground line-through",
                      )}
                    >
                      {item.title}
                    </p>
                  </>
                )}
              </div>
            ))
          )}

          {isEditing && (
            <Button variant="outline" className="w-full" onClick={addItem}>
              <Plus className="mr-1.5 h-3.5 w-3.5" />
              {draft.length === 0 ? "添加第一项" : "添加项"}
            </Button>
          )}
        </div>
      </CardContent>
    </Card>
  );
}

/** 清单建议弹框：状态在 controller，route 必须只渲染一次本组件
 *  （两个卡片实例各自渲染会导致两个弹框同时打开）。 */
export function IssueChecklistSuggestionsDialog({
  controller,
}: {
  controller: IssueChecklistController;
}): ReactElement {
  const {
    isSaving,
    draft,
    isSuggestionDialogOpen,
    suggestionState,
    suggestedItems,
    onConfigureAi,
    openSuggestions,
    setSuggestionDialogOpen,
    toggleSuggestion,
    appendSuggestions,
  } = controller;

  return (
    <Dialog open={isSuggestionDialogOpen} onOpenChange={setSuggestionDialogOpen}>
      <DialogContent className="flex w-[90vw] max-w-[calc(100%-2rem)] flex-col overflow-hidden p-0 max-h-[90vh] sm:max-w-[1100px]">
        <DialogHeader className="shrink-0 border-b bg-muted/30 px-6 pt-5 pb-4">
          <div className="flex items-center gap-3">
            <div className="flex h-9 w-9 items-center justify-center rounded-lg bg-primary/10 text-primary">
              <Wand2 className="h-4.5 w-4.5" />
            </div>
            <div className="flex-1">
              <DialogTitle className="text-base">清单建议</DialogTitle>
              <DialogDescription className="mt-0.5 text-xs">
                基于问题内容智能识别可补充的任务清单项
              </DialogDescription>
            </div>
          </div>
        </DialogHeader>

        <div className="grid flex-1 gap-0 overflow-hidden md:grid-cols-[3fr_2fr]">
          <div className="flex flex-col gap-3 overflow-hidden border-r px-6 py-5">
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2">
                <Lightbulb className="h-4 w-4 text-amber-500" />
                <span className="text-sm font-semibold">AI 建议</span>
                {suggestionState === "ready" && suggestedItems.length > 0 && (
                  <span className="inline-flex items-center rounded-full bg-primary/10 px-2 py-0.5 text-[11px] font-medium text-primary">
                    {suggestedItems.filter((i) => i.selected).length}/
                    {suggestedItems.length} 已选
                  </span>
                )}
              </div>
              <Button
                variant="ghost"
                size="sm"
                className="h-8 gap-1.5 text-xs"
                onClick={() => void openSuggestions()}
                disabled={suggestionState === "loading"}
              >
                {suggestionState === "loading" ? (
                  <>
                    <Loader2 className="h-3.5 w-3.5 animate-spin" />
                    识别中...
                  </>
                ) : (
                  <>
                    <RefreshCw className="h-3.5 w-3.5" />
                    重新识别
                  </>
                )}
              </Button>
            </div>

            <div className="flex-1 overflow-y-auto rounded-xl border bg-muted/20 p-3">
              {suggestionState === "loading" ? (
                <div className="space-y-3">
                  {[...Array(4)].map((_, i) => (
                    <div
                      key={i}
                      className="flex items-start gap-3 rounded-lg border border-dashed border-border/60 bg-background/50 p-3"
                    >
                      <Skeleton className="mt-0.5 h-5 w-5 shrink-0 rounded" />
                      <div className="flex-1 space-y-2">
                        <Skeleton className="h-4 w-3/4 rounded" />
                        <Skeleton className="h-3 w-1/4 rounded" />
                      </div>
                    </div>
                  ))}
                </div>
              ) : suggestionState === "missing-settings" ? (
                <div className="flex h-full min-h-[280px] flex-col items-center justify-center rounded-lg border border-dashed bg-background px-6 text-center">
                  <div className="flex h-12 w-12 items-center justify-center rounded-full bg-muted">
                    <Sparkles className="h-5 w-5 text-muted-foreground" />
                  </div>
                  <p className="mt-4 text-sm font-medium">尚未配置 AI</p>
                  <p className="mt-1 max-w-[240px] text-xs text-muted-foreground leading-relaxed">
                    先在设置中保存 API Host、模型和 API Key，再回来使用智能识别。
                  </p>
                  <Button
                    className="mt-5 h-8 text-xs"
                    size="sm"
                    onClick={onConfigureAi}
                  >
                    前往 AI 配置
                  </Button>
                </div>
              ) : suggestionState === "error" ? (
                <div className="flex h-full min-h-[280px] flex-col items-center justify-center rounded-lg border border-dashed bg-background px-6 text-center">
                  <div className="flex h-12 w-12 items-center justify-center rounded-full bg-destructive/10">
                    <RefreshCw className="h-5 w-5 text-destructive" />
                  </div>
                  <p className="mt-4 text-sm font-medium">识别失败</p>
                  <p className="mt-1 max-w-[240px] text-xs text-muted-foreground leading-relaxed">
                    当前无法获取 AI 建议，请稍后重试。
                  </p>
                </div>
              ) : suggestedItems.length === 0 ? (
                <div className="flex h-full min-h-[280px] flex-col items-center justify-center rounded-lg border border-dashed bg-background px-6 text-center">
                  <div className="flex h-12 w-12 items-center justify-center rounded-full bg-muted">
                    <Lightbulb className="h-5 w-5 text-muted-foreground" />
                  </div>
                  <p className="mt-4 text-sm font-medium">暂无建议项</p>
                  <p className="mt-1 max-w-[240px] text-xs text-muted-foreground leading-relaxed">
                    当前内容不足以生成建议，可以在补充评论后再试一次。
                  </p>
                </div>
              ) : (
                <div className="space-y-2">
                  {suggestedItems.map((item, index) => (
                    <button
                      key={item.id}
                      type="button"
                      onClick={() => toggleSuggestion(item.id)}
                      className={cn(
                        "group flex w-full items-start gap-3 rounded-lg border p-3 text-left transition-all duration-150",
                        item.selected
                          ? "border-primary/20 bg-background shadow-sm"
                          : "border-border/60 bg-background/60 opacity-70 hover:opacity-100",
                      )}
                    >
                      <span
                        className={cn(
                          "mt-0.5 inline-flex h-5 w-5 shrink-0 items-center justify-center rounded-md border transition-all duration-150",
                          item.selected
                            ? "border-emerald-500 bg-emerald-500 text-white shadow-sm"
                            : "border-input bg-background text-transparent group-hover:border-emerald-500/50",
                        )}
                      >
                        <CheckCircle2 className="h-3.5 w-3.5" />
                      </span>
                      <div className="min-w-0 flex-1">
                        <p className="text-sm font-medium leading-5">{item.title}</p>
                        <p className="mt-0.5 text-[11px] text-muted-foreground">
                          建议项 {index + 1}
                        </p>
                      </div>
                    </button>
                  ))}
                </div>
              )}
            </div>
          </div>

          <div className="flex flex-col gap-3 overflow-hidden bg-muted/10 px-6 py-5">
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2">
                <ListChecks className="h-4 w-4 text-emerald-500" />
                <span className="text-sm font-semibold">任务清单</span>
                {draft.length > 0 && (
                  <span className="inline-flex items-center rounded-full bg-emerald-500/10 px-2 py-0.5 text-[11px] font-medium text-emerald-600 dark:text-emerald-400">
                    {draft.filter((i) => i.isCompleted).length}/
                    {draft.length} 完成
                  </span>
                )}
              </div>
            </div>

            <div className="flex-1 overflow-y-auto rounded-xl border bg-muted/20 p-3">
              {draft.length === 0 ? (
                <div className="flex h-full min-h-[280px] flex-col items-center justify-center rounded-lg border border-dashed bg-background px-6 text-center">
                  <div className="flex h-12 w-12 items-center justify-center rounded-full bg-muted">
                    <Inbox className="h-5 w-5 text-muted-foreground" />
                  </div>
                  <p className="mt-4 text-sm font-medium">当前还没有任务清单项</p>
                  <p className="mt-1 max-w-[200px] text-xs text-muted-foreground leading-relaxed">
                    从左侧选择建议项，追加到这里。
                  </p>
                </div>
              ) : (
                <div className="space-y-2">
                  {draft.map((item, index) => (
                    <div
                      key={item.localId}
                      className="flex items-start gap-3 rounded-lg border bg-background p-3"
                    >
                      <span
                        className={cn(
                          "mt-0.5 inline-flex h-5 w-5 shrink-0 items-center justify-center rounded-md border",
                          item.isCompleted
                            ? "border-emerald-500 bg-emerald-500 text-white"
                            : "border-input bg-background text-transparent",
                        )}
                      >
                        <CheckCircle2 className="h-3.5 w-3.5" />
                      </span>
                      <div className="min-w-0 flex-1">
                        <p
                          className={cn(
                            "text-sm leading-5",
                            item.isCompleted && "text-muted-foreground line-through",
                          )}
                        >
                          {item.title || `任务 ${index + 1}`}
                        </p>
                      </div>
                    </div>
                  ))}
                </div>
              )}
            </div>
          </div>
        </div>

        <DialogFooter className="shrink-0 border-t bg-muted/30 px-6 py-4 sm:justify-between">
          <div className="hidden items-center gap-2 text-xs text-muted-foreground sm:flex">
            {suggestionState === "ready" && suggestedItems.length > 0 ? (
              <>
                <CheckCircle2 className="h-3.5 w-3.5 text-emerald-500" />
                已选择 {suggestedItems.filter((i) => i.selected).length} 项建议
              </>
            ) : suggestionState === "loading" ? null : (
              <>
                <Lightbulb className="h-3.5 w-3.5" />
                点击建议项即可选中或取消
              </>
            )}
          </div>
          <div className="flex flex-col-reverse gap-2 sm:flex-row sm:items-center">
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="h-8 text-xs"
              onClick={() => setSuggestionDialogOpen(false)}
            >
              关闭
            </Button>
            <Button
              type="button"
              size="sm"
              className="h-8 gap-1.5 text-xs"
              aria-label="追加到任务清单"
              onClick={() => void appendSuggestions()}
              disabled={
                suggestionState !== "ready" ||
                suggestedItems.every((item) => !item.selected) ||
                isSaving
              }
            >
              {isSaving && (
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
              )}
              {isSaving
                ? "追加中..."
                : `追加到任务清单 (${suggestedItems.filter((i) => i.selected).length})`}
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
