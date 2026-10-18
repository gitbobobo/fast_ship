import { useState } from "react";
import { Link } from "react-router";
import { Plus, History, Pencil, Trash2 } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
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
import { formatDate } from "@/lib/utils/format";
import { ApiRequestError } from "@/lib/api/client";
import { type Automation } from "@/lib/api/automations";
import {
  useAutomations,
  useDeleteAutomation,
  useToggleAutomation,
} from "@/lib/hooks/use-automations";
import { effectiveTimezone } from "@/lib/cron-schedule";
import { AutomationRunsDialog } from "./runs-table";
import { automationTriggersSummary, earliestNextRunAt } from "@/lib/automations/form-model";

// 自动化设置页（INT-90）：列表/启停/删除 + 运行记录弹窗。
// INT-118：新建/编辑迁到独立页（/automations/new 与 /automations/:id），
// 触发列展示多触发器摘要。

export default function AutomationsPage() {
  const { data: automations, isLoading } = useAutomations();
  const toggleAutomation = useToggleAutomation();
  const deleteAutomation = useDeleteAutomation();

  const [runsFor, setRunsFor] = useState<Automation | null>(null);

  // 只传 id：hook 会先 GET 最新详情再组全量 PUT，避免用列表里可能过期的
  // 触发器快照把别处新增的触发器删掉。
  const handleToggle = async (a: Automation) => {
    try {
      await toggleAutomation.mutateAsync(a.id);
      toast.success(a.enabled ? "已停用（进行中的运行不受影响）" : "已启用");
    } catch (err) {
      toast.error(errMessage(err, "操作失败"));
    }
  };

  const handleDelete = async (a: Automation) => {
    try {
      await deleteAutomation.mutateAsync(a.id);
      toast.success("自动化已删除");
    } catch (err) {
      toast.error(errMessage(err, "删除失败"));
    }
  };

  return (
    <div className="flex flex-col">
      <Header
        title="自动化"
        actions={
          <HeaderActions
            primary={
              <Button size="sm" render={<Link to="/automations/new" />}>
                <Plus className="mr-1.5 h-3.5 w-3.5" />
                新建自动化
              </Button>
            }
          />
        }
      />
      <div className="mx-auto w-full max-w-5xl space-y-6 p-4 md:p-6">
        <p className="text-sm text-muted-foreground">
          按 cron、内部 Issue 事件或 GitHub PR 事件自动在 T3
          环境开线程执行提示词，运行结束后可自动 settle / archive
          线程并发送站内通知。
        </p>

        {isLoading ? (
          <div className="space-y-3">
            {Array.from({ length: 3 }).map((_, i) => (
              <Skeleton key={i} className="h-10 rounded" />
            ))}
          </div>
        ) : !automations?.length ? (
          <div className="rounded-lg border bg-muted/40 py-12 text-center">
            <p className="text-sm text-muted-foreground">暂无自动化</p>
            <p className="mt-1 text-xs text-muted-foreground">
              点击右上角「新建自动化」创建第一条自动化
            </p>
          </div>
        ) : (
          <div className="overflow-hidden rounded-lg border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>名称</TableHead>
                  <TableHead>触发</TableHead>
                  <TableHead>后置</TableHead>
                  <TableHead>下次运行</TableHead>
                  <TableHead>状态</TableHead>
                  <TableHead className="text-right">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {automations.map((a) => {
                  const nextRunAt = earliestNextRunAt(a);
                  return (
                    <TableRow key={a.id}>
                      <TableCell className="font-medium">{a.name}</TableCell>
                      <TableCell
                        className="text-sm"
                        // hover 展示各 cron 触发器的原始表达式与时区
                        //（空时区服务端按 UTC）（INT-100/INT-118）。
                        title={cronTitle(a)}
                      >
                        {automationTriggersSummary(a)}
                      </TableCell>
                      <TableCell className="text-sm">
                        {postActionSummary(a)}
                      </TableCell>
                      <TableCell className="text-sm">
                        {nextRunAt ? formatDate(nextRunAt) : "—"}
                      </TableCell>
                      <TableCell>
                        <Badge variant={a.enabled ? "default" : "secondary"}>
                          {a.enabled ? "已启用" : "已停用"}
                        </Badge>
                      </TableCell>
                      <TableCell className="text-right">
                        <div className="flex justify-end gap-1">
                          <Button
                            variant="ghost"
                            size="sm"
                            aria-label={`查看运行记录 ${a.name}`}
                            onClick={() => setRunsFor(a)}
                          >
                            <History className="h-4 w-4" />
                          </Button>
                          <Button
                            variant="ghost"
                            size="sm"
                            aria-label={`编辑 ${a.name}`}
                            render={<Link to={`/automations/${a.id}`} />}
                          >
                            <Pencil className="h-4 w-4" />
                          </Button>
                          <Button
                            variant="ghost"
                            size="sm"
                            onClick={() => handleToggle(a)}
                          >
                            {a.enabled ? "停用" : "启用"}
                          </Button>
                          <AlertDialog>
                            <AlertDialogTrigger
                              render={
                                <Button
                                  variant="ghost"
                                  size="sm"
                                  aria-label={`删除 ${a.name}`}
                                />
                              }
                            >
                              <Trash2 className="h-4 w-4" />
                            </AlertDialogTrigger>
                            <AlertDialogContent>
                              <AlertDialogHeader>
                                <AlertDialogTitle>删除自动化？</AlertDialogTitle>
                                <AlertDialogDescription>
                                  删除会一并清理运行记录。有进行中的运行时无法删除；
                                  远端线程不会被终止。
                                </AlertDialogDescription>
                              </AlertDialogHeader>
                              <AlertDialogFooter>
                                <AlertDialogCancel>取消</AlertDialogCancel>
                                <AlertDialogAction
                                  onClick={() => handleDelete(a)}
                                >
                                  确认删除
                                </AlertDialogAction>
                              </AlertDialogFooter>
                            </AlertDialogContent>
                          </AlertDialog>
                        </div>
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          </div>
        )}
      </div>

      <AutomationRunsDialog
        automation={runsFor}
        onClose={() => setRunsFor(null)}
      />
    </div>
  );
}

// 触发列 hover 文案：全部 cron 触发器的表达式（时区）。
function cronTitle(a: Automation): string | undefined {
  const crons = (a.triggers ?? []).filter((t) => t.trigger_type === "cron");
  if (!crons.length) return undefined;
  return crons
    .map((t) => `${t.cron_expression ?? ""}（${effectiveTimezone(t.timezone)}）`)
    .join("；");
}

function postActionSummary(a: Automation): string {
  switch (a.post_action) {
    case "settle":
      return a.post_on_timeout ? "settle（超时也执行）" : "settle";
    case "archive":
      return a.post_on_timeout ? "archive（超时也执行）" : "archive";
    default:
      return "无";
  }
}

function errMessage(err: unknown, fallback: string): string {
  if (err instanceof ApiRequestError && err.message) return err.message;
  return fallback;
}
