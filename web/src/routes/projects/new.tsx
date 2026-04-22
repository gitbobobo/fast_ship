import { useNavigate } from "react-router";
import { useForm, useWatch } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { Header } from "@/components/layout/header";
import { GitHubTokenHelpDialog } from "@/components/projects/github-token-help-dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { projectSchema, type ProjectInput } from "@/lib/utils/validators";
import { useCreateProject } from "@/lib/hooks/use-projects";
import { toast } from "sonner";

export default function NewProjectPage() {
  const navigate = useNavigate();
  const createProject = useCreateProject();

  const {
    control,
    register,
    handleSubmit,
    formState: { errors, isSubmitting },
  } = useForm<ProjectInput>({ resolver: zodResolver(projectSchema) });
  const [githubOwner = "", githubRepo = ""] = useWatch({
    control,
    name: ["github_owner", "github_repo"],
  });

  const onSubmit = async (data: ProjectInput) => {
    try {
      const res = await createProject.mutateAsync(data);
      toast.success("项目创建成功");
      navigate(`/projects/${res.data.id}`);
    } catch {
      toast.error("创建失败，请检查输入");
    }
  };

  return (
    <>
      <Header title="创建项目" />
      <div className="p-4 md:p-6">
        <Card className="mx-auto max-w-xl">
          <CardHeader>
            <CardTitle>新建项目</CardTitle>
          </CardHeader>
          <CardContent>
            <form onSubmit={handleSubmit(onSubmit)} className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="name">项目名称</Label>
                <Input id="name" placeholder="my-app" {...register("name")} />
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

              <div className="grid gap-4 sm:grid-cols-2">
                <div className="space-y-2">
                  <Label htmlFor="github_owner">GitHub Owner</Label>
                  <Input
                    id="github_owner"
                    placeholder="owner"
                    {...register("github_owner")}
                  />
                  {errors.github_owner && (
                    <p className="text-xs text-destructive">
                      {errors.github_owner.message}
                    </p>
                  )}
                </div>
                <div className="space-y-2">
                  <Label htmlFor="github_repo">GitHub Repo</Label>
                  <Input
                    id="github_repo"
                    placeholder="repo"
                    {...register("github_repo")}
                  />
                  {errors.github_repo && (
                    <p className="text-xs text-destructive">
                      {errors.github_repo.message}
                    </p>
                  )}
                </div>
              </div>

              <div className="space-y-2">
                <div className="flex items-center justify-between">
                  <Label htmlFor="github_token">GitHub Access Token</Label>
                  <GitHubTokenHelpDialog
                    owner={githubOwner}
                    repo={githubRepo}
                  />
                </div>
                <Input
                  id="github_token"
                  type="password"
                  placeholder="github_pat_xxx 或 ghp_xxx"
                  {...register("github_token")}
                />
                {errors.github_token && (
                  <p className="text-xs text-destructive">
                    {errors.github_token.message}
                  </p>
                )}
                <p className="text-xs text-muted-foreground">
                  推荐 Fine-grained Token（前缀 github_pat_），并确保选中目标仓库且具备
                  Contents 写权限
                </p>
              </div>

              <div className="flex gap-3 pt-2">
                <Button type="submit" disabled={isSubmitting}>
                  {isSubmitting ? "创建中..." : "创建项目"}
                </Button>
                <Button
                  type="button"
                  variant="outline"
                  onClick={() => navigate(-1)}
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
