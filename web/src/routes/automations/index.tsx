import { useMemo, useState } from "react";
import { Plus, History, Pencil, Trash2 } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import {
  AutomationEditorDialog,
  browserTimezone,
} from "./editor-dialog";
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
import { useAuthStore } from "@/lib/store/auth-store";
import {
  automationPostStateLabel,
  automationRunStatusLabel,
  automationTriggerLabel,
  type Automation,
  type AutomationInput,
  type AutomationRun,
  type RuntimeMode,
} from "@/lib/api/automations";
import {
  useAutomations,
  useAutomationRuns,
  useCreateAutomation,
  useDeleteAutomation,
  useToggleAutomation,
  useUpdateAutomation,
} from "@/lib/hooks/use-automations";

// 自动化设置页（INT-90）：列表/新建/编辑/启停/删除 + 运行记录。
// 新建默认停用、空提示词；archive 后置会终止活动线程，表单里就地说明。

const emptyForm: AutomationInput = {
  name: "",
  enabled: false,
  environment_id: "",
  t3_project_id: "",
  trigger_type: "cron",
  cron_expression: "",
  timezone: "",
  issue_project_id: null,
  event_filter: null,
  prompt_template: "",
  model_selection: null,
  runtime_mode: "full-access",
  workspace: null,
  timeout_seconds: 3600,
  post_action: "none",
  post_on_timeout: false,
};

