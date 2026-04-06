import { useNavigate } from "react-router";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { Header } from "@/components/layout/header";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
  DialogFooter,
} from "@/components/ui/dialog";
import { projectSchema, type ProjectInput } from "@/lib/utils/validators";
import { useCreateProject } from "@/lib/hooks/use-projects";
import { toast } from "sonner";
import { ExternalLinkIcon } from "lucide-react";

export default function NewProjectPage() {
  const navigate = useNavigate();
  const createProject = useCreateProject();

  const {
    register,
    handleSubmit,
    formState: { errors, isSubmitting },
  } = useForm<ProjectInput>({ resolver: zodResolver(projectSchema) });

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
                  <Dialog>
                    <DialogTrigger
                      render={
                        <Button
                          type="button"
                          variant="ghost"
                          size="sm"
                          className="h-auto gap-1 px-1 py-0 text-xs text-muted-foreground hover:text-foreground"
                        />
                      }
                    >
                      如何获取？
                      <ExternalLinkIcon className="size-3" />
                    </DialogTrigger>
                    <DialogContent className="sm:max-w-md">
                      <DialogHeader>
                        <DialogTitle>如何获取 GitHub Access Token</DialogTitle>
                      </DialogHeader>
                      <div className="space-y-4 text-sm">
                        <div className="space-y-3">
                          <p className="text-muted-foreground">
                            需要创建一个具有 <code className="rounded bg-muted px-1 py-0.5 font-mono text-xs">repo</code> 权限的 Personal Access Token（PAT），用于访问你的 GitHub 仓库。
                          </p>
                          <ol className="space-y-2.5 text-foreground">
                            <li className="flex gap-2">
                              <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-primary/10 text-xs font-semibold text-primary">1</span>
                              <span>登录 GitHub，点击右上角头像，选择 <strong>Settings</strong></span>
                            </li>
                            <li className="flex gap-2">
                              <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-primary/10 text-xs font-semibold text-primary">2</span>
                              <span>在左侧菜单最底部，点击 <strong>Developer settings</strong></span>
                            </li>
                            <li className="flex gap-2">
                              <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-primary/10 text-xs font-semibold text-primary">3</span>
                              <span>选择 <strong>Personal access tokens</strong> → <strong>Tokens (classic)</strong></span>
                            </li>
                            <li className="flex gap-2">
                              <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-primary/10 text-xs font-semibold text-primary">4</span>
                              <span>点击 <strong>Generate new token</strong> → <strong>Generate new token (classic)</strong></span>
                            </li>
                            <li className="flex gap-2">
                              <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-primary/10 text-xs font-semibold text-primary">5</span>
                              <span>填写备注名称，在 <strong>Scopes</strong> 中勾选 <code className="rounded bg-muted px-1 py-0.5 font-mono text-xs">repo</code> 权限</span>
                            </li>
                            <li className="flex gap-2">
                              <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-primary/10 text-xs font-semibold text-primary">6</span>
                              <span>点击 <strong>Generate token</strong>，复制生成的 Token（仅显示一次）</span>
                            </li>
                          </ol>
                        </div>
                        <div className="rounded-lg border border-amber-200 bg-amber-50 p-3 text-amber-800 dark:border-amber-900 dark:bg-amber-950/30 dark:text-amber-400">
                          <p className="text-xs">Token 生成后只显示一次，请立即复制保存。如果遗失，需重新生成。</p>
                        </div>
                      </div>
                      <DialogFooter showCloseButton>
                        <a
                          href="https://github.com/settings/tokens/new"
                          target="_blank"
                          rel="noopener noreferrer"
                          className="inline-flex items-center gap-1.5 text-xs text-primary underline underline-offset-2 hover:no-underline"
                        >
                          前往 GitHub 创建 Token
                          <ExternalLinkIcon className="size-3" />
                        </a>
                      </DialogFooter>
                    </DialogContent>
                  </Dialog>
                </div>
                <Input
                  id="github_token"
                  type="password"
                  placeholder="ghp_xxxxxxxxxxxx"
                  {...register("github_token")}
                />
                {errors.github_token && (
                  <p className="text-xs text-destructive">
                    {errors.github_token.message}
                  </p>
                )}
                <p className="text-xs text-muted-foreground">
                  需要 repo 权限的 Personal Access Token
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
