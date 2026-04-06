import { useDeferredValue, useState } from "react";
import { Link } from "react-router";
import { Plus, GitFork, FolderOpen, Search } from "lucide-react";
import { Header } from "@/components/layout/header";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Badge } from "@/components/ui/badge";
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
  const [search, setSearch] = useState("");
  const [statusFilter, setStatusFilter] = useState("all");
  const deferredSearch = useDeferredValue(search.trim().toLowerCase());

  const filteredProjects = projects.filter((project) => {
    const matchesSearch =
      deferredSearch.length === 0 ||
      project.name.toLowerCase().includes(deferredSearch) ||
      project.description?.toLowerCase().includes(deferredSearch) ||
      `${project.github_owner}/${project.github_repo}`
        .toLowerCase()
        .includes(deferredSearch);

    const latestStatus = project.latest_version?.status ?? "none";
    const matchesStatus =
      statusFilter === "all" ||
      (statusFilter === "none" && latestStatus === "none") ||
      latestStatus === statusFilter;

    return matchesSearch && matchesStatus;
  });

  return (
    <>
      <Header title="项目" />
      <div className="p-4 md:p-6">
        <div className="mb-6 flex flex-col gap-4 lg:flex-row lg:items-end lg:justify-between">
          <div className="space-y-3">
            <p className="text-sm text-muted-foreground">
              管理你的项目和版本发布
            </p>
            <div className="flex flex-col gap-3 sm:flex-row">
              <div className="relative w-full sm:w-72">
                <Search className="pointer-events-none absolute top-1/2 left-2.5 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
                <Input
                  value={search}
                  onChange={(e) => setSearch(e.target.value)}
                  className="pl-8"
                  placeholder="搜索项目名、描述或仓库"
                />
              </div>
              <Select
                value={statusFilter}
                onValueChange={(value) => setStatusFilter(value ?? "all")}
              >
                <SelectTrigger className="w-full sm:w-44">
                  <SelectValue placeholder="全部状态" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">全部状态</SelectItem>
                  <SelectItem value="pending">最新版本待发货</SelectItem>
                  <SelectItem value="shipped">最新版本已发货</SelectItem>
                  <SelectItem value="none">未创建版本</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
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
        ) : filteredProjects.length === 0 ? (
          <div className="flex flex-col items-center justify-center rounded-lg border border-dashed py-16 text-center">
            <FolderOpen className="mb-4 h-12 w-12 text-muted-foreground/50" />
            <h3 className="text-lg font-medium">没有匹配的项目</h3>
            <p className="mt-1 text-sm text-muted-foreground">
              调整搜索关键词或筛选条件后再试
            </p>
          </div>
        ) : (
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {filteredProjects.map((project) => (
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
                    <div className="mt-3 flex items-center gap-2">
                      {project.latest_version ? (
                        <>
                          <Badge
                            variant={
                              project.latest_version.status === "shipped"
                                ? "default"
                                : "secondary"
                            }
                          >
                            {project.latest_version.status === "shipped"
                              ? "已发货"
                              : "待发货"}
                          </Badge>
                          <span className="text-xs text-muted-foreground">
                            最新版本 {project.latest_version.version_number}
                          </span>
                        </>
                      ) : (
                        <Badge variant="outline">未创建版本</Badge>
                      )}
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
