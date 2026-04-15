import { useState } from "react";
import { Link } from "react-router";
import { Plus, Package } from "lucide-react";
import { Header } from "@/components/layout/header";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Card,
  CardContent,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { useProjects } from "@/lib/hooks/use-projects";
import { useVersions } from "@/lib/hooks/use-versions";
import { formatRelativeTime } from "@/lib/utils/format";

export default function VersionsPage() {
  const { data: projectsData, isLoading: projectsLoading } = useProjects();
  const projects = projectsData?.items ?? [];
  const [selectedProjectId, setSelectedProjectId] = useState<string>("");
  const activeProjectId = projects.some((project) => project.id === selectedProjectId)
    ? selectedProjectId
    : (projects[0]?.id ?? "");

  const { data: versionsData, isLoading: versionsLoading } = useVersions(
    activeProjectId,
  );
  const versions = versionsData?.items ?? [];

  return (
    <>
      <Header title="版本" />
      <div className="p-4 md:p-6 space-y-6">
        {/* 版本列表 */}
        <div>
          <div className="mb-4 flex items-center justify-between gap-3">
            <div className="flex items-center gap-3">
              {projectsLoading ? (
                <Skeleton className="h-10 w-64 rounded-md" />
              ) : projects.length === 0 ? (
                <p className="text-sm text-muted-foreground">暂无项目</p>
              ) : (
                <Select
                  value={activeProjectId}
                  onValueChange={(value) => setSelectedProjectId(value ?? "")}
                >
                  <SelectTrigger className="w-64">
                    <SelectValue placeholder="请选择项目">
                      {projects.find((p) => p.id === activeProjectId)?.name}
                    </SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    {projects.map((project) => (
                      <SelectItem key={project.id} value={project.id}>
                        {project.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
            </div>
            {activeProjectId && (
              <Button size="sm" render={<Link to={`/projects/${activeProjectId}/versions/new`} />}>
                <Plus className="mr-1.5 h-3.5 w-3.5" />
                创建版本
              </Button>
            )}
          </div>

          {projectsLoading ? (
            <div className="space-y-3">
              {Array.from({ length: 3 }).map((_, i) => (
                <Skeleton key={i} className="h-16 rounded-lg" />
              ))}
            </div>
          ) : projects.length === 0 ? (
            <Card>
              <CardContent className="flex flex-col items-center py-10">
                <Package className="mb-3 h-10 w-10 text-muted-foreground/50" />
                <p className="text-sm text-muted-foreground">
                  暂无项目
                </p>
              </CardContent>
            </Card>
          ) : versionsLoading ? (
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
                  to={`/projects/${activeProjectId}/versions/${version.id}`}
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
    </>
  );
}
