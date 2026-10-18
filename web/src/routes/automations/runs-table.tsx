import { useMemo } from "react";

import { Skeleton } from "@/components/ui/skeleton";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { formatDate } from "@/lib/utils/format";
import { useAuthStore } from "@/lib/store/auth-store";
import {
  automationPostStateLabel,
  automationRunStatusLabel,
  type Automation,
  type AutomationRun,
} from "@/lib/api/automations";
import { useAutomationRuns } from "@/lib/hooks/use-automations";

// 运行记录表（INT-118 抽出共用）：列表页的运行记录弹窗与编辑页
// 「运行记录」分页渲染同一张表，按 automation id 查询。

export function AutomationRunsTable({
  automationId,
}: {
  automationId: string | null;
}) {
  const { data: runs, isLoading } = useAutomationRuns(automationId);
  const currentUser = useAuthStore((s) => s.user);
  const sortedRuns = useMemo(() => runs ?? [], [runs]);

  if (isLoading) {
    return (
      <div className="space-y-2 py-2">
        {Array.from({ length: 3 }).map((_, i) => (
          <Skeleton key={i} className="h-8 rounded" />
        ))}
      </div>
    );
  }
  if (sortedRuns.length === 0) {
    return (
      <p className="py-8 text-center text-sm text-muted-foreground">
        还没有运行记录
      </p>
    );
  }
  return (
    <div className="overflow-hidden rounded-lg border">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>触发时间</TableHead>
            <TableHead>状态</TableHead>
            <TableHead>模型 / 轮次 / SHA</TableHead>
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
              <TableCell className="whitespace-nowrap text-sm">
                {run.model_name || "—"}
                {run.round_number ? ` · R${run.round_number}` : ""}
                {run.head_sha ? ` · ${shortSHA(run.head_sha)}` : ""}
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
  );
}

// 列表页行内「运行记录」入口的弹窗包装。
export function AutomationRunsDialog({
  automation,
  onClose,
}: {
  automation: Automation | null;
  onClose: () => void;
}) {
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
        <AutomationRunsTable automationId={automation?.id ?? null} />
      </DialogContent>
    </Dialog>
  );
}

function shortSHA(sha: string): string {
  return sha.length > 7 ? sha.slice(0, 7) : sha;
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
