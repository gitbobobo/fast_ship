import type { ReactNode } from "react";
import { Link } from "react-router";
import { FolderX } from "lucide-react";
import { Button } from "@/components/ui/button";

/**
 * 项目级页面的面包屑标题：`fast_ship / <项目名> / <section>`。
 * 项目列表未就绪时先只显示 fast_ship 与 section 名，就绪后补上项目名；
 * 项目名超长时截断，全名放 title 属性。
 */
export function ProjectBreadcrumb({
  section,
  project,
}: {
  section: string;
  project: Project | undefined;
}) {
  return (
    <span
      className="flex min-w-0 items-center gap-1.5 text-sm"
      data-testid="project-breadcrumb"
    >
      <span className="shrink-0 text-muted-foreground">fast_ship</span>
      <span className="shrink-0 text-muted-foreground/50">/</span>
      {project && (
        <>
          <span className="max-w-40 truncate" title={project.name}>
            {project.name}
          </span>
          <span className="shrink-0 text-muted-foreground/50">/</span>
        </>
      )}
      <span className="truncate font-medium">{section}</span>
    </span>
  );
}

/**
 * 路径项目 ID 无效（列表就绪但查无此项目）时的整页空态。
 * 页面只认路径：此时不请求任何项目数据，也不静默换项目，给出明确的返回入口。
 */
export function ProjectNotFoundCard(): ReactNode {
  return (
    <div
      data-testid="project-not-found"
      className="flex min-h-[60vh] flex-col items-center justify-center gap-3 p-6 text-center"
    >
      <FolderX className="h-12 w-12 text-muted-foreground/50" />
      <p className="text-sm text-muted-foreground">项目不存在或无权访问</p>
      <Button variant="outline" size="sm" render={<Link to="/projects" />}>
        返回项目列表
      </Button>
    </div>
  );
}
