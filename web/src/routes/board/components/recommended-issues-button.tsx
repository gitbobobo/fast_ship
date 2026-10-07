import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Sparkles } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
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
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import type { CopyIssuePromptButtonHandle } from "@/components/issues/copy-issue-prompt-button";
import {
  useDeferRecommendation,
  useRecommendations,
  useRemoveRecommendation,
  useRestoreRecommendation,
} from "@/lib/hooks/use-recommendations";
import {
  RecommendationListPane,
  RECOMMENDATION_PRIORITY_GROUPS,
} from "./recommendation-list-pane";
import { RecommendationDetailPane } from "./recommendation-detail-pane";

type RecommendationTab = "active" | "deferred";

// 对齐 server 端 defer_note 的 500 rune 上限（前端按字符数近似）
const DEFER_NOTE_MAX_LENGTH = 500;

/**
 * 看板筛选行的「推荐」按钮与推荐弹框。
 * 推荐中与已延后均为空（含加载中、加载失败）时整体不渲染；徽标只计推荐中条数。
 * 弹框分「推荐中」「已延后」两个 tab，共用左右双栏：左栏分组单行列表，右栏选中项
 * 只读详情，决策闭环不出弹框。↑/↓（等价 j/k）跨组移动选中，Enter 复制提示词，
 * Esc 关闭；右栏滚动交给滚轮/触控板，按键不按焦点区分键义。
 */
