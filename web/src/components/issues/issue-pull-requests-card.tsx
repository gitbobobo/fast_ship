import { useState } from "react";
import { HTTPError } from "ky";
import { Loader2, RefreshCw, X } from "lucide-react";
import { toast } from "sonner";
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
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import {
  useAttachIssuePullRequest,
  useDetachIssuePullRequest,
  useSyncIssuePullRequests,
} from "@/lib/hooks/use-issues";
import { cn } from "@/lib/utils";

// open=绿 / draft=灰 / merged=紫 / closed=红，沿用 StateBadge 等徽标的配色约定
function pullRequestStateMeta(pr: IssuePullRequest) {
  if (pr.state === "merged") {
    return {
      label: "Merged",
      className:
        "border-violet-500/20 bg-violet-500/10 text-violet-600 dark:text-violet-400",
    };
  }
  if (pr.state === "closed") {
    return {
      label: "Closed",
      className:
        "border-rose-500/20 bg-rose-500/10 text-rose-600 dark:text-rose-400",
    };
  }
  if (pr.is_draft) {
    return {
      label: "Draft",
      className:
        "border-slate-500/20 bg-slate-500/10 text-slate-600 dark:text-slate-400",
    };
  }
  return {
    label: "Open",
    className:
      "border-emerald-500/20 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400",
  };
}

function PullRequestStateBadge({ pr }: { pr: IssuePullRequest }) {
  const meta = pullRequestStateMeta(pr);
  return (
    <span
      className={cn(
        "inline-flex shrink-0 items-center rounded-full border px-1.5 py-0.5 text-[10px] font-medium leading-none",
        meta.className,
      )}
    >
      {meta.label}
    </span>
  );
}

async function formatAttachError(error: unknown): Promise<string> {
  if (error instanceof HTTPError) {
    const body = await error.response
      .json<ApiResponse<unknown>>()
      .catch(() => null);
    if (body?.message) return body.message;
  }
  return "关联失败，请检查 PR 链接";
}

