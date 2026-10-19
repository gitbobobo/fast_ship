import type { ReactNode } from "react";
import { Link } from "react-router";
import {
  ArrowLeft,
  Clock,
  GitBranch,
  GitPullRequest,
  Laptop,
} from "lucide-react";
import type { ThreadSummary } from "@/lib/api/threads";
import { formatRelativeTime } from "@/lib/utils/format";
import { ProviderIcon } from "./provider-icon";
import { ProjectBadge } from "./project-badge";

function Row({ icon, children }: { icon: ReactNode; children: ReactNode }) {
  return (
    <div className="flex min-w-0 items-center gap-2.5 px-3 py-1.5">
      <span className="flex size-4 shrink-0 items-center justify-center text-muted-foreground">
        {icon}
      </span>
      <span className="min-w-0 flex-1 truncate">{children}</span>
    </div>
  );
}

/**
 * 详情页右侧信息卡（对齐 T3 右侧栏）：线程的环境、项目、分支、模型等元数据。
 * 显示与否跟随右栏容器宽度（@container，见 threads/layout.tsx）而非视口，
 * 避免中等视口下把聊天区挤窄。
 */
export function ThreadInfoCard({
  thread,
  environmentId,
}: {
  thread: ThreadSummary;
  environmentId: string;
}) {
  const model = [thread.provider, thread.model].filter(Boolean).join(" / ");

  return (
    <aside
      data-testid="thread-info-card"
      aria-label="线程信息"
      className="sticky top-20 mt-4 hidden w-64 shrink-0 self-start md:mt-6 @4xl:block @5xl:w-72"
    >
      <div className="divide-y rounded-2xl border bg-card py-1 text-sm shadow-xs">
        <div className="py-1">
          <Row icon={<Laptop className="size-4" />}>{thread.environment_name}</Row>
          <Row icon={<ProjectBadge name={thread.project_name} className="size-4 text-[9px]" />}>
            {thread.project_name}
          </Row>
          {thread.branch && (
            <Row icon={<GitBranch className="size-4" />}>{thread.branch}</Row>
          )}
          {model && (
            <Row icon={<ProviderIcon providerInstanceId={thread.provider} className="size-4" />}>
              {model}
            </Row>
          )}
        </div>
        <div className="py-1">
          {thread.pull_request_count > 0 && (
            <Row icon={<GitPullRequest className="size-4" />}>
              {thread.pull_request_count} 个 PR
            </Row>
          )}
          <Row icon={<Clock className="size-4" />}>
            {formatRelativeTime(thread.updated_at)}更新
          </Row>
        </div>
        {thread.parent_thread_id && (
          <div className="py-1">
            <Link
              to={`/threads/${encodeURIComponent(environmentId)}/${encodeURIComponent(thread.parent_thread_id)}`}
              className="flex items-center gap-2.5 px-3 py-1.5 text-muted-foreground hover:text-foreground"
            >
              <ArrowLeft className="size-4 shrink-0" />
              返回父线程
            </Link>
          </div>
        )}
      </div>
    </aside>
  );
}