export function RecommendedIssuesButton({
  projectId,
}: {
  projectId?: string;
}) {
  const [open, setOpen] = useState(false);
  const [tab, setTab] = useState<RecommendationTab>("active");
  const [selectedIssueId, setSelectedIssueId] = useState<string | null>(null);
  const [pendingDefer, setPendingDefer] = useState<IssueRecommendation | null>(
    null,
  );
  const [deferNote, setDeferNote] = useState("");
  const [pendingRemove, setPendingRemove] =
    useState<IssueRecommendation | null>(null);
  // 选中项消失时按上次位置顺延（同位置即下一条，越界即上一条）
  const lastSelectedIndexRef = useRef(0);
  const listPaneRef = useRef<HTMLDivElement | null>(null);
  const copyHandleRef = useRef<CopyIssuePromptButtonHandle | null>(null);

  const { data } = useRecommendations(projectId);
  const deferRecommendation = useDeferRecommendation();
  const restoreRecommendation = useRestoreRecommendation();
  const removeRecommendation = useRemoveRecommendation();
  const items = useMemo(() => data?.items ?? [], [data]);

  // 选中序与左栏展示序一致：分组排列；推荐中组内保持服务端序（updated_at 降序），
  // 已延后组内按 deferred_at 降序
  const orderedByTab = useMemo<
    Record<RecommendationTab, IssueRecommendation[]>
  >(() => {
    const active: IssueRecommendation[] = [];
    const deferred: IssueRecommendation[] = [];
    for (const item of items) {
      (item.status === "deferred" ? deferred : active).push(item);
    }
    deferred.sort((a, b) => (b.deferred_at ?? "").localeCompare(a.deferred_at ?? ""));
    const byGroup = (list: IssueRecommendation[]) =>
      RECOMMENDATION_PRIORITY_GROUPS.flatMap((group) =>
        list.filter((item) => item.priority === group.priority),
      );
    return { active: byGroup(active), deferred: byGroup(deferred) };
  }, [items]);

  const orderedItems = orderedByTab[tab];

  const selectedItem =
    orderedItems.find((item) => item.issue.id === selectedIssueId) ?? null;

  const handleOpenChange = useCallback(
    (next: boolean) => {
      if (next) {
        // 每次打开默认落在推荐中；推荐中为空但已延后非空时落在已延后，
        // 并默认选中该 tab 的第一条
        const initialTab: RecommendationTab =
          orderedByTab.active.length > 0 ? "active" : "deferred";
        setTab(initialTab);
        lastSelectedIndexRef.current = 0;
        setSelectedIssueId(orderedByTab[initialTab][0]?.issue.id ?? null);
      } else {
        setPendingDefer(null);
        setDeferNote("");
        setPendingRemove(null);
      }
      setOpen(next);
    },
    [orderedByTab],
  );

  const handleTabChange = useCallback(
    (next: string) => {
      const nextTab: RecommendationTab =
        next === "deferred" ? "deferred" : "active";
      setTab(nextTab);
      // 切 tab 默认选中第一条，选中顺延位置也随之重置
      lastSelectedIndexRef.current = 0;
      setSelectedIssueId(orderedByTab[nextTab][0]?.issue.id ?? null);
    },
    [orderedByTab],
  );

  // 选中项被移除/延后/移回或轮询消失时顺延：同位置（下一条）优先，越界退上一条；
  // 仅当两个 tab 都空时才关弹框，当前 tab 空了则停留本 tab 展示空态
  useEffect(() => {
    if (!open) return;
    if (items.length === 0) {
      setPendingDefer(null);
      setPendingRemove(null);
      setOpen(false);
      return;
    }
    if (orderedItems.length === 0) {
      if (selectedIssueId !== null) setSelectedIssueId(null);
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
  }, [open, items.length, orderedItems, selectedIssueId]);

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
      if (event.defaultPrevented || pendingRemove || pendingDefer) return;
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
        // 焦点在可交互控件上时让位原生激活（dep chip、返回、链接、延后/移除/复制按钮），
        // 左栏行（[data-issue-id]）除外：行的选中交给方向键/点击，Enter 恒定 = 复制提示词。
        // 否则 Dialog 打开时 base-ui 自动聚焦首行会把 Enter 吞成行激活。
        if (
          target instanceof HTMLElement &&
          !target.closest("[data-issue-id]") &&
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
  }, [open, pendingRemove, pendingDefer, moveSelection]);

  if (items.length === 0) {
    return null;
  }

  const handleConfirmDefer = async () => {
    if (!pendingDefer) return;
    const note = deferNote.trim();
    if ([...note].length > DEFER_NOTE_MAX_LENGTH) {
      toast.error(`备注不能超过 ${DEFER_NOTE_MAX_LENGTH} 个字符`);
      return;
    }
    try {
      await deferRecommendation.mutateAsync({
        issueId: pendingDefer.issue.id,
        note: note === "" ? undefined : note,
      });
      toast.success("已延后");
      // 失败路径保留对话框与已输入备注，只有成功才关闭并清空
      setPendingDefer(null);
      setDeferNote("");
    } catch {
      toast.error("延后处理失败");
    }
  };

  const handleRestore = async (item: IssueRecommendation) => {
    try {
      await restoreRecommendation.mutateAsync(item.issue.id);
      toast.success("已移回推荐");
    } catch {
      toast.error("移回推荐失败");
    }
  };

  const handleConfirmRemove = async () => {
    if (!pendingRemove) return;
    try {
      await removeRecommendation.mutateAsync(pendingRemove.issue.id);
      toast.success("已彻底移除");
    } catch {
      toast.error("移除推荐失败");
    } finally {
      setPendingRemove(null);
    }
  };

  const renderPane = (ordered: IssueRecommendation[]) => (
    <>
      <RecommendationListPane
        items={ordered}
        selectedIssueId={selectedIssueId}
        onSelect={setSelectedIssueId}
        listRef={listPaneRef}
        showProject={!projectId}
      />
      {selectedItem ? (
        <RecommendationDetailPane
          key={selectedItem.issue.id}
          item={selectedItem}
          onDefer={() => setPendingDefer(selectedItem)}
          onRestore={() => void handleRestore(selectedItem)}
          restorePending={restoreRecommendation.isPending}
          onRemove={() => setPendingRemove(selectedItem)}
          copyHandleRef={copyHandleRef}
          showProject={!projectId}
        />
      ) : (
        <div className="hidden sm:block" />
      )}
    </>
  );

  const paneClassName =
    "grid min-h-0 flex-1 grid-rows-[auto_minmax(0,1fr)] sm:grid-cols-[280px_minmax(0,1fr)] sm:grid-rows-none";

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
        {orderedByTab.active.length > 0 && (
          <span className="ml-1 rounded-full bg-muted px-1.5 text-xs font-medium text-muted-foreground">
            {orderedByTab.active.length}
          </span>
        )}
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
          <Tabs
            value={tab}
            onValueChange={handleTabChange}
            className="flex min-h-0 flex-1 flex-col"
          >
            <div className="border-b px-3">
              <TabsList variant="line" aria-label="推荐状态">
                <TabsTrigger value="active">
                  推荐中
                  <span className="text-xs text-muted-foreground tabular-nums">
                    {orderedByTab.active.length}
                  </span>
                </TabsTrigger>
                <TabsTrigger value="deferred">
                  已延后
                  <span className="text-xs text-muted-foreground tabular-nums">
                    {orderedByTab.deferred.length}
                  </span>
                </TabsTrigger>
              </TabsList>
            </div>
            <TabsContent value="active" className={paneClassName}>
              {orderedByTab.active.length === 0 ? (
                <p className="col-span-full flex min-h-40 items-center justify-center text-sm text-muted-foreground">
                  暂无推荐中的任务
                </p>
              ) : (
                renderPane(orderedByTab.active)
              )}
            </TabsContent>
            <TabsContent value="deferred" className={paneClassName}>
              {orderedByTab.deferred.length === 0 ? (
                <p className="col-span-full flex min-h-40 items-center justify-center text-sm text-muted-foreground">
                  暂无已延后的任务
                </p>
              ) : (
                renderPane(orderedByTab.deferred)
              )}
            </TabsContent>
          </Tabs>
        </DialogContent>
      </Dialog>

      <Dialog
        open={pendingDefer !== null}
        onOpenChange={(next) => {
          if (!next) {
            setPendingDefer(null);
            setDeferNote("");
          }
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>延后处理</DialogTitle>
            <DialogDescription>
              「{pendingDefer?.issue.title}
              」将移至已延后，可随时移回推荐。
            </DialogDescription>
          </DialogHeader>
          <Textarea
            value={deferNote}
            onChange={(event) => setDeferNote(event.target.value)}
            placeholder="备注（可选）"
            aria-label="延后备注"
            maxLength={DEFER_NOTE_MAX_LENGTH}
          />
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => {
                setPendingDefer(null);
                setDeferNote("");
              }}
            >
              取消
            </Button>
            <Button
              onClick={() => void handleConfirmDefer()}
              disabled={deferRecommendation.isPending}
            >
              {deferRecommendation.isPending ? "延后中..." : "确认延后"}
            </Button>
          </DialogFooter>
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
            <AlertDialogTitle>确认彻底移除？</AlertDialogTitle>
            <AlertDialogDescription>
              将彻底移除「{pendingRemove?.issue.title}
              」的推荐标记，此操作不可撤销。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>取消</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
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
