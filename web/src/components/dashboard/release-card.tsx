import { GitPullRequest, History, PackageOpen, Rocket } from "lucide-react";
import { Link } from "react-router";
import { Card, CardContent } from "@/components/ui/card";
import {
  SectionHeader,
  SectionUnavailable,
  formatScheduleTime,
} from "@/components/dashboard/panel-bits";
import type { components } from "@/types/api.gen";

type ReleaseBlock = components["schemas"]["DashboardRelease"];

/**
 * 发布节奏卡（INT-235 阶段三）：待发版列表、近期发版时间线与未合并 PR
 * 计数。三块统一为 AI 卡的「图标 + 粗体标签 + 值/空态」层级（视觉
 * round1 V2），纯展示，数据由页面装配。
 */
export function ReleaseCard({ release }: { release: ReleaseBlock }) {
  // 首条发版推「距上次发版 N 天」；今天发版显示「今天」。降级载荷里的列表
  // 是清零占位，不能据此推「距上次发版」，徽章随 complete 门禁（panel P4）。
  const lastRelease = release.recent_releases[0];
  const lastReleaseText =
    release.complete !== false ? daysAgoText(lastRelease?.shipped_at) : null;

  return (
    <Card className="overflow-hidden" data-testid="release-card">
      <CardContent className="p-5">
        <div className="mb-4 flex items-center gap-2">
          <Rocket className="h-4 w-4 text-muted-foreground" />
          <h3 className="text-sm font-semibold">发布节奏</h3>
          {lastReleaseText && (
            <span className="ml-auto rounded-full bg-muted px-2 py-0.5 text-[11px] font-medium text-muted-foreground">
              距上次发版 {lastReleaseText}
            </span>
          )}
        </div>

        {release.complete === false ? (
          <SectionUnavailable />
        ) : (
          <div className="divide-y">
            <section
              className="space-y-2 py-3 first:pt-0"
              data-testid="release-pending-section"
            >
              {release.pending_count === 0 ? (
                /* 0 时不再「待发版？——没有待发版」自问自答（W9）：单行标签 + 右侧 muted 暂无 */
                <SectionHeader
                  icon={PackageOpen}
                  title="待发版"
                  trailing={
                    <span className="text-xs text-muted-foreground">暂无</span>
                  }
                />
              ) : (
                <>
                  <SectionHeader
                    icon={PackageOpen}
                    title="待发版"
                    // count 徽章只在 >0 时显示（视觉 round1 V5）
                    trailing={
                      <span className="rounded-full bg-muted px-2 py-0.5 text-[11px] font-medium tabular-nums text-muted-foreground">
                        {release.pending_count}
                      </span>
                    }
                  />
                  <ul className="space-y-1 pl-6">
                    {release.pending_versions.map((version) => (
                      <li key={version.version_id}>
                        <Link
                          to={version.href}
                          className="flex items-center justify-between gap-2 rounded-md px-2 py-1.5 -mx-2 transition-colors hover:bg-muted/60"
                        >
                          <span className="truncate text-xs font-medium tabular-nums">
                            {version.version_number}
                          </span>
                          <span className="min-w-0 truncate text-[11px] text-muted-foreground">
                            {version.project_name}
                          </span>
                        </Link>
                      </li>
                    ))}
                  </ul>
                </>
              )}
            </section>

            <section
              className="space-y-2 py-3"
              data-testid="release-recent-section"
            >
              <SectionHeader icon={History} title="近期发版" />
              {release.recent_releases.length === 0 ? (
                <p className="pl-6 text-xs text-muted-foreground">
                  还没有发版记录
                </p>
              ) : (
                <ol className="space-y-1.5 pl-6">
                  {release.recent_releases.map((releaseItem) => (
                    <li
                      key={releaseItem.version_id}
                      className="flex items-center gap-2 text-xs"
                    >
                      <span
                        aria-hidden="true"
                        className="h-1.5 w-1.5 shrink-0 rounded-full bg-primary/60"
                      />
                      <Link
                        to={releaseItem.href}
                        className="min-w-0 shrink-0 truncate font-medium tabular-nums hover:underline"
                      >
                        {releaseItem.version_number}
                      </Link>
                      <span className="min-w-0 flex-1 truncate text-[11px] text-muted-foreground">
                        {releaseItem.project_name}
                      </span>
                      <span className="shrink-0 tabular-nums text-[11px] text-muted-foreground">
                        {releaseItem.shipped_at
                          ? formatScheduleTime(releaseItem.shipped_at)
                          : "时间未知"}
                      </span>
                    </li>
                  ))}
                </ol>
              )}
            </section>

            <section
              className="space-y-2 py-3"
              data-testid="release-unmerged-section"
            >
              <SectionHeader icon={GitPullRequest} title="未合并 PR" />
              <p className="pl-6 text-xs text-muted-foreground">
                {release.unmerged_pr_issue_count > 0
                  ? `${release.unmerged_pr_issue_count} 个开启问题还有未合并的 PR`
                  : "没有挂着未合并 PR 的问题"}
              </p>
            </section>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

/** 距今 N 天（按本地自然日差，PR#215 审查 Note3）；无效时间返回 null，今天发版返回「今天」。 */
function daysAgoText(iso: string | null | undefined) {
  if (!iso) return null;
  const time = new Date(iso);
  if (Number.isNaN(time.getTime())) return null;
  const today = new Date();
  const days = Math.round(
    (Date.UTC(today.getFullYear(), today.getMonth(), today.getDate()) -
      Date.UTC(time.getFullYear(), time.getMonth(), time.getDate())) /
      86_400_000,
  );
  if (days <= 0) return "今天";
  return `${days} 天`;
}
