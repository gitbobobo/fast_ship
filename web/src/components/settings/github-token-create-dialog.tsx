import { useState } from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { githubTokenSchema, type GithubTokenInput } from "@/lib/utils/validators";
import { useCreateGithubToken } from "@/lib/hooks/use-github-tokens";
import { toast } from "sonner";

interface GithubTokenCreateDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** 创建成功后回调（携带新记录），项目表单用它选中新 Token */
  onCreated?: (token: GitHubToken) => void;
}

// 新建 GitHub Token 对话框：设置页与项目表单共用同一套。
// 明文只存在于表单里，服务端不回显；创建后服务端自动做一次检测。
export function GithubTokenCreateDialog({
  open,
  onOpenChange,
  onCreated,
}: GithubTokenCreateDialogProps) {
  const createToken = useCreateGithubToken();
  const [submitting, setSubmitting] = useState(false);

  const {
    register,
    handleSubmit,
    reset,
    formState: { errors },
  } = useForm<GithubTokenInput>({ resolver: zodResolver(githubTokenSchema) });

  const handleClose = () => {
    onOpenChange(false);
    reset();
  };

  const onSubmit = async (data: GithubTokenInput) => {
    setSubmitting(true);
    try {
      const res = await createToken.mutateAsync(data);
      toast.success("GitHub Token 已保存");
      onCreated?.(res.data);
      handleClose();
    } catch {
      toast.error("保存失败，请检查输入");
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>添加 GitHub Token</DialogTitle>
          <DialogDescription>
            Token 加密保存，不再回显；保存后会自动检测一次有效性
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={handleSubmit(onSubmit)}>
          <div className="space-y-4 py-2">
            <div className="space-y-2">
              <Label htmlFor="github-token-title">标题</Label>
              <Input
                id="github-token-title"
                placeholder="例如 主仓库发布 Token"
                {...register("title")}
              />
              {errors.title && (
                <p className="text-xs text-destructive">{errors.title.message}</p>
              )}
            </div>
            <div className="space-y-2">
              <Label htmlFor="github-token-value">Token</Label>
              <Input
                id="github-token-value"
                type="password"
                placeholder="github_pat_xxx 或 ghp_xxx"
                {...register("token")}
              />
              {errors.token && (
                <p className="text-xs text-destructive">{errors.token.message}</p>
              )}
              <p className="text-xs text-muted-foreground">
                推荐 Fine-grained Token（前缀 github_pat_），按用途授予最小权限
              </p>
            </div>
          </div>
          <DialogFooter className="mt-4">
            <Button type="button" variant="outline" onClick={handleClose}>
              取消
            </Button>
            <Button type="submit" disabled={submitting}>
              {submitting ? "保存中..." : "保存"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
