import { useMemo } from "react";
import { Navigate, useParams, useSearchParams } from "react-router";
import { useCurrentProjectId } from "@/lib/current-project";
import { useProjects } from "@/lib/hooks/use-projects";
import { Button } from "@/components/ui/button";
import type { ProjectSection } from "@/lib/project-nav";

/**
 * 旧版单项目路由（/issues、/board 等）的重定向：目标项目由 query 里合法的
 * project 决定；缺失或非法时用 useCurrentProjectId 的兜底裁决取默认项目；
 * 没有任何项目时回落 /projects。列表未就绪前渲染空占位，避免弹到错误项目；
 * 列表查询失败时停留原地渲染可重试占位，不能把深链改写丢掉。
 */
function useLegacyTarget(searchParams: URLSearchParams): {
  ready: boolean;
  failed: boolean;
  targetId: string | null;
  refetch: () => void;
} {
  const { data, isLoading, isError, refetch } = useProjects();
  const projects = useMemo(() => data?.items ?? [], [data]);
  const requestedId = searchParams.get("project");
  // 重定向组件挂在旧路径上，useCurrentProjectId 的路径分支自然为空，只剩兜底裁决
  const fallbackId = useCurrentProjectId(projects);
  const targetId =
    requestedId && projects.some((p) => p.id === requestedId)
      ? requestedId
      : fallbackId;
  return {
    ready: !isLoading,
    failed: isError,
    targetId,
    refetch: () => void refetch(),
  };
}

/** 列表查询失败时的占位：留在原地址，用户可重试或修正链接。 */
function LegacyRedirectError({ onRetry }: { onRetry: () => void }) {
  return (
    <div
      data-testid="legacy-redirect-error"
      className="flex min-h-[40vh] flex-col items-center justify-center gap-3 text-sm text-muted-foreground"
    >
      <p>项目列表加载失败，暂时无法跳转旧链接。</p>
      <Button variant="outline" size="sm" onClick={onRetry}>
        重试
      </Button>
    </div>
  );
}

/** 构造新路径：project 参数由路径承担并从 query 移除，其余 query 原样保留。 */
function buildTargetPath(
  targetId: string,
  suffixPath: string,
  searchParams: URLSearchParams,
): string {
  const rest = new URLSearchParams(searchParams);
  rest.delete("project");
  const query = rest.toString();
  return `/projects/${encodeURIComponent(targetId)}${suffixPath}${query ? `?${query}` : ""}`;
}

export function LegacySectionRedirect({ section }: { section: ProjectSection }) {
  const [searchParams] = useSearchParams();
  const { ready, failed, targetId, refetch } = useLegacyTarget(searchParams);

  if (failed) return <LegacyRedirectError onRetry={refetch} />;
  if (!ready) return null;
  if (!targetId) return <Navigate to="/projects" replace />;
  return <Navigate to={buildTargetPath(targetId, `/${section}`, searchParams)} replace />;
}

export function LegacyLogRunRedirect() {
  const { runId = "" } = useParams();
  const [searchParams] = useSearchParams();
  const { ready, failed, targetId, refetch } = useLegacyTarget(searchParams);

  if (failed) return <LegacyRedirectError onRetry={refetch} />;
  if (!ready) return null;
  if (!targetId || !runId) return <Navigate to="/projects" replace />;
  return (
    <Navigate
      to={buildTargetPath(
        targetId,
        `/logs/${encodeURIComponent(runId)}`,
        searchParams,
      )}
      replace
    />
  );
}

/**
 * /projects/:id/versions/new → 新版列表页带 create=1：直接落在最终路径，
 * 不再绕道旧 /versions 造成二次跳转。
 */
export function LegacyNewVersionRedirect() {
  const { id } = useParams();
  const to = id
    ? `/projects/${encodeURIComponent(id)}/versions?create=1`
    : "/projects";
  return <Navigate to={to} replace />;
}
