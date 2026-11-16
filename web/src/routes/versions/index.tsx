import { useState, useEffect } from "react";
import { Link, useSearchParams } from "react-router";
import { Plus, Package } from "lucide-react";
import { VersionFormDialog } from "@/components/versions/version-form-dialog";
import { useRouteProject } from "@/lib/current-project";
import {
  ProjectBreadcrumb,
  ProjectNotFoundCard,
} from "@/components/projects/project-page-chrome";
import { Header } from "@/components/layout/header";
import { HeaderActions } from "@/components/layout/header-actions";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Card,
  CardContent,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { useVersions } from "@/lib/hooks/use-versions";
import { formatRelativeTime } from "@/lib/utils/format";

export default function VersionsPage() {
  // 项目上下文只认路径：无效 ID 整页空态，不再回退到偏好或静默换项目
  const { projectId, project, notFound } = useRouteProject();
  if (notFound) {
    return <ProjectNotFoundCard />;
  }
  return <VersionsPageContent projectId={projectId} project={project} />;
}

function VersionsPageContent({
  projectId,
  project,
}: {
  projectId: string;
  project: Project | undefined;
}) {
  const [searchParams, setSearchParams] = useSearchParams();
  const [createDialogOpen, setCreateDialogOpen] = useState(false);

  useEffect(() => {
    if (searchParams.get("create") === "1") {
      setCreateDialogOpen(true);
      const next = new URLSearchParams(searchParams);
      next.delete("create");
      setSearchParams(next, { replace: true });
    }
  }, [searchParams, setSearchParams]);

  const { data: versionsData, isLoading: versionsLoading } = useVersions(
    projectId,
  );
  const versions = versionsData?.items ?? [];

  return (
    <>
      <Header
        title={<ProjectBreadcrumb section="版本" project={project} />}
        actions={
          <HeaderActions
            primary={
              <Button size="sm" onClick={() => setCreateDialogOpen(true)}>
                <Plus className="mr-1.5 h-3.5 w-3.5" />
                创建版本
              </Button>
            }
          />
        }
      />
      <div className="p-4 md:p-6 space-y-6">
        {/* 版本列表 */}
        <div>
          {versionsLoading ? (
            <div className="space-y-3">
              {Array.from({ length: 3 }).map((_, i) => (
                <Skeleton key={i} className="h-16 rounded-lg" />
              ))}
            </div>
          ) : versions.length === 0 ? (
            <Card>
              <CardContent className="flex flex-col items-center py-10">
                <Package className="mb-3 h-10 w-10 text-muted-foreground/50" />
                <p className="text-sm text-muted-foreground">
                  该项目暂无版本
                </p>
              </CardContent>
            </Card>
          ) : (
            <div className="space-y-3">
              {versions.map((version) => (
                <Link
                  key={version.id}
                  to={`/projects/${projectId}/versions/${version.id}`}
                >
                  <Card className="transition-colors hover:border-primary/50">
                    <CardContent className="flex items-center justify-between py-4">
                      <div className="flex items-center gap-3">
                        <span className="font-mono font-medium">
                          {version.version_number}
                        </span>
                        <Badge
                          variant={
                            version.status === "shipped"
                              ? "default"
                              : "secondary"
                          }
                        >
                          {version.status === "shipped" ? "已发货" : "待发货"}
                        </Badge>
                        {version.artifacts && version.artifacts.length > 0 && (
                          <span className="text-xs text-muted-foreground">
                            {version.artifacts.length} 个安装包
                          </span>
                        )}
                      </div>
                      <span className="text-xs text-muted-foreground">
                        {formatRelativeTime(version.created_at)}
                      </span>
                    </CardContent>
                  </Card>
                </Link>
              ))}
            </div>
          )}
        </div>
      </div>

      <VersionFormDialog
        open={createDialogOpen}
        onOpenChange={setCreateDialogOpen}
        projectId={projectId}
      />
    </>
  );
}
