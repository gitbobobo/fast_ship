import { useEffect } from "react";
import { useNavigate } from "react-router";
import { useForm, useWatch } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { GitHubTokenHelpDialog } from "@/components/projects/github-token-help-dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  projectSchema,
  projectEditSchema,
  type ProjectInput,
  type ProjectEditInput,
} from "@/lib/utils/validators";
import {
  useProject,
  useCreateProject,
  useUpdateProject,
  useProjects,
} from "@/lib/hooks/use-projects";
import { useTokenSource } from "@/lib/hooks/use-token-source";
import { parseRepoUrl, hasGitHubRepo, repoSlug } from "@/lib/utils/github";
import { toast } from "sonner";

interface ProjectFormDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  mode: "create" | "edit";
  projectId?: string;
}

export function ProjectFormDialog({
  open,
  onOpenChange,
  mode,
  projectId,
}: ProjectFormDialogProps) {
  const navigate = useNavigate();
  const isEdit = mode === "edit";
  const { data: project, isLoading } = useProject(projectId ?? "");
  const createProject = useCreateProject();
  const updateProject = useUpdateProject(projectId ?? "");
  const { data: existingProjects } = useProjects();

  const schema = isEdit ? projectEditSchema : projectSchema;
  type FormData = ProjectInput | ProjectEditInput;

  const {
    control,
    register,
    handleSubmit,
    reset,
    setValue,
    formState: { errors, isSubmitting },
  } = useForm<FormData>({
    resolver: zodResolver(schema),
  });

  const repositoryUrl = useWatch({ control, name: "repository_url" }) || "";
  const { owner, repo } = parseRepoUrl(repositoryUrl);

  const { tokenSource, handleTokenSourceChange } = useTokenSource(setValue);

  useEffect(() => {
    if (isEdit && project) {
      reset({
        name: project.name,
        description: project.description || "",
        repository_url: hasGitHubRepo(project)
          ? `https://github.com/${project.github_owner}/${project.github_repo}`
          : "",
        github_token: "",
        source_project_id: undefined,
      });
    } else if (!isEdit && open) {
      reset({
        name: "",
        description: "",
        repository_url: "",
        github_token: "",
        source_project_id: undefined,
      });
    }
  }, [isEdit, project, open, reset]);

  const onSubmit = async (data: FormData) => {
    try {
      if (isEdit) {
        const payload: Record<string, string> = { name: data.name };
        if (data.description !== undefined) {
          payload.description = data.description;
        }
        if (data.repository_url) payload.repository_url = data.repository_url;
        const editData = data as ProjectEditInput;
        if (editData.source_project_id) {
          payload.source_project_id = editData.source_project_id;
        } else if (editData.github_token) {
          payload.github_token = editData.github_token;
        }
        await updateProject.mutateAsync(payload);
        toast.success("项目已更新");
        onOpenChange(false);
      } else {
        const createData = data as ProjectInput;
        const payload: {
          name: string;
          description?: string;
          repository_url?: string;
          github_token?: string;
          source_project_id?: string;
        } = {
          name: createData.name,
        };
        if (createData.description) {
          payload.description = createData.description;
        }
        if (createData.repository_url) {
          payload.repository_url = createData.repository_url;
        }
        if (createData.source_project_id) {
          payload.source_project_id = createData.source_project_id;
        } else if (createData.github_token) {
          payload.github_token = createData.github_token;
        }
        const res = await createProject.mutateAsync(payload);
        toast.success("项目创建成功");
        onOpenChange(false);
        navigate(`/issues?project=${res.data.id}`);
      }
    } catch {
      toast.error(isEdit ? "更新失败，请检查输入" : "创建失败，请检查输入");
    }
  };

  const hasExistingProjects =
    existingProjects && existingProjects.items.length > 0;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg max-h-[90vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{isEdit ? "编辑项目" : "新建项目"}</DialogTitle>
        </DialogHeader>

        {isEdit && isLoading ? (
          <div className="py-4 space-y-4">
            <Skeleton className="h-10 w-full" />
            <Skeleton className="h-20 w-full" />
            <Skeleton className="h-10 w-full" />
            <Skeleton className="h-10 w-full" />
          </div>
        ) : (
          <form onSubmit={handleSubmit(onSubmit)} className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="project-name">项目名称</Label>
              <Input
                id="project-name"
                placeholder={isEdit ? undefined : "my-app"}
                {...register("name")}
              />
              {errors.name && (
                <p className="text-xs text-destructive">
                  {errors.name.message}
                </p>
              )}
            </div>

            <div className="space-y-2">
              <Label htmlFor="project-description">项目描述（可选）</Label>
              <Textarea
                id="project-description"
                placeholder="简单描述你的项目"
                {...register("description")}
              />
            </div>

            <div className="space-y-2">
              <Label htmlFor="project-repository_url">仓库链接（可选）</Label>
              <Input
                id="project-repository_url"
                placeholder="https://github.com/owner/repo 或 owner/repo"
                {...register("repository_url")}
              />
              {errors.repository_url && (
                <p className="text-xs text-destructive">
                  {errors.repository_url.message}
                </p>
              )}
            </div>

            <div className="space-y-2">
              <div className="flex items-center justify-between">
                <Label htmlFor="project-github_token">GitHub Access Token{!repositoryUrl && "（请先填写仓库链接）"}</Label>
                {repositoryUrl && <GitHubTokenHelpDialog owner={owner} repo={repo} />}
              </div>

              {hasExistingProjects && (
                <Select
                  value={tokenSource}
                  onValueChange={handleTokenSourceChange}
                  disabled={!repositoryUrl}
                >
                  <SelectTrigger className="w-full" size="default">
                    <SelectValue>
                      {tokenSource === ""
                        ? isEdit
                          ? "不修改 / 输入新 Token"
                          : "输入新 Token"
                        : existingProjects?.items.find((p) => p.id === tokenSource)?.name ?? "选择 Token 来源"}
                    </SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="">
                      {isEdit ? "不修改 / 输入新 Token" : "输入新 Token"}
                    </SelectItem>
                    {existingProjects.items
                      .filter((p) => !isEdit || p.id !== projectId)
                      .map((proj) => (
                        <SelectItem key={proj.id} value={proj.id}>
                          {proj.name}{hasGitHubRepo(proj) ? ` (${repoSlug(proj)})` : ""}
                        </SelectItem>
                      ))}
                  </SelectContent>
                </Select>
              )}

              {(!hasExistingProjects || tokenSource === "") && (
                <>
                  <Input
                    id="project-github_token"
                    type="password"
                    placeholder={isEdit ? "留空则不修改" : "github_pat_xxx 或 ghp_xxx"}
                    disabled={!repositoryUrl}
                    {...register("github_token")}
                  />
                  {errors.github_token && (
                    <p className="text-xs text-destructive">
                      {errors.github_token.message}
                    </p>
                  )}
                  <p className="text-xs text-muted-foreground">
                    {isEdit
                      ? "留空表示不修改当前 Token。推荐 Fine-grained Token（前缀 github_pat_），并确保选中目标仓库且具备 Contents 写权限"
                      : "推荐 Fine-grained Token（前缀 github_pat_），并确保选中目标仓库且具备 Contents 写权限"}
                  </p>
                </>
              )}
            </div>

            <div className="flex gap-3 pt-2">
              <Button type="submit" disabled={isSubmitting}>
                {isSubmitting
                  ? isEdit
                    ? "保存中..."
                    : "创建中..."
                  : isEdit
                    ? "保存"
                    : "创建项目"}
              </Button>
              <Button
                type="button"
                variant="outline"
                onClick={() => onOpenChange(false)}
              >
                取消
              </Button>
            </div>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}