function DetachPullRequestButton({
  issueId,
  projectId,
  pr,
  onDetached,
  syncPending,
}: {
  issueId: string;
  projectId: string;
  pr: IssuePullRequest;
  onDetached: (id: string) => void;
  syncPending: boolean;
}) {
  const [open, setOpen] = useState(false);
  const detach = useDetachIssuePullRequest(issueId, projectId);

  const handleConfirm = async (event: React.MouseEvent) => {
    event.preventDefault();
    try {
      await detach.mutateAsync(pr.id);
      onDetached(pr.id);
      toast.success("已移除关联");
      setOpen(false);
    } catch {
      toast.error("移除失败，请稍后重试");
    }
  };

  return (
    <AlertDialog open={open} onOpenChange={setOpen}>
      <AlertDialogTrigger
        render={
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            aria-label={`移除 ${pr.repo_full_name}#${pr.number}`}
            disabled={detach.isPending || syncPending}
          />
        }
      >
        <X className="h-3.5 w-3.5" />
      </AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>移除 PR 关联？</AlertDialogTitle>
          <AlertDialogDescription>
            将解除该问题与 {pr.repo_full_name}#{pr.number} 的关联。
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={detach.isPending}>取消</AlertDialogCancel>
          <AlertDialogAction
            onClick={(event) => void handleConfirm(event)}
            disabled={detach.isPending}
            className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
          >
            {detach.isPending ? (
              <>
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                移除中…
              </>
            ) : (
              "确认移除"
            )}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

function PullRequestRow({
  issueId,
  projectId,
  pr,
  onDetached,
  syncPending,
}: {
  issueId: string;
  projectId: string;
  pr: IssuePullRequest;
  onDetached: (id: string) => void;
  syncPending: boolean;
}) {
  return (
    <div className="flex items-start gap-1.5 rounded-lg border p-2.5">
      <div className="min-w-0 flex-1 space-y-1">
        <div className="flex items-center gap-1.5">
          <PullRequestStateBadge pr={pr} />
          <a
            href={pr.html_url}
            target="_blank"
            rel="noopener noreferrer"
            className="min-w-0 truncate text-sm hover:underline"
            title={pr.title}
          >
            <span className="font-mono text-xs text-muted-foreground">
              {pr.repo_full_name}#{pr.number}
            </span>{" "}
            {pr.title}
          </a>
        </div>
        <p className="truncate text-xs text-muted-foreground">
          @{pr.author_login} · {pr.head_ref} → {pr.base_ref}
        </p>
      </div>
      <DetachPullRequestButton
        issueId={issueId}
        projectId={projectId}
        pr={pr}
        onDetached={onDetached}
        syncPending={syncPending}
      />
    </div>
  );
}

export function IssuePullRequestsCard({
  issueId,
  projectId,
  pullRequests,
}: {
  issueId: string;
  projectId: string;
  pullRequests?: IssuePullRequest[];
}) {
  const [url, setUrl] = useState("");
  const [syncFailures, setSyncFailures] = useState<
    Array<{ id: string; error: string }>
  >([]);
  const attach = useAttachIssuePullRequest(issueId, projectId);
  const sync = useSyncIssuePullRequests(issueId, projectId);
  const items = pullRequests ?? [];

  // 移除成功后同步清掉该行残留的刷新失败提示
  const handleDetached = (id: string) => {
    setSyncFailures((prev) => prev.filter((failure) => failure.id !== id));
  };

  // sync 期间并发 detach（本页或其他客户端）会让失败列表残留已解除的行；
  // 渲染时只展示仍存在于当前关联列表中的失败项
  const visibleFailures = syncFailures.filter((failure) =>
    items.some((pr) => pr.id === failure.id),
  );

  const handleAttach = async (event: React.FormEvent) => {
    event.preventDefault();
    const value = url.trim();
    if (!value) {
      return;
    }
    try {
      await attach.mutateAsync(value);
      setUrl("");
      toast.success("已关联 PR");
    } catch (error) {
      toast.error(await formatAttachError(error));
    }
  };

  const handleSync = async () => {
    try {
      const res = await sync.mutateAsync();
      const failures = res.data.failures ?? [];
      setSyncFailures(failures);
      if (failures.length === 0) {
        toast.success("已刷新");
      } else {
        toast.error(`${failures.length} 个 PR 刷新失败`);
      }
    } catch {
      toast.error("刷新失败，请稍后重试");
    }
  };

  return (
    <Card>
      <CardHeader className="pb-0 pt-4">
        <div className="flex items-start justify-between gap-2">
          <CardTitle className="text-sm">实现 PR</CardTitle>
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label="刷新 PR 状态"
            title="从 GitHub 刷新状态"
            disabled={sync.isPending || items.length === 0}
            onClick={() => void handleSync()}
          >
            <RefreshCw
              className={cn("h-3.5 w-3.5", sync.isPending && "animate-spin")}
            />
          </Button>
        </div>
      </CardHeader>
      <CardContent className="space-y-3 p-4 pt-3">
        <form className="flex items-center gap-1.5" onSubmit={(event) => void handleAttach(event)}>
          <Input
            value={url}
            onChange={(event) => setUrl(event.target.value)}
            placeholder="粘贴 PR 链接"
            aria-label="PR 链接"
            className="h-8 min-w-0 flex-1 text-xs"
            disabled={attach.isPending}
          />
          <Button
            type="submit"
            size="sm"
            className="h-8 shrink-0"
            aria-label="添加 PR"
            disabled={!url.trim() || attach.isPending}
          >
            {attach.isPending ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
            ) : (
              "添加"
            )}
          </Button>
        </form>

        {items.length === 0 ? (
          <p className="text-sm text-muted-foreground">暂无关联的 PR</p>
        ) : (
          <div className="space-y-2">
            {items.map((pr) => (
              <PullRequestRow
                key={pr.id}
                issueId={issueId}
                projectId={projectId}
                pr={pr}
                onDetached={handleDetached}
                syncPending={sync.isPending}
              />
            ))}
          </div>
        )}

        {visibleFailures.length > 0 && (
          <div className="space-y-1 rounded-md border border-destructive/30 bg-destructive/5 px-2.5 py-2">
            {visibleFailures.map((failure) => (
              <p key={failure.id} className="text-xs text-destructive">
                {failure.error}
              </p>
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  );
}
