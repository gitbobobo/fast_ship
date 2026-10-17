import { useState } from "react";
import { FileText, GitBranch, Laptop, Play, Settings2 } from "lucide-react";
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
  DropdownMenuItem,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
} from "@/components/ui/dropdown-menu";
import { Chip, EffortChip } from "@/components/threads/thread-chips";
import { ModelPicker } from "@/components/threads/model-picker";
import { copyWithToast } from "@/lib/copy";
import { buildIssuePrompt, normalizeIssuePrompts } from "@/lib/issue-prompt";
import {
  countBusyThreads,
  isLaunchable,
  unlaunchableReason,
} from "@/lib/issue-launch";
import {
  useIssuePromptList,
  useIssuePrompts,
  useUpdateIssuePrompts,
} from "@/lib/hooks/use-issue-prompt";
import { useThreadLaunch } from "@/lib/hooks/use-thread-launch";
import { buildModelSelection } from "@/lib/threads-compose";

export interface LaunchIssueThreadDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  projectId: string;
  issue: { id: string; reference: string; title: string };
  /** 推荐理由（仅推荐入口传入），照常附加到提示词结尾。 */
  reason?: string;
}

/**
 * 从问题启动 T3 线程的弹框。默认值按 模板偏好 → 项目绑定 + 自动负载 取；
 * 弹框关闭即卸载，状态全部随之重置。环境/模型/工作区的启动机制在
 * useThreadLaunch（与截图标注的启动对话框共用），这里只保留模板选择
 * 与偏好持久化。
 */
