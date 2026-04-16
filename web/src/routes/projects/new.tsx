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
                    <DialogContent className="sm:max-w-lg">
                      <DialogHeader>
                        <DialogTitle>如何获取 GitHub Access Token</DialogTitle>
                      </DialogHeader>
                      <div className="space-y-5 text-sm">
                        {/* Fine-grained Token (Recommended) */}
                        <div className="rounded-lg border border-green-200 bg-green-50/50 p-4 dark:border-green-900 dark:bg-green-950/20">
                          <div className="mb-3 flex items-center gap-2">
                            <span className="inline-flex items-center rounded-full bg-green-100 px-2 py-0.5 text-xs font-medium text-green-800 dark:bg-green-900 dark:text-green-200">
                              推荐
                            </span>
                            <span className="font-semibold text-green-900 dark:text-green-200">
                              Fine-grained personal access token
                            </span>
                          </div>
                          <p className="mb-3 text-muted-foreground">
                            权限更精细、更安全，仅授予特定仓库所需的权限。
                          </p>
                          <ol className="space-y-2 text-foreground">
                            <li className="flex gap-2">
                              <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-green-100 text-xs font-semibold text-green-800 dark:bg-green-900 dark:text-green-200">1</span>
                              <span>前往 <a href="https://github.com/settings/personal-access-tokens/new" target="_blank" rel="noopener noreferrer" className="text-primary underline underline-offset-2 hover:no-underline">GitHub Token 创建页</a></span>
                            </li>
                            <li className="flex gap-2">
                              <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-green-100 text-xs font-semibold text-green-800 dark:bg-green-900 dark:text-green-200">2</span>
                              <span>填写 Token name（如：FastShip）</span>
                            </li>
                            <li className="flex gap-2">
                              <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-green-100 text-xs font-semibold text-green-800 dark:bg-green-900 dark:text-green-200">3</span>
                              <span>设置 <strong>Expiration</strong> 时，建议选择不超过 <strong>366 天</strong>，部分组织会拒绝更长期限的 Fine-grained Token</span>
                            </li>
                            <li className="flex gap-2">
                              <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-green-100 text-xs font-semibold text-green-800 dark:bg-green-900 dark:text-green-200">4</span>
                              <span>在 <strong>Repository access</strong> 中选择该 Token 可访问的仓库</span>
                            </li>
                            <li className="flex gap-2">
                              <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-green-100 text-xs font-semibold text-green-800 dark:bg-green-900 dark:text-green-200">5</span>
                              <span>在 <strong>Permissions</strong> → <strong>Repository permissions</strong> 中设置 <code className="rounded bg-muted px-1 py-0.5 font-mono text-xs">Contents</code> 为 <strong>Read and write</strong></span>
                            </li>
                            <li className="flex gap-2">
                              <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-green-100 text-xs font-semibold text-green-800 dark:bg-green-900 dark:text-green-200">6</span>
                              <span>点击 <strong>Generate token</strong>，复制生成的 Token（仅显示一次）</span>
                            </li>
                          </ol>
                        </div>

                        {/* Classic Token */}
                        <div className="rounded-lg border border-border p-4">
                          <div className="mb-3 flex items-center gap-2">
                            <span className="inline-flex items-center rounded-full bg-muted px-2 py-0.5 text-xs font-medium text-muted-foreground">
                              备选
                            </span>
                            <span className="font-semibold">Classic personal access token</span>
                          </div>
                          <p className="mb-3 text-muted-foreground">
                            如果你需要访问多个仓库或组织仓库，可以使用 Classic Token。
                          </p>
                          <ol className="space-y-2 text-foreground">
                            <li className="flex gap-2">
                              <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-primary/10 text-xs font-semibold text-primary">1</span>
                              <span>前往 <strong>Settings</strong> → <strong>Developer settings</strong> → <strong>Personal access tokens</strong> → <strong>Tokens (classic)</strong></span>
                            </li>
                            <li className="flex gap-2">
                              <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-primary/10 text-xs font-semibold text-primary">2</span>
                              <span>点击 <strong>Generate new token (classic)</strong></span>
                            </li>
                            <li className="flex gap-2">
                              <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-primary/10 text-xs font-semibold text-primary">3</span>
                              <span>在 <strong>Scopes</strong> 中勾选 <code className="rounded bg-muted px-1 py-0.5 font-mono text-xs">repo</code> 权限</span>
                            </li>
                            <li className="flex gap-2">
                              <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-primary/10 text-xs font-semibold text-primary">4</span>
                              <span>点击 <strong>Generate token</strong>，复制生成的 Token</span>
                            </li>
                          </ol>
                        </div>

                        <div className="rounded-lg border border-amber-200 bg-amber-50 p-3 text-amber-800 dark:border-amber-900 dark:bg-amber-950/30 dark:text-amber-400">
                          <p className="text-xs">Token 生成后只显示一次，请立即复制保存。如果遗失，需重新生成。</p>
                        </div>
                      </div>
                      <DialogFooter>
                        <a
                          href="https://github.com/settings/personal-access-tokens/new"
                          target="_blank"
                          rel="noopener noreferrer"
                          className="inline-flex items-center gap-1.5 text-xs text-primary underline underline-offset-2 hover:no-underline"
                        >
                          前往 GitHub 创建 Fine-grained Token
                          <ExternalLinkIcon className="size-3" />
                        </a>
                      </DialogFooter>
                    </DialogContent>
                  </Dialog>
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
                  推荐 Fine-grained Token（前缀 github_pat_），权限更精细安全
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
