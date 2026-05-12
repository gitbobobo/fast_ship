import { useEffect } from "react";
import { useParams, useNavigate } from "react-router";
import { useForm, useWatch } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { Header } from "@/components/layout/header";
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
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { projectEditSchema, type ProjectEditInput } from "@/lib/utils/validators";
import { useProject, useUpdateProject, useProjects } from "@/lib/hooks/use-projects";
import { useTokenSource } from "@/lib/hooks/use-token-source";
import { parseRepoUrl } from "@/lib/utils/github";
import { toast } from "sonner";

export default function EditProjectPage() {
  const { id } = useParams();
  const navigate = useNavigate();
  const { data: project, isLoading } = useProject(id!);
  const updateProject = useUpdateProject(id!);
  const { data: existingProjects } = useProjects();

  const {
    control,
    register,
    handleSubmit,
    reset,
    setValue,
    formState: { errors, isSubmitting },
  } = useForm<ProjectEditInput>({ resolver: zodResolver(projectEditSchema) });

  const repositoryUrl = useWatch({ control, name: "repository_url" }) || "";
  const { owner, repo } = parseRepoUrl(repositoryUrl);

  const { tokenSource, handleTokenSourceChange } = useTokenSource(setValue);

  useEffect(() => {
    if (project) {
      reset({
        name: project.name,
        description: project.description || "",
        repository_url: `https://github.com/${project.github_owner}/${project.github_repo}`,
        github_token: "",
        source_project_id: undefined,
      });
    }
  }, [project, reset]);

  const onSubmit = async (data: ProjectEditInput) => {
    try {
      const payload: Record<string, string> = { name: data.name };
      if (data.description) payload.description = data.description;
      if (data.repository_url) payload.repository_url = data.repository_url;
      if (data.source_project_id) {
        payload.source_project_id = data.source_project_id;
      } else if (data.github_token) {
        payload.github_token = data.github_token;
      }
      await updateProject.mutateAsync(payload);
      toast.success("项目已更新");
      navigate(`/projects/${id}`);
    } catch {
      toast.error("更新失败，请检查输入");
    }
  };

  if (isLoading) {
    return (
      <>
        <Header title="编辑项目" />
        <div className="p-4 md:p-6">
          <Skeleton className="mx-auto h-96 max-w-xl rounded-lg" />
        </div>
      </>
    );
  }

  const hasExistingProjects =
    existingProjects &&
    existingProjects.items.length > 0;

  return (
    <>
      <Header title="编辑项目" />
      <div className="p-4 md:p-6">
        <Card className="mx-auto max-w-xl">
          <CardHeader className="pt-4 pb-2">
            <CardTitle>编辑项目</CardTitle>
          </CardHeader>
          <CardContent className="pt-2">
            <form onSubmit={handleSubmit(onSubmit)} className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="name">项目名称</Label>
                <Input id="name" {...register("name")} />
                {errors.name && (
                  <p className="text-xs text-destructive">
                    {errors.name.message}
                  </p>
                )}
              </div>

              <div className="space-y-2">
                <Label htmlFor="description">项目描述（可选）</Label>
                <Textarea
                  id="description"
                  placeholder="简单描述你的项目"
                  {...register("description")}
                />
              </div>

              <div className="space-y-2">
                <Label htmlFor="repository_url">仓库链接</Label>
                <Input
                  id="repository_url"
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
                  <Label htmlFor="github_token">GitHub Access Token</Label>
                  <GitHubTokenHelpDialog owner={owner} repo={repo} />
                </div>

                {hasExistingProjects && (
                  <Select
                    value={tokenSource}
                    onValueChange={handleTokenSourceChange}
                  >
                    <SelectTrigger className="w-full" size="default">
                      <SelectValue>
                        {tokenSource === ""
                          ? "不修改 / 输入新 Token"
                          : existingProjects?.items.find((p) => p.id === tokenSource)?.name ?? "选择 Token 来源（留空则不修改）"}
                      </SelectValue>
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="">不修改 / 输入新 Token</SelectItem>
                      {existingProjects.items
                        .filter((p) => p.id !== id)
                        .map((proj) => (
                          <SelectItem key={proj.id} value={proj.id}>
                            {proj.name} ({proj.github_owner}/{proj.github_repo})
                          </SelectItem>
                        ))}
                    </SelectContent>
                  </Select>
                )}

                {(!hasExistingProjects || tokenSource === "") && (
                  <>
                    <Input
                      id="github_token"
                      type="password"
                      placeholder="留空则不修改"
                      {...register("github_token")}
                    />
                    {errors.github_token && (
                      <p className="text-xs text-destructive">
                        {errors.github_token.message}
                      </p>
                    )}
                    <p className="text-xs text-muted-foreground">
                      留空表示不修改当前 Token。推荐 Fine-grained Token（前缀
                      github_pat_），并确保选中目标仓库且具备 Contents 写权限
                    </p>
                  </>
                )}
              </div>

              <div className="flex gap-3 pt-2">
                <Button type="submit" disabled={isSubmitting}>
                  {isSubmitting ? "保存中..." : "保存"}
                </Button>
                <Button
                  type="button"
                  variant="outline"
                  onClick={() => navigate(`/projects/${id}`)}
                >
                  取消
                </Button>
              </div>
            </form>
          </CardContent>
        </Card>
      </div>
    </>
  );
}