export default function AutomationsPage() {
  const { data: automations, isLoading } = useAutomations();
  const createAutomation = useCreateAutomation();
  const updateAutomation = useUpdateAutomation();
  const toggleAutomation = useToggleAutomation();
  const deleteAutomation = useDeleteAutomation();

  const [editorOpen, setEditorOpen] = useState(false);
  const [editing, setEditing] = useState<Automation | null>(null);
  const [form, setForm] = useState<AutomationInput>(emptyForm);
  const [runsFor, setRunsFor] = useState<Automation | null>(null);

  const openCreate = () => {
    setEditing(null);
    setForm({ ...emptyForm, timezone: browserTimezone() });
    setEditorOpen(true);
  };

  const openEdit = (a: Automation) => {
    setEditing(a);
    setForm(automationToForm(a));
    setEditorOpen(true);
  };

  const handleSubmit = async () => {
    if (!form.name.trim() || !form.prompt_template.trim()) {
      toast.error("请填写名称与提示词模板");
      return;
    }
    if (form.trigger_type === "cron" && !form.cron_expression?.trim()) {
      toast.error("定时触发需要填写 cron 表达式");
      return;
    }
    if (form.workspace?.type === "worktree" && !form.workspace.base_ref?.trim()) {
      toast.error("新建 worktree 需要仓库当前分支，请稍候再试或改用当前检出");
      return;
    }
    try {
      if (editing) {
        await updateAutomation.mutateAsync({ id: editing.id, data: form });
        toast.success("自动化已更新（只影响后续触发）");
      } else {
        await createAutomation.mutateAsync(form);
        toast.success("自动化已创建（默认停用）");
      }
      setEditorOpen(false);
    } catch (err) {
      toast.error(errMessage(err, "保存失败"));
    }
  };

  const handleToggle = async (a: Automation) => {
    try {
      await toggleAutomation.mutateAsync({
        id: a.id,
        data: { ...automationToForm(a), enabled: !a.enabled },
      });
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
              <Button size="sm" onClick={openCreate}>
                <Plus className="mr-1.5 h-3.5 w-3.5" />
                新建自动化
              </Button>
            }
          />
        }
      />
      <div className="mx-auto w-full max-w-5xl space-y-6 p-4 md:p-6">
        <p className="text-sm text-muted-foreground">
          按 cron 或内部 Issue 事件自动在 T3 环境开线程执行提示词，
          运行结束后可自动 settle / archive 线程并发送站内通知。
        </p>

        <AutomationEditorDialog
          open={editorOpen}
          editing={editing}
          form={form}
          onFormChange={setForm}
          onClose={() => setEditorOpen(false)}
          onSubmit={handleSubmit}
          submitting={createAutomation.isPending || updateAutomation.isPending}
        />

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
                {automations.map((a) => (
                  <TableRow key={a.id}>
                    <TableCell className="font-medium">{a.name}</TableCell>
                    <TableCell className="text-sm">
                      {triggerSummary(a)}
                    </TableCell>
                    <TableCell className="text-sm">
                      {postActionSummary(a)}
                    </TableCell>
                    <TableCell className="text-sm">
                      {a.trigger_type === "cron" && a.next_run_at
                        ? formatDate(a.next_run_at)
                        : "—"}
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
                          onClick={() => openEdit(a)}
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
                ))}
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

function automationToForm(a: Automation): AutomationInput {
  return {
    name: a.name,
    enabled: a.enabled,
    environment_id: a.environment_id,
    t3_project_id: a.t3_project_id,
    trigger_type: a.trigger_type,
    cron_expression: a.cron_expression ?? "",
    timezone: a.timezone ?? "",
    issue_project_id: a.issue_project_id ?? null,
    event_filter: a.event_filter ?? null,
    prompt_template: a.prompt_template,
    model_selection: a.model_selection ?? null,
    runtime_mode: (a.runtime_mode as RuntimeMode) || "full-access",
    workspace: a.workspace ?? null,
    timeout_seconds: a.timeout_seconds,
    post_action: a.post_action,
    post_on_timeout: a.post_on_timeout,
  };
}

function triggerSummary(a: Automation): string {
  const label = automationTriggerLabel(a.trigger_type);
  if (a.trigger_type === "cron") {
    return `${label} · ${a.cron_expression ?? ""}${
      a.timezone ? `（${a.timezone}）` : ""
    }`;
  }
  return label;
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

// ---- 运行记录 ----

function AutomationRunsDialog({
  automation,
  onClose,
}: {
  automation: Automation | null;
  onClose: () => void;
}) {
  const { data: runs, isLoading } = useAutomationRuns(automation?.id ?? null);
  const currentUser = useAuthStore((s) => s.user);
  const sortedRuns = useMemo(() => runs ?? [], [runs]);

  return (
    <Dialog
      open={Boolean(automation)}
      onOpenChange={(next) => !next && onClose()}
    >
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>运行记录 · {automation?.name}</DialogTitle>
          <DialogDescription>
            最近触发的运行（最多 50 条）；线程在 T3 侧查看。
          </DialogDescription>
        </DialogHeader>
        {isLoading ? (
          <div className="space-y-2 py-4">
            {Array.from({ length: 3 }).map((_, i) => (
              <Skeleton key={i} className="h-8 rounded" />
            ))}
          </div>
        ) : sortedRuns.length === 0 ? (
          <p className="py-8 text-center text-sm text-muted-foreground">
            还没有运行记录
          </p>
        ) : (
          <div className="overflow-hidden rounded-lg border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>触发时间</TableHead>
                  <TableHead>状态</TableHead>
                  <TableHead>摘要 / 错误</TableHead>
                  <TableHead>后置</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {sortedRuns.map((run) => (
                  <TableRow key={run.id}>
                    <TableCell className="whitespace-nowrap text-sm">
                      {formatDate(run.triggered_at)}
                      <RunThreadLink run={run} currentUserId={currentUser?.id} />
                    </TableCell>
                    <TableCell className="text-sm">
                      {automationRunStatusLabel(run.status)}
                      {run.error_code ? (
                        <span className="ml-1 text-xs text-destructive">
                          {run.error_code}
                        </span>
                      ) : null}
                    </TableCell>
                    <TableCell className="max-w-[16rem] text-sm">
                      <span className="line-clamp-2">
                        {run.summary || run.error_message || "—"}
                      </span>
                    </TableCell>
                    <TableCell className="text-sm">
                      {automationPostStateLabel(run.post_state)}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

// RunThreadLink 完成的运行给出线程入口（复用既有 T3 线程路由）。
function RunThreadLink({
  run,
  currentUserId,
}: {
  run: AutomationRun;
  currentUserId?: string;
}) {
  if (!currentUserId || !run.thread_id) return null;
  return (
    <a
      className="ml-2 text-xs text-primary hover:underline"
      href={`/threads/${run.environment_id}/${run.thread_id}`}
      onClick={(e) => e.stopPropagation()}
    >
      线程
    </a>
  );
}
