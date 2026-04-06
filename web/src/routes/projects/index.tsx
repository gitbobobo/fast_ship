import { Link } from "react-router";
import { Plus, GitFork, FolderOpen } from "lucide-react";
import { Header } from "@/components/layout/header";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { useProjects } from "@/lib/hooks/use-projects";
import { formatRelativeTime } from "@/lib/utils/format";

export default function ProjectsPage() {
  const { data, isLoading } = useProjects();
  const projects = data?.items ?? [];

  return (
    <>
      <Header title="项目" />
      <div className="p-4 md:p-6">
        <div className="mb-6 flex items-center justify-between">
          <p className="text-sm text-muted-foreground">
            管理你的项目和版本发布
          </p>
          <Button render={<Link to="/projects/new" />}>
            <Plus className="mr-2 h-4 w-4" />
            创建项目
          </Button>
        </div>

        {isLoading ? (
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {Array.from({ length: 3 }).map((_, i) => (
              <Skeleton key={i} className="h-36 rounded-lg" />
            ))}
          </div>
        ) : projects.length === 0 ? (
          <div className="flex flex-col items-center justify-center py-20 text-center">
            <FolderOpen className="mb-4 h-12 w-12 text-muted-foreground/50" />
            <h3 className="text-lg font-medium">暂无项目</h3>
            <p className="mt-1 text-sm text-muted-foreground">
              创建你的第一个项目来开始管理版本发布
            </p>
            <Button className="mt-4" render={<Link to="/projects/new" />}>
              <Plus className="mr-2 h-4 w-4" />
              创建项目
            </Button>
          </div>
        ) : (
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {projects.map((project) => (
              <Link key={project.id} to={`/projects/${project.id}`}>
                <Card className="transition-colors hover:border-primary/50">
                  <CardHeader className="pb-2">
                    <CardTitle className="text-base truncate">
                      {project.name}
                    </CardTitle>
                    {project.description && (
                      <CardDescription className="line-clamp-2">
                        {project.description}
                      </CardDescription>
                    )}
                  </CardHeader>
                  <CardContent>
                    <div className="flex items-center gap-2 text-xs text-muted-foreground">
                      <GitFork className="h-3.5 w-3.5" />
                      <span className="truncate">
                        {project.github_owner}/{project.github_repo}
                      </span>
                    </div>
                    <p className="mt-2 text-xs text-muted-foreground">
                      {formatRelativeTime(project.updated_at)}
                    </p>
                  </CardContent>
                </Card>
              </Link>
            ))}
          </div>
        )}
      </div>
    </>
  );
}
