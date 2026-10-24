import { useDeferredValue, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useSearchParams } from "react-router";
import {
  Plus,
  GitFork,
  FolderOpen,
  Search,
  MoreHorizontal,
  Pencil,
  Trash2,
  Archive,
  ArchiveRestore,
  ChevronRight,
} from "lucide-react";
import { ProjectFormDialog } from "@/components/projects/project-form-dialog";
import { ShelveProjectDialog } from "@/components/projects/shelve-project-dialog";
import { Header } from "@/components/layout/header";
import { HeaderActions } from "@/components/layout/header-actions";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import {
  useProjects,
  useDeleteProject,
  useUnshelveProject,
} from "@/lib/hooks/use-projects";
import { shelvedStatusLabel } from "@/lib/projects";
import { formatRelativeTime } from "@/lib/utils/format";
import { hasGitHubRepo, repoSlug } from "@/lib/utils/github";
import { cn } from "@/lib/utils";
import { toast } from "sonner";

export default function ProjectsPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const { data, isLoading } = useProjects();
  const projects = useMemo(() => data?.items ?? [], [data]);
  const [search, setSearch] = useState("");
  const deferredSearch = useDeferredValue(search.trim().toLowerCase());

  const [deleteTarget, setDeleteTarget] = useState<Project | null>(null);
  const deleteProject = useDeleteProject();

  const [shelveTarget, setShelveTarget] = useState<Project | null>(null);
  const unshelveProject = useUnshelveProject();
  const [shelvedGroupOpen, setShelvedGroupOpen] = useState(false);

  const [dialogOpen, setDialogOpen] = useState(false);
  const [dialogMode, setDialogMode] = useState<"create" | "edit">("create");
  const [editingProjectId, setEditingProjectId] = useState<string | undefined>();
  const [dialogSection, setDialogSection] = useState<string | undefined>();
  // ?edit=<projectId>&section=t3 深链：打开一次目标项目的编辑弹框后即清掉参数
  const deepLinkHandledRef = useRef(false);
  useEffect(() => {
    if (deepLinkHandledRef.current || isLoading) return;
    const editId = searchParams.get("edit");
    if (!editId || !projects.some((p) => p.id === editId)) return;
    deepLinkHandledRef.current = true;
    setDialogMode("edit");
    setEditingProjectId(editId);
    setDialogSection(searchParams.get("section") ?? undefined);
    setDialogOpen(true);
    setSearchParams(
      (current) => {
        const next = new URLSearchParams(current);
        next.delete("edit");
        next.delete("section");
        return next;
      },
      { replace: true },
    );
  }, [isLoading, projects, searchParams, setSearchParams]);

  // 顺序由 useProjects 统一提供（Issue 总数降序），这里只按搜索词过滤；
  // 搁置项目不进日常网格，收到底部默认折叠的「已搁置」分组
  const filteredProjects = projects.filter((project) => {
    const slug = repoSlug(project);
    const matchesSearch =
      deferredSearch.length === 0 ||
      project.name.toLowerCase().includes(deferredSearch) ||
      project.description?.toLowerCase().includes(deferredSearch) ||
      slug.toLowerCase().includes(deferredSearch);

    return matchesSearch;
  });
  const activeProjects = filteredProjects.filter((p) => !p.is_shelved);
  const shelvedProjects = filteredProjects.filter((p) => p.is_shelved);

  const handleDelete = async () => {
    if (!deleteTarget) return;
    try {
      await deleteProject.mutateAsync(deleteTarget.id);
      toast.success("项目已删除");
    } catch {
      toast.error("删除失败");
    } finally {
      setDeleteTarget(null);
    }
  };

  const handleUnshelve = async (project: Project) => {
    try {
      await unshelveProject.mutateAsync(project.id);
      toast.success("已取消搁置");
    } catch {
      toast.error("取消搁置失败");
    }
  };

  const openCreateDialog = () => {
    setDialogMode("create");
    setEditingProjectId(undefined);
    setDialogOpen(true);
  };

  const cardHandlers = {
    onEdit: (project: Project) => {
      setDialogMode("edit");
      setEditingProjectId(project.id);
      setDialogOpen(true);
    },
    onShelve: setShelveTarget,
    onUnshelve: (project: Project) => {
      void handleUnshelve(project);
    },
    onDelete: setDeleteTarget,
  };

  return (
    <>
      <Header
        title="项目"
        actions={
          <HeaderActions
            primary={
              <Button size="sm" onClick={openCreateDialog}>
                <Plus className="mr-2 h-4 w-4" />
                创建项目
              </Button>
            }
          />
        }
      />
      <div className="p-4 md:p-6">
        <div className="mb-6 flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
          <div className="relative w-full sm:w-72">
            <Search className="pointer-events-none absolute top-1/2 left-2.5 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              className="pl-8"
              placeholder="搜索项目名、描述或仓库"
            />
          </div>
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
          </div>
        ) : filteredProjects.length === 0 ? (
          <div className="flex flex-col items-center justify-center rounded-lg border border-dashed py-16 text-center">
            <FolderOpen className="mb-4 h-12 w-12 text-muted-foreground/50" />
            <h3 className="text-lg font-medium">没有匹配的项目</h3>
            <p className="mt-1 text-sm text-muted-foreground">
              调整搜索关键词后再试
            </p>
          </div>
        ) : (
          <>
            {activeProjects.length > 0 && (
              <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
                {activeProjects.map((project) => (
                  <ProjectCard
                    key={project.id}
                    project={project}
                    {...cardHandlers}
                  />
                ))}
              </div>
            )}
            {shelvedProjects.length > 0 && (
              <section className={cn(activeProjects.length > 0 && "mt-8")}>
                <button
                  type="button"
                  aria-expanded={shelvedGroupOpen}
                  onClick={() => setShelvedGroupOpen((open) => !open)}
                  className="flex items-center gap-1.5 text-sm font-medium text-muted-foreground transition-colors hover:text-foreground"
                >
                  <ChevronRight
                    aria-hidden="true"
                    className={cn(
                      "h-4 w-4 transition-transform",
                      shelvedGroupOpen && "rotate-90",
                    )}
                  />
                  已搁置 ({shelvedProjects.length})
                </button>
                {shelvedGroupOpen && (
                  <div className="mt-4 grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
                    {shelvedProjects.map((project) => (
                      <ProjectCard
                        key={project.id}
                        project={project}
                        {...cardHandlers}
                      />
                    ))}
                  </div>
                )}
              </section>
            )}
          </>
        )}
      </div>

      <ProjectFormDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        mode={dialogMode}
        projectId={editingProjectId}
        section={dialogSection}
      />

      <ShelveProjectDialog
        project={shelveTarget}
        onClose={() => setShelveTarget(null)}
      />

      <AlertDialog
        open={!!deleteTarget}
        onOpenChange={(open) => {
          if (!open) setDeleteTarget(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>确认删除项目?</AlertDialogTitle>
            <AlertDialogDescription>
              删除后，项目下所有版本和安装包数据将一并删除，且不可恢复。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel onClick={() => setDeleteTarget(null)}>
              取消
            </AlertDialogCancel>
            <AlertDialogAction onClick={handleDelete}>
              确认删除
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

function ProjectCard({
  project,
  onEdit,
  onShelve,
  onUnshelve,
  onDelete,
}: {
  project: Project;
  onEdit: (project: Project) => void;
  onShelve: (project: Project) => void;
  onUnshelve: (project: Project) => void;
  onDelete: (project: Project) => void;
}) {
  const navigate = useNavigate();
  const shelveLabel = shelvedStatusLabel(project);

  return (
    <Card
      className="relative flex flex-col transition-colors hover:border-primary/50 cursor-pointer p-3"
      onClick={() => navigate(`/issues?project=${project.id}`)}
    >
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0 flex-1 space-y-1">
          <h3 className="text-base font-semibold truncate">{project.name}</h3>
          {project.description && (
            <p className="text-sm text-muted-foreground line-clamp-2">
              {project.description}
            </p>
          )}
        </div>
        <DropdownMenu>
          <DropdownMenuTrigger
            onClick={(e) => e.stopPropagation()}
            className="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-md text-sm font-medium transition-colors hover:bg-accent hover:text-accent-foreground focus-visible:outline-hidden focus-visible:ring-1 focus-visible:ring-ring"
            aria-label="更多操作"
          >
            <MoreHorizontal className="h-4 w-4" />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem
              onClick={(e) => {
                e.stopPropagation();
                onEdit(project);
              }}
            >
              <Pencil className="mr-2 h-4 w-4" />
              编辑
            </DropdownMenuItem>
            {project.is_shelved ? (
              <DropdownMenuItem
                onClick={(e) => {
                  e.stopPropagation();
                  onUnshelve(project);
                }}
              >
                <ArchiveRestore className="mr-2 h-4 w-4" />
                取消搁置
              </DropdownMenuItem>
            ) : (
              <DropdownMenuItem
                onClick={(e) => {
                  e.stopPropagation();
                  onShelve(project);
                }}
              >
                <Archive className="mr-2 h-4 w-4" />
                搁置…
              </DropdownMenuItem>
            )}
            <DropdownMenuItem
              variant="destructive"
              onClick={(e) => {
                e.stopPropagation();
                onDelete(project);
              }}
            >
              <Trash2 className="mr-2 h-4 w-4" />
              删除
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      <div className="mt-4 flex flex-1 flex-col justify-end gap-3">
        <div className="flex items-center gap-2 text-xs text-muted-foreground">
          <GitFork className="h-3.5 w-3.5" />
          <span className="truncate">
            {hasGitHubRepo(project) ? repoSlug(project) : "未关联 GitHub"}
          </span>
        </div>
        <div className="flex items-center gap-2">
          {shelveLabel && <Badge variant="secondary">{shelveLabel}</Badge>}
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
          <span className="ml-auto text-xs text-muted-foreground">
            {project.issue_count ?? 0} 个问题
          </span>
        </div>
        <p className="text-xs text-muted-foreground">
          {formatRelativeTime(project.updated_at)}
        </p>
      </div>
    </Card>
  );
}
