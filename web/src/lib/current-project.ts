import { useEffect } from "react";
import { matchPath, useLocation, useParams } from "react-router";
import { useProjects } from "@/lib/hooks/use-projects";
import { useProjectPreferenceStore } from "@/lib/store/project-preference-store";

/**
 * 从路径解析项目上下文：匹配 /projects/:id/* 时返回路径里的项目 ID。
 * 全局页（含旧版单项目路由）返回 null。
 */
export function matchRouteProjectId(pathname: string): string | null {
  return matchPath("/projects/:id/*", pathname)?.params.id ?? null;
}

/**
 * 当前项目 ID 的裁决纯函数（useCurrentProjectId 的实现核心）：
 * 1. 路径里有项目 ID → 直接采用（即使不在列表里——页面要用路径 ID，不静默换项目）；
 * 2. 最近选择仍在列表 → 沿用；
 * 3. 否则取第一个未搁置项目——搁置项目不该被默认选中；
 * 4. 全部搁置时回落列表首项，保证仍有一个可用项目；
 * 5. 没有任何项目 → null。
 */
export function resolveCurrentProjectId(
  projects: Project[],
  lastSelectedProjectId: string | null,
  routeProjectId: string | null,
): string | null {
  if (routeProjectId) return routeProjectId;
  if (
    lastSelectedProjectId &&
    projects.some((project) => project.id === lastSelectedProjectId)
  ) {
    return lastSelectedProjectId;
  }
  return (projects.find((p) => !p.is_shelved) ?? projects[0])?.id ?? null;
}

/**
 * 全局当前项目 ID：路径优先，其次最近选择与兜底回落（见 resolveCurrentProjectId）。
 * 路径带项目 ID 时写入 lastSelectedProjectId，让无路径页面的兜底随之收敛；
 * 挂在旧路径上时路径分支自然为空，只剩兜底裁决（旧路由重定向依赖这一点）。
 */
export function useCurrentProjectId(projects: Project[]): string | null {
  const { pathname } = useLocation();
  const routeProjectId = matchRouteProjectId(pathname);
  const lastSelectedProjectId = useProjectPreferenceStore(
    (state) => state.lastSelectedProjectId,
  );
  const setLastSelectedProjectId = useProjectPreferenceStore(
    (state) => state.setLastSelectedProjectId,
  );

  useEffect(() => {
    if (routeProjectId && routeProjectId !== lastSelectedProjectId) {
      setLastSelectedProjectId(routeProjectId);
    }
  }, [routeProjectId, lastSelectedProjectId, setLastSelectedProjectId]);

  return resolveCurrentProjectId(
    projects,
    lastSelectedProjectId,
    routeProjectId,
  );
}

export interface RouteProject {
  /** 路径参数 :id；不在项目级路由上时为空串 */
  projectId: string;
  /** 项目列表就绪后按 projectId 查到的项目；查不到为 undefined */
  project: Project | undefined;
  /** 项目列表查询加载中 */
  isLoading: boolean;
  /** 列表已就绪且查无此项目；加载中或查询失败（无数据）时不误报 */
  notFound: boolean;
}

/**
 * 项目级路由（/projects/:id/*）的项目上下文。本任务先只消费 projectId，
 * project / notFound 供后续接管页内空态与数据来源。
 */
export function useRouteProject(): RouteProject {
  const params = useParams();
  const query = useProjects();
  const projectId = params.id ?? "";
  const projects = query.data?.items;
  const project = projects?.find((p) => p.id === projectId);
  return {
    projectId,
    project,
    isLoading: query.isLoading,
    notFound: projects !== undefined && !project,
  };
}
