import { useParams, useNavigate } from "react-router";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { Header } from "@/components/layout/header";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { versionSchema, type VersionInput } from "@/lib/utils/validators";
import { useCreateVersion } from "@/lib/hooks/use-versions";
import { toast } from "sonner";

export default function NewVersionPage() {
  const { id } = useParams();
  const navigate = useNavigate();
  const createVersion = useCreateVersion(id!);

  const {
    register,
    handleSubmit,
    formState: { errors, isSubmitting },
  } = useForm<VersionInput>({ resolver: zodResolver(versionSchema) });

  const onSubmit = async (data: VersionInput) => {
    try {
      const res = await createVersion.mutateAsync(data);
      toast.success("版本创建成功");
      navigate(`/projects/${id}/versions/${res.data.id}`);
    } catch {
      toast.error("创建失败，版本号可能已存在");
    }
  };

  return (
    <>
      <Header title="创建版本" />
      <div className="p-4 md:p-6">
        <Card className="mx-auto max-w-xl">
          <CardHeader>
            <CardTitle>新建版本</CardTitle>
          </CardHeader>
          <CardContent>
            <form onSubmit={handleSubmit(onSubmit)} className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="version_number">版本号</Label>
                <Input
                  id="version_number"
                  placeholder="v1.0.0"
                  {...register("version_number")}
                />
                {errors.version_number && (
                  <p className="text-xs text-destructive">
                    {errors.version_number.message}
                  </p>
                )}
              </div>

              <div className="space-y-2">
                <Label htmlFor="target_commitish">
                  目标分支 / Commit（可选）
                </Label>
                <Input
                  id="target_commitish"
                  placeholder="main 或 commit SHA"
                  {...register("target_commitish")}
                />
                <p className="text-xs text-muted-foreground">
                  用于在 GitHub 上创建 Tag，可稍后补充
                </p>
              </div>

              <div className="space-y-2">
                <Label htmlFor="release_notes">Release 说明（可选）</Label>
                <Textarea
                  id="release_notes"
                  placeholder="支持 Markdown 格式，可稍后补充"
                  rows={6}
                  {...register("release_notes")}
                />
              </div>

              <div className="flex gap-3 pt-2">
                <Button type="submit" disabled={isSubmitting}>
                  {isSubmitting ? "创建中..." : "创建版本"}
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