export function LaunchIssueThreadDialog({
  open,
  onOpenChange,
  projectId,
  issue,
  reason,
}: LaunchIssueThreadDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        {open && (
          <LaunchIssueThreadBody
            projectId={projectId}
            issue={issue}
            reason={reason}
            onClose={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  );
}

function LaunchIssueThreadBody({
  projectId,
  issue,
  reason,
  onClose,
}: {
  projectId: string;
  issue: { id: string; reference: string; title: string };
  reason?: string;
  onClose: () => void;
}) {
  const prompts = useIssuePromptList();
  const { data: promptSettings } = useIssuePrompts();
  const updatePrompts = useUpdateIssuePrompts();

  const [promptId, setPromptId] = useState("");
  const prompt = prompts.find((p) => p.id === promptId) ?? prompts[0];
  const preferences = prompt?.preferences;
  const scope = `${preferences?.environment ?? ""}:${prompt?.id ?? ""}`;

  const promptText = buildIssuePrompt({
    projectId,
    issueId: issue.id,
    content: prompt?.content ?? "",
    reason,
  });

  const launchState = useThreadLaunch({
    projectId,
    promptText,
    scope,
    preferences: preferences ?? null,
  });
  const {
    environmentsQuery,
    environments,
    rows,
    threads,
    autoRow,
    prefEnv,
    envChoice,
    setEnvPick,
    selectedRow,
    environment,
    binding,
    workspace,
    setWorkspace,
    isRepo,
    refName,
    composer,
    providers,
    modelPrefMissed,
    resolved,
    setPick,
    launching,
    hasBinding,
    bindingStale,
    canLaunch,
    goBind,
    goPairSettings,
    launch,
    launchErrorText,
  } = launchState;

  const [saveDefault, setSaveDefault] = useState(false);

  const persistPreferences = async () => {
    const modelSelection = buildModelSelection(launchState.resolved);
    const next = normalizeIssuePrompts(promptSettings?.prompts).map((p) =>
      p.id === prompt.id
        ? {
            ...p,
            preferences: {
              environment: envChoice,
              workspace,
              ...(modelSelection ? { model_selection: modelSelection } : {}),
            } satisfies IssuePromptPreferences,
          }
        : p,
    );
    await updatePrompts.mutateAsync(next);
  };

  const doLaunch = () =>
    launch(onClose, () => {
      if (saveDefault) {
        void persistPreferences().catch(() =>
          toast.error("线程已启动，但模板偏好保存失败"),
        );
      }
    });

  const copyPrompt = () =>
    void copyWithToast(promptText, "已复制提示词");

  return (
    <div
      onKeyDown={(event) => {
        if ((event.metaKey || event.ctrlKey) && event.key === "Enter") {
          event.preventDefault();
          void doLaunch();
        }
      }}
    >
      <DialogHeader>
        <DialogTitle>
          启动线程 · {issue.reference}
        </DialogTitle>
        <DialogDescription className="truncate">{issue.title}</DialogDescription>
      </DialogHeader>

      {environmentsQuery.isLoading ? (
        <p className="py-6 text-center text-sm text-muted-foreground">
          正在加载环境…
        </p>
      ) : environmentsQuery.isError || !environments ? (
        <div
          role="alert"
          className="flex items-center justify-between gap-2 rounded-lg bg-amber-500/10 px-3 py-2 text-sm text-amber-800 dark:text-amber-300"
        >
          <span>环境加载失败</span>
          <Button
            variant="outline"
            size="sm"
            onClick={() => void environmentsQuery.refetch()}
          >
            重试
          </Button>
        </div>
      ) : environments.length === 0 ? (
        <div className="rounded-lg border bg-muted/40 px-4 py-6 text-center">
          <p className="text-sm text-muted-foreground">还没有连接 T3 环境</p>
          <p className="mt-1 text-xs text-muted-foreground">
            先在设置中粘贴配对链接添加环境；仍可复制提示词手动使用。
          </p>
          <Button
            variant="outline"
            size="sm"
            className="mt-3"
            onClick={() => goPairSettings(onClose)}
          >
            去设置配对
          </Button>
        </div>
      ) : (
        <div className="grid grid-cols-[64px_minmax(0,1fr)] items-center gap-x-3 gap-y-2.5">
          <span className="text-xs text-muted-foreground">模板</span>
          <Chip
            ariaLabel="提示词模板"
            icon={<FileText className="h-3.5 w-3.5 shrink-0" />}
            label={prompt?.name ?? "默认"}
            disabled={launching || prompts.length <= 1}
          >
            <DropdownMenuRadioGroup
              value={prompt?.id ?? ""}
              onValueChange={setPromptId}
            >
              {prompts.map((item) => (
                <DropdownMenuRadioItem key={item.id} value={item.id}>
                  <span className="truncate">{item.name}</span>
                </DropdownMenuRadioItem>
              ))}
            </DropdownMenuRadioGroup>
          </Chip>

          <span className="text-xs text-muted-foreground">环境</span>
          <Chip
            ariaLabel="运行环境"
            icon={<Laptop className="h-3.5 w-3.5 shrink-0" />}
            label={
              envChoice === "auto"
                ? autoRow
                  ? `自动 → ${autoRow.environment.name} · ${countBusyThreads(threads, autoRow.environment.id)} 运行中`
                  : "自动（无可启动环境）"
                : (selectedRow?.environment.name ?? "选择环境")
            }
            disabled={launching}
          >
            <DropdownMenuRadioGroup
              value={envChoice}
              onValueChange={(value) => setEnvPick(value)}
            >
              <DropdownMenuRadioItem value="auto">
                <span className="truncate">
                  自动{autoRow ? ` → ${autoRow.environment.name}` : ""}
                </span>
                {autoRow && (
                  <span className="ml-auto text-xs text-muted-foreground">
                    {countBusyThreads(threads, autoRow.environment.id)} 运行中
                  </span>
                )}
              </DropdownMenuRadioItem>
              {rows.filter(isLaunchable).map((row) => (
                <DropdownMenuRadioItem
                  key={row.environment.id}
                  value={row.environment.id}
                >
                  <span className="truncate">{row.environment.name}</span>
                  <span className="ml-auto text-xs text-muted-foreground">
                    {countBusyThreads(threads, row.environment.id)} 运行中
                  </span>
                </DropdownMenuRadioItem>
              ))}
            </DropdownMenuRadioGroup>
            {rows.filter((row) => !isLaunchable(row)).length > 0 && (
              <>
                <DropdownMenuSeparator />
                {rows
                  .filter((row) => !isLaunchable(row))
                  .map((row) => (
                    <DropdownMenuItem key={row.environment.id} disabled>
                      <span className="truncate">{row.environment.name}</span>
                      <span className="ml-auto text-xs text-muted-foreground">
                        {row.binding ? unlaunchableReason(row) : "未绑定"}
                      </span>
                    </DropdownMenuItem>
                  ))}
              </>
            )}
            <DropdownMenuSeparator />
            <DropdownMenuItem onClick={() => goBind(onClose)}>
              <Settings2 className="h-4 w-4" />
              去绑定…
            </DropdownMenuItem>
          </Chip>

          {environment && binding ? (
            <>
              <span className="text-xs text-muted-foreground">工作区</span>
              <div className="flex min-w-0 items-center gap-2">
                <Chip
                  ariaLabel="工作区"
                  icon={<GitBranch className="h-3.5 w-3.5 shrink-0" />}
                  label={
                    workspace === "worktree" ? "New worktree" : "Current checkout"
                  }
                  disabled={launching || !isRepo}
                >
                  <DropdownMenuRadioGroup
                    value={workspace}
                    onValueChange={(value) =>
                      setWorkspace(value === "root" ? "root" : "worktree")
                    }
                  >
                    <DropdownMenuRadioItem value="worktree">
                      New worktree
                    </DropdownMenuRadioItem>
                    <DropdownMenuRadioItem value="root">
                      Current checkout
                    </DropdownMenuRadioItem>
                  </DropdownMenuRadioGroup>
                </Chip>
                {workspace === "worktree" && refName && (
                  <span className="truncate text-xs text-muted-foreground">
                    从 {refName}
                  </span>
                )}
                {!isRepo && (
                  <span className="truncate text-xs text-muted-foreground">
                    非 Git 仓库，仅主检出
                  </span>
                )}
              </div>

              <span className="text-xs text-muted-foreground">模型</span>
              <div className="flex min-w-0 flex-wrap items-center gap-1.5">
                {composer.isLoading ? (
                  <span className="text-xs text-muted-foreground">加载中…</span>
                ) : (
                  <>
                    <ModelPicker
                      providers={providers}
                      resolved={resolved}
                      disabled={launching || providers.length === 0}
                      onPick={setPick}
                    />
                    {resolved.model?.descriptors.map((descriptor) => (
                      <EffortChip
                        key={descriptor.id}
                        descriptor={descriptor}
                        resolved={resolved}
                        disabled={launching}
                        onChange={setPick}
                      />
                    ))}
                  </>
                )}
              </div>
            </>
          ) : (
            <span className="col-start-2 text-xs text-muted-foreground">
              {hasBinding
                ? "已绑定的环境当前都不可启动"
                : "该项目还没有绑定 T3 项目"}
              <button
                type="button"
                className="ml-1 underline underline-offset-2 hover:text-foreground"
                onClick={() => goBind(onClose)}
              >
                去绑定
              </button>
            </span>
          )}
        </div>
      )}

      {prefEnv.preferredMissing && envChoice === "auto" && (
        <p role="note" className="mt-3 text-xs text-muted-foreground">
          模板偏好的环境当前不可启动，已回落自动选择。
        </p>
      )}
      {modelPrefMissed && (
        <p role="note" className="mt-3 text-xs text-muted-foreground">
          模板偏好的模型在该环境不可用，已回落项目默认。
        </p>
      )}
      {bindingStale && (
        <p role="alert" className="mt-3 text-xs text-destructive">
          绑定已失效：{environment?.name} 中不存在该 T3 项目，请到项目设置重新绑定。
        </p>
      )}

      <details className="mt-3 rounded-lg border bg-muted/30 text-xs">
        <summary className="cursor-pointer select-none px-3 py-2 text-muted-foreground">
          提示词预览（只读）
        </summary>
        <pre className="max-h-48 overflow-auto whitespace-pre-wrap border-t px-3 py-2 font-mono text-[11px] leading-relaxed">
          {promptText}
        </pre>
      </details>

      <label className="mt-3 flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          className="h-4 w-4 shrink-0 rounded border border-input accent-primary"
          checked={saveDefault}
          onChange={(event) => setSaveDefault(event.target.checked)}
          disabled={!environment || launching}
        />
        保存为此模板的默认
      </label>

      {(launchState.create.isError || launchState.launchError !== null) && (
        <p role="alert" className="mt-3 text-xs text-destructive">
          {launchErrorText()}
        </p>
      )}

      <DialogFooter className="mt-4">
        <Button
          variant="outline"
          onClick={copyPrompt}
          disabled={launching}
        >
          复制提示词
        </Button>
        <Button onClick={() => void doLaunch()} disabled={!canLaunch}>
          <Play className="h-4 w-4" />
          {launching ? "启动中…" : "启动 ⌘↵"}
        </Button>
      </DialogFooter>
    </div>
  );
}
