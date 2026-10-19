import { useState } from "react";
import { Link, useNavigate } from "react-router";
import {
  Bot as BotIcon,
  PanelRightClose,
  PanelRightOpen,
  Plus,
} from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
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
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Header } from "@/components/layout/header";
import { HeaderActions } from "@/components/layout/header-actions";
import { ApiRequestError } from "@/lib/api/client";
import type { Bot } from "@/lib/api/bots";
import { useBot, useBots, useDeleteBot } from "@/lib/hooks/use-bots";
import { cn } from "@/lib/utils";
import { BotAuthorizationsDialog } from "./bot-authorizations-dialog";
import { BotChat } from "./bot-chat";
import {
  BotDetailPanel,
  BotDetailPanelSkeleton,
  type BotDetailPanelSchedule,
} from "./bot-detail-panel";
import { BotFormDialog } from "./bot-form-dialog";
import { BotListPanel } from "./bot-list-panel";
import { BotScheduleTaskDialog } from "./bot-schedule-section";

/**
 * 机器人三栏工作台（INT-125）：左栏列表 / 中栏主线聊天 / 右栏详情。
 * 桌面（lg+）整页不滚动、三栏各自滚动；移动端 /bots 只显示列表、
 * /bots/:botId 只显示聊天，详情改由右侧 Sheet 打开。
 * 「隐藏详情」（桌面）与「详情 Sheet」（移动）是两份互不影响的状态，
 * 但编辑/授权/删除/任务详情弹窗只在这一层各持有一份——桌面 aside 与
 * 移动 Sheet 里的两份面板共享同一份状态（约定同 issue 详情页侧栏）。
 */
