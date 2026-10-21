import { useState } from "react";
import { Link } from "react-router";
import { ChevronRight, GitPullRequest } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Header } from "@/components/layout/header";
import { ConclusionBadge } from "@/components/reviews/conclusion-badge";
import { formatDate } from "@/lib/utils/format";
import { type ReviewProjectSummary } from "@/lib/api/reviews";
import { useReviewProjects } from "@/lib/hooks/use-reviews";
import { cn } from "@/lib/utils";

// 审查配置页（INT-141）：承接旧 /reviews 项目表的瘦身版。已开启项目在上
// （每行写审查 owner/repo），未开启折叠为「其余 N 个项目未开启 · 展开」。

export default function ReviewConfigPage() {
  const { data: projects, isLoading, isError, refetch } = useReviewProjects();
  const [showDisabled, setShowDisabled] = useState(false);

  const enabled = (projects ?? []).filter((p) => p.enabled);
  const disabled = (projects ?? []).filter((p) => !p.enabled);

  const title = (
    <span className="flex min-w-0 items-center gap-1.5">
      <Link
        to="/reviews"
        className="shrink-0 font-normal text-muted-foreground transition-colors hover:text-foreground"
      >
        审查
      </Link>
      <span className="shrink-0 text-muted-foreground">/</span>
      <span className="truncate">配置</span>
    </span>
  );

  return (
    <div className="flex flex-col">
      <Header title={title} backTo="/reviews" />
      <div className="mx-auto w-full max-w-5xl space-y-6 p-4 md:p-6">
        <p className="text-sm text-muted-foreground">
          对 GitHub PR 自动执行代码审查与视觉审查。审查按项目开启，
          同一仓库在整个部署内只允许一个项目开启。
        </p>

        {isLoading ? (
          <div className="space-y-3">
            {Array.from({ length: 3 }).map((_, i) => (
              <Skeleton key={i} className="h-10 rounded" />
            ))}
          </div>
        ) : isError ? (
          <div className="rounded-lg border bg-muted/40 py-12 text-center">
            <p className="text-sm text-muted-foreground">项目列表加载失败</p>
            <div className="mt-4">
              <Button variant="outline" size="sm" onClick={() => void refetch()}>
                重试
              </Button>
            </div>
          </div>
        ) : !projects?.length ? (
          <div className="rounded-lg border bg-muted/40 py-12 text-center">
            <GitPullRequest className="mx-auto mb-2 h-6 w-6 text-muted-foreground" />
            <p className="text-sm text-muted-foreground">
              先给项目关联 GitHub 仓库，再开启审查
            </p>
            <div className="mt-4">
              <Button size="sm" render={<Link to="/projects" />}>
                前往项目
              </Button>
            </div>
          </div>
        ) : (
          <>
            {enabled.length > 0 && (
              <ProjectTable projects={enabled} showStatus={false} />
            )}

            {disabled.length > 0 && (
              <section className="space-y-2">
                <button
                  type="button"
                  aria-expanded={showDisabled}
                  onClick={() => setShowDisabled((open) => !open)}
                  className="flex items-center gap-1 text-sm font-medium text-muted-foreground hover:text-foreground"
                >
                  <ChevronRight
                    className={cn(
                      "h-4 w-4 transition-transform",
                      showDisabled && "rotate-90",
                    )}
                  />
                  其余 {disabled.length} 个项目未开启 ·{" "}
                  {showDisabled ? "收起" : "展开"}
                </button>
                {showDisabled && (
                  <ProjectTable projects={disabled} showStatus />
                )}
              </section>
            )}
          </>
        )}
      </div>
    </div>
  );
}

function ProjectTable({
  projects,
  showStatus,
}: {
  projects: ReviewProjectSummary[];
  /** 未开启折叠区显示「由 X 开启」占用提示。 */
  showStatus: boolean;
}) {
  return (
    <div className="overflow-hidden rounded-lg border">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>项目</TableHead>
            <TableHead>审查仓库</TableHead>
            <TableHead>代码组合</TableHead>
            <TableHead>视觉组合</TableHead>
            <TableHead>最近结论</TableHead>
            <TableHead>最近审查</TableHead>
            <TableHead className="text-right">操作</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {projects.map((project) => (
            <TableRow key={project.project_id}>
              <TableCell className="font-medium">
                <div className="flex flex-col items-start gap-1">
                  {project.project_name}
                  {showStatus && project.taken_by && (
                    <span className="text-xs font-normal text-muted-foreground">
                      由 {project.taken_by} 开启
                    </span>
                  )}
                </div>
              </TableCell>
              <TableCell className="text-sm">
                {project.github_owner}/{project.github_repo}
              </TableCell>
              <TableCell className="text-sm">
                {project.code_combination_count}
              </TableCell>
              <TableCell className="text-sm">
                {project.visual_combination_count}
              </TableCell>
              <TableCell>
                <div className="flex flex-col items-start gap-1">
                  <ConclusionBadge label="代码" status={project.code_status} />
                  <ConclusionBadge
                    label="视觉"
                    status={project.visual_status}
                  />
                </div>
              </TableCell>
              <TableCell className="whitespace-nowrap text-sm">
                {project.last_review_at
                  ? formatDate(project.last_review_at)
                  : "—"}
              </TableCell>
              <TableCell className="text-right">
                <Button
                  variant="ghost"
                  size="sm"
                  render={
                    <Link to={`/reviews/${project.project_id}`} />
                  }
                >
                  设置
                </Button>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}
