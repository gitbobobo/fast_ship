import { useState } from "react";
import { Copy, GitBranch, Laptop, Play } from "lucide-react";
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
import { Chip, EffortChip, ModelChip } from "@/components/threads/thread-chips";
import { copyWithToast } from "@/lib/copy";
import {
  countBusyThreads,
  isLaunchable,
  unlaunchableReason,
} from "@/lib/issue-launch";
import { useThreadLaunch } from "@/lib/hooks/use-thread-launch";

export interface LaunchAnnotationThreadDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  projectId: string;
  /** 已填充好的固定提示词（annotation-prompt.ts 产物） */
  promptText: string;
  /** 启动前的二次确认文案里的数量（当前范围 open 标注数） */
  openCount: number;
}

/**
 * 处理截图标注意见的启动弹框：复用 launch-issue-thread-dialog 的
 * 环境/模型/工作区模式，提示词来自 annotation-prompt 固定模板、预览只读。
 * 启动前已由入口按钮做过一次数量二次确认；失败时错误留在弹框内，
 * 「复制提示词」始终可用作为退路。
 */
export function LaunchAnnotationThreadDialog({
  open,
  onOpenChange,
  projectId,
  promptText,
  openCount,
}: LaunchAnnotationThreadDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        {open && (
          <LaunchAnnotationThreadBody
            projectId={projectId}
            promptText={promptText}
            openCount={openCount}
            onClose={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  );
}

function LaunchAnnotationThreadBody({
  projectId,
  promptText,
  openCount,
  onClose,
}: {
  projectId: string;
  promptText: string;
  openCount: number;
  onClose: () => void;
}) {
  // 无模板偏好：作用域按环境隔离，偏好留空
  const launchState = useThreadLaunch({
    projectId,
    promptText,
    scope: "",
    preferences: null,
  });
  const {
    environmentsQuery,
    environments,
    rows,
    threads,
    autoRow,
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
  const [confirming, setConfirming] = useState(true);

  const copyPrompt = () => void copyWithToast(promptText, "已复制提示词");
  const doLaunch = () => launch(onClose);

  return (
    <div
      onKeyDown={(event) => {
        if ((event.metaKey || event.ctrlKey) && event.key === "Enter") {
          event.preventDefault();
          // 与按钮共用状态门禁：确认步骤未过或环境/绑定未就绪时快捷键不启动
          if (confirming || !canLaunch) return;
          void doLaunch();
        }
      }}
    >
      <DialogHeader>
        <DialogTitle>启动线程处理标注意见</DialogTitle>
        <DialogDescription className="truncate">
          将启动一个 Agent 线程逐条处理当前范围内的 {openCount} 条未解决标注
        </DialogDescription>
      </DialogHeader>

      {confirming ? (
        <div className="space-y-3 text-sm">
          <p>
            当前筛选范围内有{" "}
            <span className="font-medium">{openCount}</span>{" "}
            条未解决标注。Agent 会按提示词逐条查看、修复并把已修复的标注置为已解决；
            修不了的和拿不准的会保持原样并在结束时汇报。
          </p>
          <p className="text-xs text-muted-foreground">
            全程不会提交代码、不会创建 PR。确认后可在下一步选择运行环境与模型。
          </p>
        </div>
      ) : environmentsQuery.isLoading ? (
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
                    workspace === "worktree"
                      ? "New worktree"
                      : "Current checkout"
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
                    <ModelChip
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

      {(launchState.create.isError || launchState.launchError !== null) && (
        <p role="alert" className="mt-3 text-xs text-destructive">
          {launchErrorText()}
          。也可以直接复制提示词，到任意 Agent 环境手动执行。
        </p>
      )}

      <DialogFooter className="mt-4">
        {confirming ? (
          <>
            <Button variant="outline" onClick={() => onClose()}>
              取消
            </Button>
            <Button onClick={() => setConfirming(false)}>
              <Play className="h-4 w-4" />
              继续（{openCount} 条）
            </Button>
          </>
        ) : (
          <>
            <Button variant="outline" onClick={copyPrompt} disabled={launching}>
              <Copy className="h-4 w-4" />
              复制提示词
            </Button>
            <Button onClick={() => void doLaunch()} disabled={!canLaunch}>
              <Play className="h-4 w-4" />
              {launching ? "启动中…" : "启动 ⌘↵"}
            </Button>
          </>
        )}
      </DialogFooter>
    </div>
  );
}
