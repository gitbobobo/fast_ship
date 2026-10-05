import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Sparkles } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
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
import type { CopyIssuePromptButtonHandle } from "@/components/issues/copy-issue-prompt-button";
import {
  useRecommendations,
  useRemoveRecommendation,
} from "@/lib/hooks/use-recommendations";
import {
  RecommendationListPane,
  RECOMMENDATION_PRIORITY_GROUPS,
} from "./recommendation-list-pane";
import { RecommendationDetailPane } from "./recommendation-detail-pane";

/**
 * 看板筛选行的「推荐」按钮与推荐弹框。
 * 列表为空（含加载中、加载失败）时整体不渲染。
 * 弹框左右双栏：左栏分组单行列表，右栏选中项只读详情，决策闭环不出弹框。
 * ↑/↓（等价 j/k）跨组移动选中，Enter 复制提示词，Esc 关闭；
 * 右栏滚动交给滚轮/触控板，按键不按焦点区分键义。
 */
export function RecommendedIssuesButton({
  projectId,
}: {
  projectId?: string;
}) {
  const [open, setOpen] = useState(false);
  const [selectedIssueId, setSelectedIssueId] = useState<string | null>(null);
  const [pendingRemove, setPendingRemove] =
    useState<IssueRecommendation | null>(null);
  // 选中项消失时按上次位置顺延（同位置即下一条，越界即上一条）
  const lastSelectedIndexRef = useRef(0);
  const listPaneRef = useRef<HTMLDivElement | null>(null);
  const copyHandleRef = useRef<CopyIssuePromptButtonHandle | null>(null);

  const { data } = useRecommendations(projectId);
  const removeRecommendation = useRemoveRecommendation();
  const items = useMemo(() => data?.items ?? [], [data]);

  // 选中序与左栏展示序一致：分组排列，组内 updated_at 降序
  const orderedItems = useMemo(
    () =>
      RECOMMENDATION_PRIORITY_GROUPS.flatMap((group) =>
        items.filter((item) => item.priority === group.priority),
      ),
    [items],
  );

  const selectedItem =
    orderedItems.find((item) => item.issue.id === selectedIssueId) ?? null;

  const handleOpenChange = useCallback(
    (next: boolean) => {
      if (next) {
        // 每次打开默认选中最高优先级的第一条
        lastSelectedIndexRef.current = 0;
        setSelectedIssueId(orderedItems[0]?.issue.id ?? null);
      } else {
        setPendingRemove(null);
      }
      setOpen(next);
    },
    [orderedItems],
  );

  // 选中项被移除或轮询消失时顺延：同位置（下一条）优先，越界退上一条；列表清空则关弹框
  useEffect(() => {
    if (!open) return;
    if (orderedItems.length === 0) {
      setPendingRemove(null);
      setOpen(false);
      return;
    }
    const index = orderedItems.findIndex(
      (item) => item.issue.id === selectedIssueId,
    );
    if (index >= 0) {
      lastSelectedIndexRef.current = index;
      return;
    }
    const fallback =
      orderedItems[
        Math.min(lastSelectedIndexRef.current, orderedItems.length - 1)
      ];
    setSelectedIssueId(fallback.issue.id);
  }, [open, orderedItems, selectedIssueId]);

  // 键盘/点击选中后保证左栏行可见
  useEffect(() => {
    if (!open || !selectedIssueId) return;
    listPaneRef.current
      ?.querySelector(`[data-issue-id="${selectedIssueId}"]`)
      ?.scrollIntoView({ block: "nearest" });
  }, [open, selectedIssueId]);

  const moveSelection = useCallback(
    (delta: number) => {
      setSelectedIssueId((current) => {
        const index = orderedItems.findIndex(
          (item) => item.issue.id === current,
        );
        const nextIndex = index < 0 ? 0 : index + delta;
        const clamped = Math.min(
          Math.max(nextIndex, 0),
          orderedItems.length - 1,
        );
        return orderedItems[clamped]?.issue.id ?? current;
      });
    },
    [orderedItems],
  );

  // 弹框内全局按键（仿 use-board-multi-select 的 window 监听范式，仅 open 时挂载）。
  // 必须用捕获阶段：base-ui Dialog 在 document 上对 ArrowUp/ArrowDown 调 stopPropagation，
  // 冒泡阶段监听拿不到这两个键。事件源落在菜单/确认框浮层内时放行，按键归浮层
  useEffect(() => {
    if (!open) return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.defaultPrevented || pendingRemove) return;
      // 修饰键组合让位浏览器/系统快捷键（如 Cmd+K）
      if (event.metaKey || event.ctrlKey || event.altKey) return;
      const target = event.target;
      if (
        target instanceof HTMLElement &&
        target.closest('[role="menu"], [role="alertdialog"]')
      ) {
        return;
      }
      if (event.key === "ArrowDown" || event.key === "j") {
        event.preventDefault();
        moveSelection(1);
      } else if (event.key === "ArrowUp" || event.key === "k") {
        event.preventDefault();
        moveSelection(-1);
      } else if (event.key === "Enter") {
        // 焦点在可交互控件上时让位原生激活（dep chip、返回、链接、按钮），其余位置 Enter = 复制提示词
        if (
          target instanceof HTMLElement &&
          target.closest(
            'button, a[href], input, textarea, select, [role="button"], [role="link"], [contenteditable="true"]',
          )
        ) {
          return;
        }
        event.preventDefault();
        copyHandleRef.current?.activate();
      }
    };
    window.addEventListener("keydown", handleKeyDown, true);
    return () => window.removeEventListener("keydown", handleKeyDown, true);
  }, [open, pendingRemove, moveSelection]);

  if (items.length === 0) {
    return null;
  }

  const handleConfirmRemove = async () => {
    if (!pendingRemove) return;
    try {
      await removeRecommendation.mutateAsync(pendingRemove.issue.id);
      toast.success("已移除推荐");
    } catch {
      toast.error("移除推荐失败");
    } finally {
      setPendingRemove(null);
    }
  };

  return (
    <>
      <Button
        variant="outline"
        size="sm"
        className="ml-auto h-7 text-xs"
        onClick={() => handleOpenChange(true)}
      >
        <Sparkles className="mr-1 h-3 w-3" />
        推荐
        <span className="ml-1 rounded-full bg-muted px-1.5 text-xs font-medium text-muted-foreground">
          {items.length}
        </span>
      </Button>

      <Dialog open={open} onOpenChange={handleOpenChange}>
        <DialogContent className="flex h-[80vh] flex-col gap-0 overflow-hidden p-0 sm:max-w-[1100px]">
          <DialogHeader className="gap-1.5 border-b px-5 pt-4 pb-3">
            <DialogTitle>
              推荐任务
              <span className="ml-2 text-sm font-normal text-muted-foreground tabular-nums">
                {items.length}
              </span>
            </DialogTitle>
            <DialogDescription className="text-xs">
              按优先级排列，开始开发或关闭后自动移出。↑/↓ 切换，Enter 复制提示词。
            </DialogDescription>
          </DialogHeader>
          <div className="grid min-h-0 flex-1 grid-rows-[auto_minmax(0,1fr)] sm:grid-cols-[280px_minmax(0,1fr)] sm:grid-rows-none">
            <RecommendationListPane
              items={orderedItems}
              selectedIssueId={selectedIssueId}
              onSelect={setSelectedIssueId}
              listRef={listPaneRef}
              showProject={!projectId}
            />
            {selectedItem ? (
              <RecommendationDetailPane
                key={selectedItem.issue.id}
                item={selectedItem}
                onRemove={() => setPendingRemove(selectedItem)}
                copyHandleRef={copyHandleRef}
                showProject={!projectId}
              />
            ) : (
              <div className="hidden sm:block" />
            )}
          </div>
        </DialogContent>
      </Dialog>

      <AlertDialog
        open={pendingRemove !== null}
        onOpenChange={(next) => {
          if (!next) setPendingRemove(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>确认移除推荐？</AlertDialogTitle>
            <AlertDialogDescription>
              将移除「{pendingRemove?.issue.title}
              」的推荐标记，此操作不可撤销。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>取消</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => void handleConfirmRemove()}
              disabled={removeRecommendation.isPending}
            >
              {removeRecommendation.isPending ? "移除中..." : "确认移除"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
