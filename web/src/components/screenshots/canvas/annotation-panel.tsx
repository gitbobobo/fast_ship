import { useDeferredValue, useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import {
  Check,
  ChevronDown,
  Crosshair,
  MessageSquareText,
  Pencil,
  RotateCcw,
  Trash2,
  X,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { screenshotAnnotationApi } from "@/lib/api/screenshot-annotations";
import { useIssues } from "@/lib/hooks/use-issues";
import {
  useDeleteScreenshotAnnotation,
  useUpdateScreenshotAnnotation,
} from "@/lib/hooks/use-screenshot-annotations";
import type { AnnotationStatusFilter } from "@/lib/screenshot-canvas";
import { cn } from "@/lib/utils";
import { formatRelativeTime } from "@/lib/utils/format";
import { ANNOTATION_BODY_MAX } from "./annotation-draft";

interface AnnotationPanelProps {
  projectId: string;
  /** 当前画布范围内的全部标注（未按状态过滤） */
  annotations: ScreenshotAnnotation[];
  statusFilter: AnnotationStatusFilter;
  onStatusFilterChange: (filter: AnnotationStatusFilter) => void;
  selectedId: string | null;
  hoverId: string | null;
  onHover: (annotationId: string | null) => void;
  onSelect: (annotationId: string) => void;
  onLocate: (annotation: ScreenshotAnnotation) => void;
}

const EMPTY_TEXT: Record<AnnotationStatusFilter, string> = {
  open: "暂无未解决的标注",
  resolved: "暂无已解决的标注",
  all: "暂无标注",
};

export function AnnotationPanel({
  projectId,
  annotations,
  statusFilter,
  onStatusFilterChange,
  selectedId,
  hoverId,
  onHover,
  onSelect,
  onLocate,
}: AnnotationPanelProps) {
  const listRef = useRef<HTMLUListElement>(null);
  const deleteMutation = useDeleteScreenshotAnnotation(projectId);
  const [pendingDelete, setPendingDelete] =
    useState<ScreenshotAnnotation | null>(null);

  const shown =
    statusFilter === "all"
      ? annotations
      : annotations.filter((a) => a.status === statusFilter);

  // 画布上点选标注后，把面板里对应条目滚入视野
  useEffect(() => {
    if (!selectedId) return;
    listRef.current
      ?.querySelector(`[data-annotation-id="${CSS.escape(selectedId)}"]`)
      ?.scrollIntoView({ block: "nearest" });
  }, [selectedId, shown]);

  const confirmDelete = async () => {
    if (!pendingDelete) return;
    const target = pendingDelete;
    setPendingDelete(null);
    try {
      await deleteMutation.mutateAsync(target.id);
      toast.success("已删除标注");
    } catch {
      toast.error("删除失败，请稍后重试");
    }
  };

  return (
    <div className="flex h-full min-h-0 flex-col" data-testid="annotation-panel">
      <div className="flex items-center justify-between gap-2 border-b px-3 py-2">
        <div className="flex items-center gap-1.5 text-sm font-medium">
          <MessageSquareText className="h-4 w-4" />
          标注
          <span className="text-xs font-normal text-muted-foreground">
            {shown.length}
          </span>
        </div>
        <Tabs
          value={statusFilter}
          onValueChange={(v) => onStatusFilterChange(v as AnnotationStatusFilter)}
        >
          <TabsList className="h-7">
            <TabsTrigger value="open" className="px-2 text-xs">
              未解决
            </TabsTrigger>
            <TabsTrigger value="resolved" className="px-2 text-xs">
              已解决
            </TabsTrigger>
            <TabsTrigger value="all" className="px-2 text-xs">
              全部
            </TabsTrigger>
          </TabsList>
        </Tabs>
      </div>

      {shown.length === 0 ? (
        <p className="px-3 py-10 text-center text-sm text-muted-foreground">
          {annotations.length === 0 ? "暂无标注" : EMPTY_TEXT[statusFilter]}
        </p>
      ) : (
        <ul ref={listRef} className="min-h-0 flex-1 divide-y overflow-y-auto">
          {shown.map((annotation) => (
            <AnnotationItem
              key={annotation.id}
              projectId={projectId}
              annotation={annotation}
              selected={annotation.id === selectedId}
              hovered={annotation.id === hoverId}
              onHover={onHover}
              onSelect={onSelect}
              onLocate={onLocate}
              onDelete={setPendingDelete}
            />
          ))}
        </ul>
      )}

      <AlertDialog
        open={pendingDelete !== null}
        onOpenChange={(o) => {
          if (!o) setPendingDelete(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>删除标注？</AlertDialogTitle>
            <AlertDialogDescription>
              删除后不可恢复。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>取消</AlertDialogCancel>
            <AlertDialogAction onClick={() => void confirmDelete()}>
              确认删除
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function AnnotationItem({
  projectId,
  annotation,
  selected,
  hovered,
  onHover,
  onSelect,
  onLocate,
  onDelete,
}: {
  projectId: string;
  annotation: ScreenshotAnnotation;
  selected: boolean;
  hovered: boolean;
  onHover: (annotationId: string | null) => void;
  onSelect: (annotationId: string) => void;
  onLocate: (annotation: ScreenshotAnnotation) => void;
  onDelete: (annotation: ScreenshotAnnotation) => void;
}) {
  const update = useUpdateScreenshotAnnotation(projectId);
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState("");
  const resolved = annotation.status === "resolved";
  const screenName = annotation.screen_title || annotation.screen_key;

  // Issue 选择器走服务端搜索，超过前 100 条的旧 Issue 也能关联上
  const [pickerOpen, setPickerOpen] = useState(false);
  const [issueSearch, setIssueSearch] = useState("");
  const deferredIssueSearch = useDeferredValue(issueSearch);
  const { data: issuesData, isFetching: issuesFetching } = useIssues(
    projectId,
    {
      page_size: 100,
      sort: "updated_desc",
      q: deferredIssueSearch.trim() || undefined,
    },
  );
  const issueOptions = issuesData?.items ?? [];

  const save = async (payload: UpdateScreenshotAnnotationPayload) => {
    try {
      await update.mutateAsync({ annotationId: annotation.id, payload });
      return true;
    } catch {
      toast.error("保存失败，请稍后重试");
      return false;
    }
  };

  const submitEdit = async () => {
    const next = draft.trim();
    if (!next) return;
    if (next === annotation.body || (await save({ body: next }))) {
      setEditing(false);
    }
  };

  // 当前关联的 Issue 可能不在前 100 条里，单独补一个选项保证能显示
  const linkedMissing =
    annotation.issue_id &&
    !issueOptions.some((issue) => issue.id === annotation.issue_id);

  return (
    <li
      data-annotation-id={annotation.id}
      data-testid={`annotation-item-${annotation.id}`}
      className={cn(
        "space-y-2 px-3 py-3 transition-colors",
        (selected || hovered) && "bg-accent/60",
        selected && "ring-1 ring-primary ring-inset",
      )}
      onPointerEnter={() => onHover(annotation.id)}
      onPointerLeave={() => onHover(null)}
      onClick={() => onSelect(annotation.id)}
    >
      <div className="flex gap-2.5">
        <img
          src={screenshotAnnotationApi.cropUrl(annotation)}
          alt=""
          loading="lazy"
          className="h-14 w-14 shrink-0 rounded-md border bg-muted/30 object-cover"
        />
        <div className="min-w-0 flex-1 space-y-1">
          {editing ? (
            <div className="space-y-1.5" onClick={(e) => e.stopPropagation()}>
              <Textarea
                autoFocus
                value={draft}
                maxLength={ANNOTATION_BODY_MAX}
                aria-label="编辑标注内容"
                className="min-h-16 text-sm"
                disabled={update.isPending}
                onChange={(e) => setDraft(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Escape") {
                    e.preventDefault();
                    e.stopPropagation();
                    setEditing(false);
                  } else if (
                    e.key === "Enter" &&
                    (e.metaKey || e.ctrlKey) &&
                    !e.nativeEvent.isComposing
                  ) {
                    e.preventDefault();
                    void submitEdit();
                  }
                }}
              />
              <div className="flex justify-end gap-1">
                <Button
                  size="icon-sm"
                  variant="ghost"
                  aria-label="取消编辑"
                  disabled={update.isPending}
                  onClick={() => setEditing(false)}
                >
                  <X className="h-3.5 w-3.5" />
                </Button>
                <Button
                  size="icon-sm"
                  aria-label="保存"
                  disabled={!draft.trim() || update.isPending}
                  onClick={() => void submitEdit()}
                >
                  <Check className="h-3.5 w-3.5" />
                </Button>
              </div>
            </div>
          ) : (
            <p
              className={cn(
                "line-clamp-3 text-sm break-words whitespace-pre-wrap",
                resolved && "text-muted-foreground",
              )}
            >
              {annotation.body}
            </p>
          )}
          <div className="flex flex-wrap items-center gap-x-1.5 gap-y-1 text-xs text-muted-foreground">
            <span className="max-w-32 truncate">{screenName}</span>
            <span>·</span>
            <span>{formatRelativeTime(annotation.created_at)}</span>
            <Badge
              variant={resolved ? "secondary" : "outline"}
              className={cn(
                !resolved && "border-amber-500/60 text-amber-600 dark:text-amber-400",
                resolved && "text-emerald-600 dark:text-emerald-400",
              )}
            >
              {resolved ? "已解决" : "未解决"}
            </Badge>
            {!annotation.is_latest_version && (
              <Badge variant="outline">旧版本</Badge>
            )}
          </div>
        </div>
      </div>

      <div onClick={(e) => e.stopPropagation()} className="space-y-2">
        <button
          type="button"
          aria-label="关联 Issue"
          disabled={update.isPending}
          onClick={() => setPickerOpen(true)}
          className="flex h-7 w-full items-center justify-between gap-1.5 rounded-lg border border-input bg-transparent py-1 pr-2 pl-2.5 text-sm outline-none transition-colors disabled:cursor-not-allowed disabled:opacity-50 dark:bg-input/30 dark:hover:bg-input/50 focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50"
        >
          <span className="min-w-0 flex-1 truncate text-left">
            {(() => {
              if (!annotation.issue_id) return "不关联 Issue";
              const hit = issueOptions.find(
                (i) => i.id === annotation.issue_id,
              );
              if (hit) return `${hit.reference} ${hit.title}`;
              return annotation.issue_reference
                ? `${annotation.issue_reference} ${annotation.issue_title ?? ""}`
                : "已关联 Issue";
            })()}
          </span>
          <ChevronDown className="h-4 w-4 shrink-0 text-muted-foreground" />
        </button>

        <Dialog
          open={pickerOpen}
          onOpenChange={(open) => {
            setPickerOpen(open);
            if (!open) setIssueSearch("");
          }}
        >
          <DialogContent className="sm:max-w-md" aria-label="关联 Issue">
            <DialogHeader>
              <DialogTitle>关联 Issue</DialogTitle>
            </DialogHeader>
            <Input
              autoFocus
              value={issueSearch}
              onChange={(e) => setIssueSearch(e.target.value)}
              placeholder="搜索标题或编号…"
              aria-label="搜索 Issue"
            />
            <div className="max-h-72 space-y-0.5 overflow-y-auto">
              <PickerRow
                label="不关联 Issue"
                selected={!annotation.issue_id}
                onSelect={() => {
                  void save({ issue_id: "" });
                  setPickerOpen(false);
                }}
              />
              {linkedMissing && (
                <PickerRow
                  label={`${annotation.issue_reference ?? ""} ${annotation.issue_title ?? ""}`}
                  selected
                  onSelect={() => setPickerOpen(false)}
                />
              )}
              {issueOptions.map((issue) => (
                <PickerRow
                  key={issue.id}
                  label={`${issue.reference} ${issue.title}`}
                  selected={issue.id === annotation.issue_id}
                  onSelect={() => {
                    void save({ issue_id: issue.id });
                    setPickerOpen(false);
                  }}
                />
              ))}
              {issueOptions.length === 0 && (
                <p className="px-2 py-6 text-center text-sm text-muted-foreground">
                  {issuesFetching ? "搜索中…" : "没有匹配的 Issue"}
                </p>
              )}
            </div>
          </DialogContent>
        </Dialog>

        <div className="flex items-center gap-1">
          <Button
            size="sm"
            variant="outline"
            onClick={() => onLocate(annotation)}
          >
            <Crosshair className="mr-1 h-3.5 w-3.5" />
            定位
          </Button>
          <div className="ml-auto flex items-center gap-0.5">
            <Button
              size="icon-sm"
              variant="ghost"
              aria-label="编辑文字"
              title="编辑文字"
              disabled={editing}
              onClick={() => {
                setDraft(annotation.body);
                setEditing(true);
              }}
            >
              <Pencil className="h-3.5 w-3.5" />
            </Button>
            <Button
              size="icon-sm"
              variant="ghost"
              aria-label={resolved ? "重新打开" : "标为已解决"}
              title={resolved ? "重新打开" : "标为已解决"}
              disabled={update.isPending}
              onClick={() =>
                void save({ status: resolved ? "open" : "resolved" })
              }
            >
              {resolved ? (
                <RotateCcw className="h-3.5 w-3.5" />
              ) : (
                <Check className="h-3.5 w-3.5" />
              )}
            </Button>
            <Button
              size="icon-sm"
              variant="ghost"
              aria-label="删除标注"
              title="删除标注"
              className="text-destructive hover:text-destructive"
              onClick={() => onDelete(annotation)}
            >
              <Trash2 className="h-3.5 w-3.5" />
            </Button>
          </div>
        </div>
      </div>
    </li>
  );
}

function PickerRow({
  label,
  selected,
  onSelect,
}: {
  label: string;
  selected: boolean;
  onSelect: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onSelect}
      className={cn(
        "flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm hover:bg-accent",
        selected && "bg-accent/60",
      )}
    >
      <span className="min-w-0 flex-1 truncate">{label}</span>
      {selected && <Check className="h-3.5 w-3.5 shrink-0" />}
    </button>
  );
}
