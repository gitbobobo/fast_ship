import { useEffect, useMemo, useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { Ellipsis } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import {
  OptionSelect,
  withMissingOption,
} from "@/components/ui/option-select";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
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
import { Header } from "@/components/layout/header";
import { HeaderActions } from "@/components/layout/header-actions";
import { environmentApi, threadApi } from "@/lib/api/threads";
import { ApiRequestError } from "@/lib/api/client";
import {
  type Automation,
  type AutomationTriggerType,
  type RuntimeMode,
} from "@/lib/api/automations";
import {
  buildModelSelection,
  parseProviders,
  resolveModel,
  type ModelPick,
} from "@/lib/threads-compose";
import { T3ProjectPicker } from "@/components/threads/t3-project-picker";
import { ModelPicker } from "@/components/threads/model-picker";
import {
  useAutomation,
  useCreateAutomation,
  useDeleteAutomation,
  useUpdateAutomation,
} from "@/lib/hooks/use-automations";
import { AddTriggerMenu } from "./add-trigger-menu";
import { TriggerCard } from "./trigger-card";
import { AutomationRunsTable } from "./runs-table";
import {
  MAX_TRIGGERS,
  POST_ACTION_OPTIONS,
  POST_ON_TIMEOUT_OPTIONS,
  RUNTIME_MODE_OPTIONS,
  TIMEOUT_OPTIONS,
  automationFormToInput,
  automationToForm,
  emptyAutomationForm,
  emptyTriggerForm,
  isPRTrigger,
  toAutomationModelSelection,
  type AutomationForm,
  type TriggerForm,
} from "@/lib/automations/form-model";

// 自动化新建/编辑独立页（INT-118）：/automations/new 与 /automations/:id。
// 头部面包屑 + 保存/「…」菜单；设置分页承载触发器卡片与其余配置项，
// 运行记录分页只在编辑已有自动化时出现。

export default function AutomationEditorPage() {
  const { id } = useParams();
  const automationId = id ?? null;
  const { data: automation, isLoading } = useAutomation(automationId);

  if (automationId && isLoading) {
    return (
      <div className="flex flex-col">
        <Header title="自动化" />
        <div className="mx-auto w-full max-w-3xl space-y-3 p-4 md:p-6">
          <Skeleton className="h-8 w-1/3" />
          <Skeleton className="h-40 rounded-lg" />
        </div>
      </div>
    );
  }

  if (automationId && !automation) {
    return (
      <div className="flex flex-col">
        <Header title="自动化" />
        <div className="mx-auto w-full max-w-3xl p-4 md:p-6">
          <div className="space-y-4 rounded-lg border bg-muted/40 p-10 text-center">
            <p className="text-sm text-muted-foreground">
              自动化不存在或已被删除
            </p>
            <div>
              <Button
                variant="outline"
                size="sm"
                render={<Link to="/automations" />}
              >
                返回自动化列表
              </Button>
            </div>
          </div>
        </div>
      </div>
    );
  }

  // key 固定为自动化 id：保存后的详情 refetch 不会重挂载、不会清掉表单输入。
  return (
    <AutomationEditor
      key={automation?.id ?? "new"}
      automation={automation ?? null}
    />
  );
}

function AutomationEditor({ automation }: { automation: Automation | null }) {
  const isNew = !automation;
  const navigate = useNavigate();
  const [form, setForm] = useState<AutomationForm>(() =>
    automation ? automationToForm(automation) : emptyAutomationForm(),
  );
  const [deleteOpen, setDeleteOpen] = useState(false);

  const createAutomation = useCreateAutomation();
  const updateAutomation = useUpdateAutomation();
  const deleteAutomation = useDeleteAutomation();
  const submitting = createAutomation.isPending || updateAutomation.isPending;

  const set = <K extends keyof AutomationForm>(
    key: K,
    value: AutomationForm[K],
  ) => setForm((f) => ({ ...f, [key]: value }));

  const {
    data: environments = [],
    isLoading: environmentsLoading,
    isError: environmentsError,
  } = useQuery({
    queryKey: ["automations", "env-options"],
    queryFn: async () => (await environmentApi.list()).data.items,
  });

  const {
    data: composer,
    isLoading: composerLoading,
    isError: composerError,
  } = useQuery({
    queryKey: ["automations", "composer", form.environment_id],
    queryFn: async () => (await threadApi.composerOptions(form.environment_id)).data,
    enabled: Boolean(form.environment_id),
  });

  const { data: workspaceOpts } = useQuery({
    queryKey: [
      "automations",
      "workspace",
      form.environment_id,
      form.t3_project_id,
    ],
    queryFn: async () =>
      (
        await threadApi.workspaceOptions(
          form.environment_id,
          form.t3_project_id,
          "",
        )
      ).data,
    enabled: Boolean(form.environment_id && form.t3_project_id),
  });

  const projects = composer?.projects ?? [];
  const providers = useMemo(
    () => parseProviders(composer?.config),
    [composer?.config],
  );
  const t3Project = projects.find((p) => p.id === form.t3_project_id);
  const modelPick: ModelPick | null = form.model_selection
    ? {
        instanceId: form.model_selection.instance_id,
        model: form.model_selection.model,
        efforts: Object.fromEntries(
          (form.model_selection.options ?? [])
            .filter((o) => typeof o.value === "string")
            .map((o) => [o.id, String(o.value)]),
        ),
      }
    : null;
  const modelResolved = resolveModel(providers, t3Project, modelPick);
  // 已选模型在当前 providers 里找不到（换环境/项目或快照过期）时标记失效，
  // 收起态显示 `xx（已失效）` 而不是回落模型的名字。
  const selection = form.model_selection;
  const modelMissing = Boolean(
    selection &&
      !providers.some(
        (p) =>
          p.instanceId === selection.instance_id &&
          p.models.some((m) => m.slug === selection.model),
      ),
  );
  const workspaceKind = form.workspace?.type ?? "root";
  const canWorktree =
    workspaceOpts?.is_repo !== false && Boolean(workspaceOpts?.ref_name);

  useEffect(() => {
    if (form.workspace?.type !== "worktree") return;
    const ref = workspaceOpts?.ref_name?.trim();
    if (!ref || form.workspace.base_ref === ref) return;
    if (form.workspace.base_ref) return;
    setForm((f) => ({
      ...f,
      workspace: { ...f.workspace!, base_ref: ref },
    }));
  }, [form, workspaceOpts?.ref_name]);

  // ---- 触发器增删改 ----

  const addTrigger = (type: AutomationTriggerType) => {
    if (form.triggers.length >= MAX_TRIGGERS) {
      toast.error(`触发器最多 ${MAX_TRIGGERS} 个`);
      return;
    }
    setForm((f) => ({
      ...f,
      triggers: [...f.triggers, emptyTriggerForm(type)],
    }));
  };

  const updateTrigger = (key: string, next: TriggerForm) =>
    setForm((f) => ({
      ...f,
      triggers: f.triggers.map((t) => (t.key === key ? next : t)),
    }));

  const removeTrigger = (key: string) =>
    setForm((f) => ({
      ...f,
      triggers: f.triggers.filter((t) => t.key !== key),
    }));

  // ---- 提交 / 删除 ----

  const validate = (): boolean => {
    if (!form.name.trim() || !form.prompt_template.trim()) {
      toast.error("请填写名称与提示词模板");
      return false;
    }
    if (form.enabled && form.triggers.length === 0) {
      toast.error("启用状态下至少需要一个触发器");
      return false;
    }
    for (const trigger of form.triggers) {
      if (
        trigger.trigger_type === "cron" &&
        !trigger.cron_expression.trim()
      ) {
        toast.error("定时触发需要填写 cron 表达式");
        return false;
      }
      if (isPRTrigger(trigger.trigger_type)) {
        if (!trigger.model_selections.length) {
          toast.error("GitHub PR 触发必须至少选择一个模型");
          return false;
        }
        if (trigger.model_selections.length > 10) {
          toast.error("最多选择 10 个模型");
          return false;
        }
        const debounce = trigger.debounce_seconds;
        if (debounce < 0 || debounce > 86400) {
          toast.error("防抖窗口须在 0 到 86400 秒之间（0=立即）");
          return false;
        }
      }
    }
    if (form.workspace?.type === "worktree" && !form.workspace.base_ref?.trim()) {
      toast.error("新建 worktree 需要仓库当前分支，请稍候再试或改用当前检出");
      return false;
    }
    return true;
  };

  const handleSubmit = async () => {
    if (!validate()) return;
    // 提交时快照触发器的本地 key 顺序：请求进行中用户仍可增删/编辑卡片，
    // 响应回来时只能按这份快照对齐，不能按「当前表单位置」对齐。
    const submittedKeys = form.triggers.map((t) => t.key);
    const data = automationFormToInput(form);
    try {
      if (automation) {
        const res = await updateAutomation.mutateAsync({
          id: automation.id,
          data,
        });
        // 回写服务端返回的触发器 id（响应第 i 项 = 提交时第 i 个 key），
        // 新建的触发器下次保存走更新而不是再建一条。请求期间已被删掉的
        // key 直接忽略，否则会把 id 写到别的卡片上。
        const saved = res.data.triggers ?? [];
        const idByKey = new Map<string, string>();
        submittedKeys.forEach((key, i) => {
          const savedId = saved[i]?.id;
          if (savedId) idByKey.set(key, savedId);
        });
        setForm((f) => ({
          ...f,
          triggers: f.triggers.map((t) => {
            const savedId = idByKey.get(t.key);
            return savedId ? { ...t, id: savedId } : t;
          }),
        }));
        toast.success("自动化已更新（只影响后续触发）");
      } else {
        await createAutomation.mutateAsync(data);
        toast.success("自动化已创建");
        navigate("/automations");
      }
    } catch (err) {
      toast.error(errMessage(err, "保存失败"));
    }
  };

  const handleDelete = async () => {
    if (!automation) return;
    try {
      await deleteAutomation.mutateAsync(automation.id);
      toast.success("自动化已删除");
      navigate("/automations");
    } catch (err) {
      setDeleteOpen(false);
      toast.error(errMessage(err, "删除失败"));
    }
  };

  // 换环境/项目要清空下游字段：T3 项目、模型、工作区，
  // 以及各 PR 触发器的模型选择（模型属于环境上下文）。
  const clearTriggerModels = (triggers: TriggerForm[]) =>
    triggers.map((t) =>
      t.model_selections.length ? { ...t, model_selections: [] } : t,
    );

  const settings = (
    <div className="space-y-6">
      <section className="space-y-2">
        <h2 className="text-sm font-medium text-muted-foreground">触发器</h2>
        <div className="space-y-2">
          {form.triggers.map((trigger) => (
            <TriggerCard
              key={trigger.key}
              trigger={trigger}
              providers={providers}
              t3Project={t3Project}
              onChange={(next) => updateTrigger(trigger.key, next)}
              onRemove={() => removeTrigger(trigger.key)}
            />
          ))}
          <AddTriggerMenu
            onAdd={addTrigger}
            disabled={form.triggers.length >= MAX_TRIGGERS}
          />
        </div>
      </section>

      <section className="space-y-2">
        <Label htmlFor="automation-prompt">提示词模板</Label>
        <Textarea
          id="automation-prompt"
          rows={6}
          value={form.prompt_template}
          placeholder="支持占位符 {{issue_id}}、{{issue_title}}、{{issue_body}}；事件触发时替换。"
          onChange={(e) => set("prompt_template", e.target.value)}
        />
      </section>

      <section className="grid grid-cols-2 gap-3">
        <div className="space-y-2">
          <Label>模型</Label>
          <ModelPicker
            providers={providers}
            resolved={modelResolved}
            // 有显式选择时即使 providers 没能加载也要能改回「项目默认」
            // （composer-options 失败 / 无可用 provider）。
            disabled={providers.length === 0 && !selection}
            className="w-full max-w-none"
            leadingOption={{
              label: "项目默认",
              selected: !selection,
              onSelect: () => set("model_selection", null),
            }}
            triggerLabel={
              composerLoading
                ? "加载中…"
                : composerError
                  ? "加载失败，请重试"
                  : modelMissing
                    ? `${selection?.model}（已失效）`
                    : undefined
            }
            onPick={(pick) => {
              const resolved = resolveModel(providers, t3Project, pick);
              set(
                "model_selection",
                toAutomationModelSelection(buildModelSelection(resolved)),
              );
            }}
          />
          <p className="text-xs text-muted-foreground">
            cron / Issue 事件触发使用；留空用 T3 项目默认模型。PR
            触发器的模型在各自触发器里配置。
          </p>
        </div>
        <div className="space-y-2">
          <Label>工作区</Label>
          <OptionSelect
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
            options={[
              { value: "root", label: "当前检出（默认）" },
              {
                value: "worktree",
                label: "新建 worktree",
                disabled: !canWorktree,
              },
              ...(workspaceKind === "existing_worktree"
                ? [{ value: "existing_worktree", label: "已有 worktree" }]
                : []),
            ]}
            className="w-full"
          />
          <p className="text-xs text-muted-foreground">
            {workspaceKind === "worktree"
              ? `基线分支 ${form.workspace?.base_ref || "（等待仓库信息）"}`
              : "留空按项目当前检出启动线程。"}
          </p>
        </div>
      </section>

      <section className="grid grid-cols-2 gap-3">
        <div className="space-y-2">
          <Label>超时时长</Label>
          <OptionSelect
            value={String(form.timeout_seconds)}
            onValueChange={(next) => set("timeout_seconds", Number(next))}
            options={withMissingOption(
              TIMEOUT_OPTIONS,
              String(form.timeout_seconds),
              `${form.timeout_seconds} 秒`,
            )}
            className="w-full"
          />
        </div>
        <div className="space-y-2">
          <Label>运行模式</Label>
          <OptionSelect
            value={form.runtime_mode}
            onValueChange={(next) => set("runtime_mode", next as RuntimeMode)}
            options={withMissingOption(
              RUNTIME_MODE_OPTIONS,
              form.runtime_mode,
              `${form.runtime_mode}（未知）`,
            )}
            className="w-full"
          />
        </div>
      </section>

      <section className="grid grid-cols-2 gap-3">
        <div className="space-y-2">
          <Label>结束后置操作</Label>
          <OptionSelect
            value={form.post_action ?? "none"}
            onValueChange={(next) =>
              set("post_action", next as AutomationForm["post_action"])
            }
            options={POST_ACTION_OPTIONS}
            className="w-full"
          />
          {form.post_action === "archive" && (
            <p className="text-xs text-muted-foreground">
              archive 会终止仍在活动的线程。
            </p>
          )}
        </div>
        <div className="space-y-2">
          <Label>超时后仍执行后置</Label>
          <OptionSelect
            value={form.post_on_timeout ? "yes" : "no"}
            onValueChange={(next) => set("post_on_timeout", next === "yes")}
            options={POST_ON_TIMEOUT_OPTIONS}
            className="w-full"
          />
        </div>
      </section>
    </div>
  );

  return (
    <div className="flex flex-col">
      <Header
        title={
          <span className="flex min-w-0 items-center gap-1.5">
            <Link
              to="/automations"
              className="shrink-0 font-normal text-muted-foreground transition-colors hover:text-foreground"
            >
              自动化
            </Link>
            <span className="shrink-0 text-muted-foreground">/</span>
            <span className="truncate">{form.name.trim() || "未命名"}</span>
          </span>
        }
        actions={
          <HeaderActions
            primary={
              <div className="flex items-center gap-2">
                <Button size="sm" onClick={handleSubmit} disabled={submitting}>
                  {submitting ? "保存中..." : "保存"}
                </Button>
                <DropdownMenu>
                  <DropdownMenuTrigger
                    render={
                      <Button
                        variant="outline"
                        size="icon-sm"
                        aria-label="更多操作"
                      />
                    }
                  >
                    <Ellipsis className="h-4 w-4" />
                  </DropdownMenuTrigger>
                  <DropdownMenuContent align="end" className="w-44">
                    <DropdownMenuItem
                      onClick={() => set("enabled", !form.enabled)}
                    >
                      {form.enabled ? "停用" : "启用"}
                    </DropdownMenuItem>
                    {!isNew && (
                      <DropdownMenuItem
                        variant="destructive"
                        onClick={() => setDeleteOpen(true)}
                      >
                        删除
                      </DropdownMenuItem>
                    )}
                  </DropdownMenuContent>
                </DropdownMenu>
              </div>
            }
          />
        }
      />

      <div className="mx-auto w-full max-w-3xl space-y-6 p-4 md:p-6">
        <div className="space-y-3">
          <Input
            aria-label="名称"
            value={form.name}
            placeholder="例如 每日令牌回报"
            onChange={(e) => set("name", e.target.value)}
            className="h-11 border-transparent bg-transparent px-0 text-xl font-semibold shadow-none focus-visible:ring-0"
          />
          <div className="flex flex-wrap items-center gap-3">
            <div className="flex items-center gap-2">
              <Switch
                checked={form.enabled}
                onCheckedChange={(next) => set("enabled", next)}
                aria-label="启用状态"
              />
              <span className="text-sm text-muted-foreground">
                {form.enabled ? "已启用" : "已停用"}
              </span>
            </div>
            <span className="h-4 w-px bg-border" aria-hidden />
            <OptionSelect
              aria-label="T3 环境"
              value={form.environment_id}
              onValueChange={(next) =>
                setForm((f) => ({
                  ...f,
                  environment_id: next ?? "",
                  t3_project_id: "",
                  model_selection: null,
                  workspace: null,
                  triggers: clearTriggerModels(f.triggers),
                }))
              }
              options={withMissingOption(
                environments.map((env) => ({
                  value: env.id,
                  label: env.name,
                })),
                form.environment_id,
                environmentsLoading
                  ? "加载中…"
                  : environmentsError
                    ? "加载失败，请重试"
                    : `${form.environment_id}（已失效）`,
              )}
              placeholder="选择环境"
              className="w-48"
            />
            <T3ProjectPicker
              projects={projects}
              value={form.t3_project_id}
              onValueChange={(next) =>
                setForm((f) => ({
                  ...f,
                  t3_project_id: next,
                  model_selection: null,
                  workspace: null,
                  triggers: clearTriggerModels(f.triggers),
                }))
              }
              ariaLabel="T3 项目"
              placeholder="选择项目"
              className="max-w-64"
              triggerLabel={
                composerLoading
                  ? "加载中…"
                  : composerError
                    ? "加载失败，请重试"
                    : form.t3_project_id && !t3Project
                      ? `${form.t3_project_id}（已失效）`
                      : undefined
              }
            />
          </div>
        </div>

        {isNew ? (
          settings
        ) : (
          <Tabs defaultValue="settings" className="space-y-4">
            <TabsList>
              <TabsTrigger value="settings">设置</TabsTrigger>
              <TabsTrigger value="runs">运行记录</TabsTrigger>
            </TabsList>
            <TabsContent value="settings">{settings}</TabsContent>
            <TabsContent value="runs">
              <AutomationRunsTable automationId={automation.id} />
            </TabsContent>
          </Tabs>
        )}
      </div>

      <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
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
            <AlertDialogAction onClick={handleDelete}>
              确认删除
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function errMessage(err: unknown, fallback: string): string {
  if (err instanceof ApiRequestError && err.message) return err.message;
  return fallback;
}
