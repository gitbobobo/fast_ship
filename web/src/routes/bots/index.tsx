import { useState } from "react";
import { Link } from "react-router";
import { Bot, KeyRound, Pencil, MessagesSquare, Plus, Trash2 } from "lucide-react";
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
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Header } from "@/components/layout/header";
import { HeaderActions } from "@/components/layout/header-actions";
import { Skeleton } from "@/components/ui/skeleton";
import { ApiRequestError } from "@/lib/api/client";
import type { Bot as BotConfig } from "@/lib/api/bots";
import { useBots, useDeleteBot } from "@/lib/hooks/use-bots";
import { BotFormDialog } from "@/components/bots/bot-form-dialog";
import { formatDate } from "@/lib/utils/format";

// 机器人列表页：创建/编辑/删除入口，对话走详情页的唯一主线。

function errorText(err: unknown, fallback: string) {
  return err instanceof ApiRequestError && err.message ? err.message : fallback;
}

export default function BotsPage() {
  const { data: bots, isLoading } = useBots();
  const deleteBot = useDeleteBot();
  const [formOpen, setFormOpen] = useState(false);
  const [editing, setEditing] = useState<BotConfig | null>(null);

  const openCreate = () => {
    setEditing(null);
    setFormOpen(true);
  };

  const openEdit = (bot: BotConfig) => {
    setEditing(bot);
    setFormOpen(true);
  };

  const handleDelete = async (bot: BotConfig) => {
    try {
      await deleteBot.mutateAsync(bot.id);
      toast.success("机器人已删除");
    } catch (err) {
      // 活动 run 或未终结调用未清理时服务端返回冲突，提示先显式处理。
      toast.error(errorText(err, "删除失败"));
    }
  };

  return (
    <div className="flex flex-col">
      <Header
        title="机器人"
        actions={
          <HeaderActions
            primary={
              <Button size="sm" onClick={openCreate}>
                <Plus className="mr-1.5 h-3.5 w-3.5" />
                新建机器人
              </Button>
            }
          />
        }
      />
      <div className="mx-auto w-full max-w-5xl space-y-6 p-4 md:p-6">
        <p className="text-sm text-muted-foreground">
          内置智能体：使用你自己的 OpenAI 兼容 API Key 常驻待命，
          通过白名单工具操作项目、Issue 与 T3 线程，所有写操作需你确认。
        </p>

        <BotFormDialog
          open={formOpen}
          editing={editing}
          onClose={() => setFormOpen(false)}
        />

        {isLoading ? (
          <div className="space-y-3">
            {Array.from({ length: 3 }).map((_, i) => (
              <Skeleton key={i} className="h-10 rounded" />
            ))}
          </div>
        ) : !bots?.length ? (
          <div className="rounded-lg border bg-muted/40 py-12 text-center">
            <Bot className="mx-auto h-8 w-8 text-muted-foreground" />
            <p className="mt-2 text-sm text-muted-foreground">还没有机器人</p>
            <p className="mt-1 text-xs text-muted-foreground">
              点击右上角「新建机器人」，填入 OpenAI 兼容接口的地址与 Key 即可开始
            </p>
          </div>
        ) : (
          <div className="overflow-hidden rounded-lg border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>名称</TableHead>
                  <TableHead>模型</TableHead>
                  <TableHead>API 地址</TableHead>
                  <TableHead>密钥</TableHead>
                  <TableHead>创建时间</TableHead>
                  <TableHead className="text-right">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {bots.map((bot) => (
                  <TableRow key={bot.id}>
                    <TableCell className="font-medium">
                      <Link
                        to={`/bots/${bot.id}`}
                        className="hover:underline"
                      >
                        {bot.name}
                      </Link>
                    </TableCell>
                    <TableCell className="text-sm">{bot.model}</TableCell>
                    <TableCell className="max-w-56 truncate text-sm text-muted-foreground">
                      {bot.base_url}
                    </TableCell>
                    <TableCell className="text-sm">
                      {bot.api_key_last4 ? (
                        <span className="inline-flex items-center gap-1 text-muted-foreground">
                          <KeyRound className="h-3.5 w-3.5" />···{bot.api_key_last4}
                        </span>
                      ) : (
                        <span className="text-xs text-amber-700 dark:text-amber-400">
                          未配置
                        </span>
                      )}
                    </TableCell>
                    <TableCell className="whitespace-nowrap text-sm">
                      {formatDate(bot.created_at)}
                    </TableCell>
                    <TableCell className="text-right">
                      <div className="flex justify-end gap-1">
                        <Button
                          variant="ghost"
                          size="sm"
                          render={<Link to={`/bots/${bot.id}`} />}
                          aria-label={`对话 ${bot.name}`}
                        >
                          <MessagesSquare className="h-4 w-4" />
                        </Button>
                        <Button
                          variant="ghost"
                          size="sm"
                          aria-label={`编辑 ${bot.name}`}
                          onClick={() => openEdit(bot)}
                        >
                          <Pencil className="h-4 w-4" />
                        </Button>
                        <AlertDialog>
                          <AlertDialogTrigger
                            render={
                              <Button
                                variant="ghost"
                                size="sm"
                                aria-label={`删除 ${bot.name}`}
                              />
                            }
                          >
                            <Trash2 className="h-4 w-4" />
                          </AlertDialogTrigger>
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
                              <AlertDialogAction
                                onClick={() => void handleDelete(bot)}
                              >
                                确认删除
                              </AlertDialogAction>
                            </AlertDialogFooter>
                          </AlertDialogContent>
                        </AlertDialog>
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </div>
    </div>
  );
}