export function BotWorkspace({ botId }: { botId?: string }) {
  const navigate = useNavigate();
  const { data: bot } = useBot(botId ?? "");
  const { data: bots } = useBots();
  const deleteBot = useDeleteBot();
  const [panelHidden, setPanelHidden] = useState(false);
  const [sheetOpen, setSheetOpen] = useState(false);
  // 新建（editing=null）与编辑（打开时捕获的 bot）共用同一个表单弹窗。
  const [formOpen, setFormOpen] = useState(false);
  const [formEditing, setFormEditing] = useState<Bot | null>(null);
  const [authOpen, setAuthOpen] = useState(false);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [scheduleExpanded, setScheduleExpanded] = useState(true);
  const [scheduleDetailId, setScheduleDetailId] = useState<string | null>(null);

  const handleDelete = async () => {
    if (!bot) return;
    try {
      await deleteBot.mutateAsync(bot.id);
      toast.success("机器人已删除");
      navigate("/bots");
    } catch (err) {
      // 活动 run 或未终结调用未清理时服务端返回冲突，提示先显式处理。
      toast.error(
        err instanceof ApiRequestError && err.message
          ? err.message
          : "删除失败",
      );
    }
  };

  // 新建入口统一走这里：重置 editing，避免沿用上次的编辑目标。
  const openCreate = () => {
    setFormEditing(null);
    setFormOpen(true);
  };

  const schedule: BotDetailPanelSchedule = {
    expanded: scheduleExpanded,
    onExpandedChange: setScheduleExpanded,
    onOpenDetail: setScheduleDetailId,
  };

  // aside 与 Sheet 各挂一份面板但共享工作台的全部状态；onHide 由宿主
  // 决定语义——桌面是「隐藏右栏」、Sheet 里是「关闭 Sheet」。
  const detailPanel = (onHide: () => void) =>
    bot ? (
      <BotDetailPanel
        bot={bot}
        schedule={schedule}
        onEdit={() => {
          setFormEditing(bot);
          setFormOpen(true);
        }}
        onAuthorize={() => setAuthOpen(true)}
        onDelete={() => setDeleteOpen(true)}
        onHide={onHide}
      />
    ) : (
      <BotDetailPanelSkeleton />
    );

  return (
    <div className="flex h-full min-h-0 flex-col">
      <Header
        title={
          bot?.name ??
          bots?.find((b) => b.id === botId)?.name ??
          "机器人"
        }
        actions={
          botId && (
            <HeaderActions
              primary={
                <div className="flex items-center gap-1.5">
                  {/* 移动端深链 /bots/:id 时页头「返回」可能不可用，
                      这里固定给一条回列表的入口。 */}
                  <Button
                    variant="outline"
                    size="sm"
                    className="lg:hidden"
                    render={<Link to="/bots" aria-label="机器人列表" />}
                  >
                    机器人
                  </Button>
                  <Button
                    variant="outline"
                    size="sm"
                    className="lg:hidden"
                    aria-label="详情"
                    onClick={() => setSheetOpen(true)}
                  >
                    详情
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    className="hidden lg:inline-flex"
                    aria-label={panelHidden ? "显示详情" : "隐藏详情"}
                    onClick={() => setPanelHidden((v) => !v)}
                  >
                    {panelHidden ? (
                      <PanelRightOpen className="h-4 w-4" />
                    ) : (
                      <PanelRightClose className="h-4 w-4" />
                    )}
                  </Button>
                </div>
              }
            />
          )
        }
      />

      <div className="flex min-h-0 flex-1">
        <BotListPanel selectedId={botId} onCreate={openCreate} />

        <div
          className={cn(
            "min-w-0 flex-1 flex-col",
            // 中栏是聊天内容的滚动容器（useScrollFollow 依赖它）；
            // 未选机器人时桌面显示空状态、移动端让位给列表。
            botId ? "flex min-h-0 overflow-y-auto" : "hidden lg:flex",
          )}
        >
          {botId ? (
            <BotChat key={botId} botId={botId} />
          ) : (
            <div className="flex flex-1 flex-col items-center justify-center gap-3 px-6 text-center">
              <BotIcon className="h-10 w-10 text-muted-foreground" />
              <p className="text-sm text-muted-foreground">
                从左侧选择一个机器人
              </p>
              <p className="max-w-md text-xs text-muted-foreground">
                内置智能体：使用你自己的 OpenAI 兼容 API Key
                常驻待命，通过白名单工具操作项目、Issue 与 T3
                线程，所有写操作需你确认。
              </p>
              <Button size="sm" onClick={openCreate}>
                <Plus className="mr-1.5 h-3.5 w-3.5" />
                新建机器人
              </Button>
            </div>
          )}
        </div>

        {/* 右栏随 botId 挂载而非详情数据：切换机器人时骨架占位，
            中栏宽度与页头不回跳。 */}
        {botId && !panelHidden && (
          <aside className="hidden h-full w-80 shrink-0 border-l lg:block">
            {detailPanel(() => setPanelHidden(true))}
          </aside>
        )}
      </div>

      <BotFormDialog
        open={formOpen}
        editing={formEditing}
        onClose={() => setFormOpen(false)}
        onSaved={
          formEditing
            ? undefined
            : (saved) => navigate(`/bots/${saved.id}`)
        }
      />
      {bot && (
        <BotAuthorizationsDialog
          botId={bot.id}
          open={authOpen}
          onClose={() => setAuthOpen(false)}
        />
      )}
      <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>删除机器人？</AlertDialogTitle>
            <AlertDialogDescription>
              删除会一并清理对话记录与授权。
              有进行中的主线或未终结的调用时无法删除，需先停止或逐项处理。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>取消</AlertDialogCancel>
            <AlertDialogAction onClick={() => void handleDelete()}>
              确认删除
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      {botId && (
        <BotScheduleTaskDialog
          botId={botId}
          taskId={scheduleDetailId}
          onClose={() => setScheduleDetailId(null)}
        />
      )}

      {botId && (
        <Sheet open={sheetOpen} onOpenChange={setSheetOpen}>
          <SheetContent
            side="right"
            showCloseButton={false}
            className="w-80 max-w-[85vw] gap-0 p-0"
          >
            {/* Base UI Popup 是 role=dialog：sr-only 标题给辅助技术一个名字。 */}
            <SheetHeader className="sr-only">
              <SheetTitle>{bot?.name ?? "机器人"}详情</SheetTitle>
            </SheetHeader>
            {detailPanel(() => setSheetOpen(false))}
          </SheetContent>
        </Sheet>
      )}
    </div>
  );
}
