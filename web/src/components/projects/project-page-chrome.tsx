import type { ReactNode } from "react";
import { Link } from "react-router";
import { FolderX } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useRouteProject } from "@/lib/current-project";

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
      <span className="shrink-0 text-muted-foreground">Fast Ship</span>
      <span className="shrink-0 text-muted-foreground/50">/</span>
      {project && (
        <>
          <span className="max-w-40 truncate text-muted-foreground" title={project.name}>
            {project.name}
          </span>
          <span className="shrink-0 text-muted-foreground/50">/</span>
        </>
      )}
      <span className="truncate font-semibold text-foreground">{section}</span>
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

/** 项目列表首次加载中的整页占位：项目有效性未知，页面数据 hooks 不应挂载。 */
function ProjectPageSkeleton(): ReactNode {
  return (
    <div
      data-testid="project-page-loading"
      className="flex min-h-[40vh] items-center justify-center text-sm text-muted-foreground"
    >
      加载中...
    </div>
  );
}

/**
 * 项目页三态门：项目列表首次加载中先渲染占位（冷启动深链无效 ID 时不给
 * 数据请求窗口）；确认无效渲染空态；其余放行——包括列表查询失败（无法
 * 判定有效性时交给页面自身与后端兑底），避免列表故障拖垮全部项目页。
 */
export function ProjectPageGate({
  children,
}: {
  children: (ctx: {
    projectId: string;
    project: Project | undefined;
  }) => ReactNode;
}): ReactNode {
  const { projectId, project, isLoading, notFound } = useRouteProject();
  if (isLoading) return <ProjectPageSkeleton />;
  if (notFound) return <ProjectNotFoundCard />;
  return <>{children({ projectId, project })}</>;
}
