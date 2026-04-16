import { useState } from "react";
import { Link, useParams, useNavigate } from "react-router";
import { Plus, Pencil, Trash2, GitFork, Package, Bug, RefreshCw } from "lucide-react";
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
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { useProject, useDeleteProject } from "@/lib/hooks/use-projects";
import { useVersions } from "@/lib/hooks/use-versions";
import { useSyncProjectIssues } from "@/lib/hooks/use-issues";
import { formatRelativeTime, formatDate } from "@/lib/utils/format";
import { toast } from "sonner";

export default function ProjectDetailPage() {
  const { id } = useParams();
  const navigate = useNavigate();
  const [statusFilter, setStatusFilter] = useState("all");
  const { data: project, isLoading: projectLoading } = useProject(id!);
  const { data: versionsData, isLoading: versionsLoading } = useVersions(
    id!,
    statusFilter === "all" ? undefined : statusFilter,
  );
  const deleteProject = useDeleteProject();
  const syncIssues = useSyncProjectIssues(id!);

  const versions = versionsData?.items ?? [];
  const issueSync = project?.issue_sync;

  const handleDelete = async () => {
    try {
      await deleteProject.mutateAsync(id!);
      toast.success("项目已删除");
      navigate("/projects", { replace: true });
    } catch {
      toast.error("删除失败");
    }
  };

  const handleSyncIssues = async () => {
    try {
      const res = await syncIssues.mutateAsync();
      toast.success(
        `同步完成：${res.data.synced_issue_count} 个问题，${res.data.synced_comment_count} 条评论`,
      );
    } catch {
      toast.error("同步失败，请检查 GitHub 仓库配置和 Token");
    }
  };

  if (projectLoading) {
    return (
      <>
        <Header title="项目详情" />
        <div className="p-4 md:p-6 space-y-4">
          <Skeleton className="h-24 rounded-lg" />
          <Skeleton className="h-48 rounded-lg" />
        </div>
      </>
    );
  }

  if (!project) {
    return (
      <>
        <Header title="项目详情" />
        <div className="p-4 md:p-6">
          <p className="text-sm text-muted-foreground">项目不存在</p>
        </div>
      </>
    );
  }

  return (
    <>
      <Header title={project.name} />
      <div className="p-4 md:p-6 space-y-6">
        {/* 项目信息卡片 */}
        <Card>
          <CardHeader className="flex-row items-start justify-between space-y-0">
            <div className="space-y-1">
              <CardTitle>{project.name}</CardTitle>
              {project.description && (
                <CardDescription>{project.description}</CardDescription>
              )}
            </div>
            <div className="flex gap-2">
              <Button variant="outline" size="sm" render={<Link to={`/projects/${id}/edit`} />}>
                  <Pencil className="mr-1.5 h-3.5 w-3.5" />
                  编辑
              </Button>
              <AlertDialog>
                <AlertDialogTrigger render={<Button variant="outline" size="sm" />}>
                    <Trash2 className="mr-1.5 h-3.5 w-3.5" />
                    删除
                </AlertDialogTrigger>
                <AlertDialogContent>
                  <AlertDialogHeader>
                    <AlertDialogTitle>确认删除项目?</AlertDialogTitle>
                    <AlertDialogDescription>
                      删除后，项目下所有版本和安装包数据将一并删除，且不可恢复。
                    </AlertDialogDescription>
                  </AlertDialogHeader>
                  <AlertDialogFooter>
                    <AlertDialogCancel>取消</AlertDialogCancel>
                    <AlertDialogAction onClick={handleDelete}>
                      确认删除
                    </AlertDialogAction>
                  </AlertDialogFooter>
                </AlertDialogContent>
              </AlertDialog>
            </div>
          </CardHeader>
          <CardContent>
            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              <GitFork className="h-4 w-4" />
              <a
                href={`https://github.com/${project.github_owner}/${project.github_repo}`}
                target="_blank"
                rel="noopener noreferrer"
                className="hover:underline"
              >
                {project.github_owner}/{project.github_repo}
              </a>
            </div>
            <p className="mt-2 text-xs text-muted-foreground">
              创建于 {formatDate(project.created_at)}
            </p>
            {issueSync && (
              <div className="mt-3 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                <span>Issues 同步状态</span>
                <Badge
                  variant={
                    issueSync.status === "failed"
                      ? "destructive"
                      : issueSync.status === "completed"
                        ? "default"
                        : "secondary"
                  }
                >
                  {issueSync.status === "running"
                    ? "同步中"
                    : issueSync.status === "failed"
                      ? "同步失败"
                      : issueSync.status === "completed"
                        ? "同步完成"
                        : "未同步"}
                </Badge>
                {issueSync.last_synced_at && (
                  <span>最近同步 {formatRelativeTime(issueSync.last_synced_at)}</span>
                )}
                {issueSync.last_error && (
                  <span className="text-destructive">
                    最近错误：{issueSync.last_error}
                  </span>
                )}
              </div>
            )}
          </CardContent>
        </Card>

        {/* 版本列表 */}
        <div>
          <div className="mb-4 flex items-center justify-between">
            <div className="flex items-center gap-3">
              <h2 className="text-lg font-semibold">版本</h2>
              <Select
                value={statusFilter}
                onValueChange={(value) => setStatusFilter(value ?? "all")}
              >
                <SelectTrigger className="w-36">
                  <SelectValue placeholder="全部状态" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">全部状态</SelectItem>
                  <SelectItem value="pending">待发货</SelectItem>
                  <SelectItem value="shipped">已发货</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <Button size="sm" render={<Link to={`/projects/${id}/versions/new`} />}>
              <Plus className="mr-1.5 h-3.5 w-3.5" />
              创建版本
            </Button>
          </div>

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
                  {statusFilter === "all"
                    ? "暂无版本，创建第一个版本开始发布"
                    : "当前筛选条件下暂无版本"}
                </p>
              </CardContent>
            </Card>
          ) : (
            <div className="space-y-3">
              {versions.map((version) => (
                <Link
                  key={version.id}
                  to={`/projects/${id}/versions/${version.id}`}
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

        {/* 问题入口 */}
        <div>
          <div className="mb-4 flex items-center justify-between">
            <h2 className="text-lg font-semibold">问题</h2>
            <div className="flex gap-2">
              <Button variant="outline" size="sm" render={<Link to={`/issues?project=${id}`} />}>
                查看全部问题
              </Button>
              <Button size="sm" onClick={handleSyncIssues} disabled={syncIssues.isPending}>
                <RefreshCw className={`mr-1.5 h-3.5 w-3.5 ${syncIssues.isPending ? "animate-spin" : ""}`} />
                {syncIssues.isPending ? "同步中..." : "同步 GitHub Issues"}
              </Button>
            </div>
          </div>
          <Card>
            <CardContent className="flex flex-col items-center py-10">
              <Bug className="mb-3 h-10 w-10 text-muted-foreground/50" />
              <p className="text-sm text-muted-foreground">
                在问题列表页查看和管理所有 GitHub Issues
              </p>
            </CardContent>
          </Card>
        </div>
      </div>
    </>
  );
}
