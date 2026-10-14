import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Plus, History, Pencil, Trash2 } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
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
import { environmentApi, threadApi } from "@/lib/api/threads";
import { useProjects } from "@/lib/hooks/use-projects";
import { useAuthStore } from "@/lib/store/auth-store";
import {
  automationPostStateLabel,
  automationRunStatusLabel,
  automationTriggerLabel,
  type Automation,
  type AutomationInput,
  type AutomationModelSelection,
  type AutomationRun,
  type AutomationTriggerType,
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
import {
  buildModelSelection,
  parseProviders,
  resolveModel,
} from "@/lib/threads-compose";

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

const browserTimezone = () => Intl.DateTimeFormat().resolvedOptions().timeZone;

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

function toAutomationModelSelection(
  sel: ReturnType<typeof buildModelSelection>,
): AutomationModelSelection | null {
  if (!sel) return null;
  const options = sel.options
    ?.filter((o) => typeof o.value === "string")
    .map((o) => ({ id: o.id, value: String(o.value) }));
  return {
    instance_id: sel.instance_id,
    model: sel.model,
    ...(options?.length ? { options } : {}),
  };
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

// ---- 新建/编辑 ----

function AutomationEditorDialog({
  open,
  editing,
  form,
  onFormChange,
  onClose,
  onSubmit,
  submitting,
}: {
  open: boolean;
  editing: Automation | null;
  form: AutomationInput;
  onFormChange: (form: AutomationInput) => void;
  onClose: () => void;
  onSubmit: () => void;
  submitting: boolean;
}) {
  const set = <K extends keyof AutomationInput>(key: K, value: AutomationInput[K]) =>
    onFormChange({ ...form, [key]: value });

  const { data: environments = [] } = useQuery({
    queryKey: ["automations", "env-options"],
    queryFn: async () => (await environmentApi.list()).data.items,
    enabled: open,
  });

  const { data: composer } = useQuery({
    queryKey: ["automations", "composer", form.environment_id],
    queryFn: async () =>
      (
        await threadApi.composerOptions(form.environment_id)
      ).data,
    enabled: open && Boolean(form.environment_id),
  });

  const { data: workspaceOpts } = useQuery({
    queryKey: ["automations", "workspace", form.environment_id, form.t3_project_id],
    queryFn: async () =>
      (await threadApi.workspaceOptions(form.environment_id, form.t3_project_id, "")).data,
    enabled: open && Boolean(form.environment_id && form.t3_project_id),
  });

  const projects = composer?.projects ?? [];
  const providers = useMemo(
    () => parseProviders(composer?.config),
    [composer?.config],
  );
  const t3Project = projects.find((p) => p.id === form.t3_project_id);
  const modelKey = form.model_selection
    ? `${form.model_selection.instance_id}::${form.model_selection.model}`
    : "default";
  const workspaceKind = form.workspace?.type ?? "root";
  const canWorktree = workspaceOpts?.is_repo !== false && Boolean(workspaceOpts?.ref_name);

  useEffect(() => {
    if (form.workspace?.type !== "worktree") return;
    const ref = workspaceOpts?.ref_name?.trim();
    if (!ref || form.workspace.base_ref === ref) return;
    if (form.workspace.base_ref) return;
    onFormChange({
      ...form,
      workspace: { ...form.workspace, base_ref: ref },
    });
  }, [form, onFormChange, workspaceOpts?.ref_name]);

  const filter = form.event_filter ?? {};

  return (
    <Dialog open={open} onOpenChange={(next) => !next && onClose()}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{editing ? "编辑自动化" : "新建自动化"}</DialogTitle>
          <DialogDescription>
            {editing
              ? "更新只影响后续触发，进行中的运行按原配置执行。"
              : "新建默认停用；启用前需在服务端配置 public_url。"}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-2">
          <div className="space-y-2">
            <Label htmlFor="automation-name">名称</Label>
            <Input
              id="automation-name"
              value={form.name}
              placeholder="例如 每日令牌回报"
              onChange={(e) => set("name", e.target.value)}
            />
          </div>

          <div className="space-y-2">
            <Label>触发方式</Label>
            <Select
              value={form.trigger_type}
              onValueChange={(next) =>
                set("trigger_type", next as AutomationTriggerType)
              }
            >
              <SelectTrigger className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="cron">定时（cron）</SelectItem>
                <SelectItem value="issue_created">Issue 新建</SelectItem>
                <SelectItem value="issue_status_changed">
                  Issue 状态变化
                </SelectItem>
                <SelectItem value="issue_label_added">Issue 加标签</SelectItem>
              </SelectContent>
            </Select>
          </div>

          {form.trigger_type === "cron" ? (
            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-2">
                <Label htmlFor="automation-cron">cron 表达式</Label>
                <Input
                  id="automation-cron"
                  value={form.cron_expression ?? ""}
                  placeholder="*/10 * * * *"
                  onChange={(e) => set("cron_expression", e.target.value)}
                />
                <p className="text-xs text-muted-foreground">
                  标准五字段：分 时 日 月 周；不支持秒与 @every。
                </p>
              </div>
              <div className="space-y-2">
                <Label htmlFor="automation-tz">时区</Label>
                <Input
                  id="automation-tz"
                  value={form.timezone ?? ""}
                  placeholder={browserTimezone()}
                  onChange={(e) => set("timezone", e.target.value)}
                />
                <p className="text-xs text-muted-foreground">
                  IANA 名称，留空按 UTC。
                </p>
              </div>
            </div>
          ) : (
            <EventFilterFields
              triggerType={form.trigger_type}
              filter={filter}
              onChange={(next) => set("event_filter", next)}
            />
          )}

          {form.trigger_type !== "cron" && (
            <div className="space-y-2">
              <Label>限定 Fast Ship 项目（可选）</Label>
              <IssueProjectSelect
                value={form.issue_project_id ?? ""}
                onChange={(next) =>
                  set("issue_project_id", next === "" ? null : next)
                }
              />
              <p className="text-xs text-muted-foreground">
                留空表示当前用户全部项目的事件都触发。
              </p>
            </div>
          )}

          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-2">
              <Label>T3 环境</Label>
              <Select
                value={form.environment_id}
                onValueChange={(next) =>
                  onFormChange({
                    ...form,
                    environment_id: next ?? "",
                    t3_project_id: "",
                    model_selection: null,
                    workspace: null,
                  })
                }
              >
                <SelectTrigger className="w-full">
                  <SelectValue placeholder="选择环境" />
                </SelectTrigger>
                <SelectContent>
                  {environments.map((env) => (
                    <SelectItem key={env.id} value={env.id}>
                      {env.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-2">
              <Label>T3 项目</Label>
              <Select
                value={form.t3_project_id}
                onValueChange={(next) =>
                  onFormChange({
                    ...form,
                    t3_project_id: next ?? "",
                    model_selection: null,
                    workspace: null,
                  })
                }
              >
                <SelectTrigger className="w-full">
                  <SelectValue placeholder="选择项目" />
                </SelectTrigger>
                <SelectContent>
                  {projects.map((project) => (
                    <SelectItem key={project.id} value={project.id}>
                      {project.title}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-2">
              <Label>模型</Label>
              <Select
                value={modelKey}
                onValueChange={(next) => {
                  if (!next || next === "default") {
                    set("model_selection", null);
                    return;
                  }
                  const sep = next.indexOf("::");
                  const instanceId = sep >= 0 ? next.slice(0, sep) : next;
                  const model = sep >= 0 ? next.slice(sep + 2) : "";
                  const resolved = resolveModel(providers, t3Project, {
                    instanceId,
                    model,
                    efforts: {},
                  });
                  set(
                    "model_selection",
                    toAutomationModelSelection(buildModelSelection(resolved)),
                  );
                }}
              >
                <SelectTrigger className="w-full">
                  <SelectValue placeholder="项目默认" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="default">项目默认</SelectItem>
                  {providers.flatMap((provider) =>
                    provider.models.map((model) => (
                      <SelectItem
                        key={`${provider.instanceId}::${model.slug}`}
                        value={`${provider.instanceId}::${model.slug}`}
                      >
                        {provider.name} / {model.name}
                      </SelectItem>
                    )),
                  )}
                </SelectContent>
              </Select>
              <p className="text-xs text-muted-foreground">
                选项来自该环境 composer-options；留空用 T3 项目默认模型。
              </p>
            </div>
            <div className="space-y-2">
              <Label>工作区</Label>
              <Select
                value={workspaceKind}
                onValueChange={(next) => {
                  if (next === "worktree") {
                    set("workspace", {
                      type: "worktree",
                      base_ref: workspaceOpts?.ref_name ?? "",
                    });
                    return;
                  }
                  if (next === "existing_worktree") {
                    return;
                  }
                  set("workspace", null);
                }}
              >
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="root">当前检出（默认）</SelectItem>
                  <SelectItem value="worktree" disabled={!canWorktree}>
                    新建 worktree
                  </SelectItem>
                  {workspaceKind === "existing_worktree" && (
                    <SelectItem value="existing_worktree">
                      已有 worktree
                    </SelectItem>
                  )}
                </SelectContent>
              </Select>
              <p className="text-xs text-muted-foreground">
                {workspaceKind === "worktree"
                  ? `基线分支 ${form.workspace?.base_ref || "（等待仓库信息）"}`
                  : "留空按项目当前检出启动线程。"}
              </p>
            </div>
          </div>

          <div className="space-y-2">
            <Label htmlFor="automation-prompt">提示词模板</Label>
            <Textarea
              id="automation-prompt"
              rows={5}
              value={form.prompt_template}
              placeholder="支持占位符 {{issue_id}}、{{issue_title}}、{{issue_body}}；事件触发时替换。"
              onChange={(e) => set("prompt_template", e.target.value)}
            />
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-2">
              <Label>超时时长</Label>
              <Select
                value={String(form.timeout_seconds ?? 3600)}
                onValueChange={(next) => set("timeout_seconds", Number(next))}
              >
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="300">5 分钟</SelectItem>
                  <SelectItem value="900">15 分钟</SelectItem>
                  <SelectItem value="3600">1 小时（默认）</SelectItem>
                  <SelectItem value="14400">4 小时</SelectItem>
                  <SelectItem value="86400">24 小时</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-2">
              <Label>运行模式</Label>
              <Select
                value={form.runtime_mode ?? "full-access"}
                onValueChange={(next) => set("runtime_mode", next as RuntimeMode)}
              >
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="full-access">full-access（默认）</SelectItem>
                  <SelectItem value="auto">auto</SelectItem>
                  <SelectItem value="auto-accept-edits">
                    auto-accept-edits
                  </SelectItem>
                  <SelectItem value="approval-required">
                    approval-required
                  </SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-2">
              <Label>结束后置操作</Label>
              <Select
                value={form.post_action ?? "none"}
                onValueChange={(next) =>
                  set("post_action", next as AutomationInput["post_action"])
                }
              >
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="none">无（默认）</SelectItem>
                  <SelectItem value="settle">settle（收进历史区）</SelectItem>
                  <SelectItem value="archive">archive（归档）</SelectItem>
                </SelectContent>
              </Select>
              {form.post_action === "archive" && (
                <p className="text-xs text-muted-foreground">
                  archive 会终止仍在活动的线程。
                </p>
              )}
            </div>
            <div className="space-y-2">
              <Label>超时后仍执行后置</Label>
              <Select
                value={form.post_on_timeout ? "yes" : "no"}
                onValueChange={(next) => set("post_on_timeout", next === "yes")}
              >
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="no">否（默认）</SelectItem>
                  <SelectItem value="yes">是</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>

          <div className="space-y-2">
            <Label>启用状态</Label>
            <Select
              value={form.enabled ? "on" : "off"}
              onValueChange={(next) => set("enabled", next === "on")}
            >
              <SelectTrigger className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="off">停用（默认）</SelectItem>
                <SelectItem value="on">启用</SelectItem>
              </SelectContent>
            </Select>
          </div>
        </div>

        <DialogFooter>
          <Button type="button" variant="outline" onClick={onClose}>
            取消
          </Button>
          <Button type="button" onClick={onSubmit} disabled={submitting}>
            {submitting ? "保存中..." : "保存"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// EventFilterFields 事件过滤的可选输入：无过滤 = 该类型全部真实事件。
function EventFilterFields({
  triggerType,
  filter,
  onChange,
}: {
  triggerType: AutomationTriggerType;
  filter: NonNullable<AutomationInput["event_filter"]>;
  onChange: (filter: AutomationInput["event_filter"]) => void;
}) {
  if (triggerType === "issue_created") {
    return (
      <p className="text-xs text-muted-foreground">
        Issue 新建事件不做事件字段过滤；项目范围用下方「限定 Fast Ship 项目」控制。
      </p>
    );
  }
  if (triggerType === "issue_status_changed") {
    return (
      <div className="grid grid-cols-3 gap-3">
        <div className="space-y-2">
          <Label>字段</Label>
          <Select
            value={filter.field ?? ""}
            onValueChange={(next) =>
              onChange({
                ...filter,
                field: next === "" ? undefined : (next as "state" | "workflow_status"),
              })
            }
          >
            <SelectTrigger className="w-full">
              <SelectValue placeholder="任意" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="">任意</SelectItem>
              <SelectItem value="state">state（开关状态）</SelectItem>
              <SelectItem value="workflow_status">workflow_status</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div className="space-y-2">
          <Label>从（可选）</Label>
          <Input
            value={filter.from ?? ""}
            placeholder="open / todo"
            onChange={(e) => onChange({ ...filter, from: e.target.value })}
          />
        </div>
        <div className="space-y-2">
          <Label>到（可选）</Label>
          <Input
            value={filter.to ?? ""}
            placeholder="closed / done"
            onChange={(e) => onChange({ ...filter, to: e.target.value })}
          />
        </div>
      </div>
    );
  }
  return (
    <div className="space-y-2">
      <Label htmlFor="automation-labels">匹配标签（可选，逗号分隔）</Label>
      <Input
        id="automation-labels"
        value={(filter.labels ?? []).join(", ")}
        placeholder="留空匹配全部加标签事件"
        onChange={(e) =>
          onChange({
            ...filter,
            labels: e.target.value
              .split(",")
              .map((s) => s.trim())
              .filter(Boolean),
          })
        }
      />
    </div>
  );
}

// IssueProjectSelect 事件触发的可选项目范围（不依赖 INT-89 项目绑定）。
function IssueProjectSelect({
  value,
  onChange,
}: {
  value: string;
  onChange: (value: string) => void;
}) {
  const { data: projects } = useProjects();
  const items = projects?.items ?? [];
  return (
    <Select value={value} onValueChange={(next) => onChange(next ?? "")}>
      <SelectTrigger className="w-full">
        <SelectValue placeholder="全部项目" />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="">全部项目</SelectItem>
        {items.map((project) => (
          <SelectItem key={project.id} value={project.id}>
            {project.name}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
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
