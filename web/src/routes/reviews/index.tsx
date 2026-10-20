import { Link } from "react-router";
import { GitPullRequest } from "lucide-react";

import { Badge } from "@/components/ui/badge";
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

// 审查总览（INT-135）：当前用户所有已关联 GitHub 仓库的项目列表。
// 开关、组合数、最近一次审查结论；同仓库已被其他项目开启的行只读。

export default function ReviewsPage() {
  const { data: projects, isLoading } = useReviewProjects();

  return (
    <div className="flex flex-col">
      <Header title="审查" />
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
          <div className="overflow-hidden rounded-lg border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>项目</TableHead>
                  <TableHead>仓库</TableHead>
                  <TableHead>开关</TableHead>
                  <TableHead>代码组合</TableHead>
                  <TableHead>视觉组合</TableHead>
                  <TableHead>最近状态</TableHead>
                  <TableHead>最近审查</TableHead>
                  <TableHead className="text-right">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {projects.map((project) => (
                  <ReviewProjectRow key={project.project_id} project={project} />
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </div>
    </div>
  );
}

function ReviewProjectRow({ project }: { project: ReviewProjectSummary }) {
  return (
    <TableRow>
      <TableCell className="font-medium">{project.project_name}</TableCell>
      <TableCell className="text-sm">
        {project.github_owner}/{project.github_repo}
      </TableCell>
      <TableCell>
        <div className="flex flex-col items-start gap-1">
          <Badge variant={project.enabled ? "default" : "secondary"}>
            {project.enabled ? "已开启" : "未开启"}
          </Badge>
          {project.taken_by && (
            <span className="text-xs text-muted-foreground">
              由 {project.taken_by} 开启
            </span>
          )}
        </div>
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
          <ConclusionBadge label="视觉" status={project.visual_status} />
        </div>
      </TableCell>
      <TableCell className="whitespace-nowrap text-sm">
        {project.last_review_at ? formatDate(project.last_review_at) : "—"}
      </TableCell>
      <TableCell className="text-right">
        <div className="flex justify-end gap-1">
          <Button
            variant="ghost"
            size="sm"
            render={<Link to={`/reviews/${project.project_id}`} />}
          >
            设置
          </Button>
          <Button
            variant="ghost"
            size="sm"
            render={
              <Link to={`/reviews/${project.project_id}?tab=records`} />
            }
          >
            审查记录
          </Button>
        </div>
      </TableCell>
    </TableRow>
  );
}


