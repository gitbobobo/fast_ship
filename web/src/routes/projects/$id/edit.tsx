import { useParams, useNavigate } from "react-router";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { Header } from "@/components/layout/header";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { projectEditSchema, type ProjectEditInput } from "@/lib/utils/validators";
import { useProject, useUpdateProject } from "@/lib/hooks/use-projects";
import { toast } from "sonner";
import { useEffect } from "react";

export default function EditProjectPage() {
  const { id } = useParams();
  const navigate = useNavigate();
  const { data: project, isLoading } = useProject(id!);
  const updateProject = useUpdateProject(id!);

  const {
    register,
    handleSubmit,
    reset,
    formState: { errors, isSubmitting },
  } = useForm<ProjectEditInput>({ resolver: zodResolver(projectEditSchema) });

  useEffect(() => {
    if (project) {
      reset({
        name: project.name,
        description: project.description || "",
        github_owner: project.github_owner,
        github_repo: project.github_repo,
        github_token: "",
      });
    }
  }, [project, reset]);

  const onSubmit = async (data: ProjectEditInput) => {
    try {
      const payload: Record<string, string> = {
        name: data.name,
        description: data.description || "",
        github_owner: data.github_owner,
        github_repo: data.github_repo,
      };
      if (data.github_token) {
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

  return (
    <>
      <Header title="编辑项目" />
      <div className="p-4 md:p-6">
        <Card className="mx-auto max-w-xl">
          <CardHeader>
            <CardTitle>编辑项目</CardTitle>
          </CardHeader>
          <CardContent>
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
                <Textarea id="description" {...register("description")} />
              </div>

              <div className="grid gap-4 sm:grid-cols-2">
                <div className="space-y-2">
                  <Label htmlFor="github_owner">GitHub Owner</Label>
                  <Input id="github_owner" {...register("github_owner")} />
                  {errors.github_owner && (
                    <p className="text-xs text-destructive">
                      {errors.github_owner.message}
                    </p>
                  )}
                </div>
                <div className="space-y-2">
                  <Label htmlFor="github_repo">GitHub Repo</Label>
                  <Input id="github_repo" {...register("github_repo")} />
                  {errors.github_repo && (
                    <p className="text-xs text-destructive">
                      {errors.github_repo.message}
                    </p>
                  )}
                </div>
              </div>

              <div className="space-y-2">
                <Label htmlFor="github_token">GitHub Access Token</Label>
                <Input
                  id="github_token"
                  type="password"
                  placeholder="留空则不修改"
                  {...register("github_token")}
                />
                <p className="text-xs text-muted-foreground">
                  留空表示不修改当前 Token
                </p>
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
