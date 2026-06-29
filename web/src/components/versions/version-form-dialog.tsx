import { useEffect } from "react";
import { useNavigate } from "react-router";
import { useForm, useWatch } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
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
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { versionSchema, type VersionInput } from "@/lib/utils/validators";
import { useProjectBranches } from "@/lib/hooks/use-projects";
import { useCreateVersion } from "@/lib/hooks/use-versions";
import { toast } from "sonner";

interface VersionFormDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  projectId: string;
}

export function VersionFormDialog({
  open,
  onOpenChange,
  projectId,
}: VersionFormDialogProps) {
  const navigate = useNavigate();
  const createVersion = useCreateVersion(projectId);
  const {
    data: branchesData,
    isLoading: branchesLoading,
    isError: branchesError,
    refetch: refetchBranches,
  } = useProjectBranches(projectId);

  const {
    register,
    handleSubmit,
    reset,
    setValue,
    control,
    formState: { errors, isSubmitting },
  } = useForm<VersionInput>({
    resolver: zodResolver(versionSchema),
  });
  const targetBranch = useWatch({ control, name: "target_commitish" });

  useEffect(() => {
    if (open) {
      reset({
        version_number: "",
        target_commitish: "",
        release_notes: "",
      });
    }
  }, [open, projectId, reset]);

  useEffect(() => {
    if (open && !targetBranch && branchesData?.default_branch) {
      setValue("target_commitish", branchesData.default_branch, {
        shouldDirty: false,
        shouldValidate: true,
      });
    }
  }, [open, branchesData?.default_branch, setValue, targetBranch]);

  const onSubmit = async (data: VersionInput) => {
    try {
      const res = await createVersion.mutateAsync(data);
      toast.success("版本创建成功");
      navigate(`/projects/${projectId}/versions/${res.data.id}`);
      onOpenChange(false);
    } catch {
      toast.error("创建失败，版本号可能已存在");
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg max-h-[90vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>新建版本</DialogTitle>
        </DialogHeader>

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
            <Label htmlFor="target_commitish">目标分支（可选）</Label>
            <Select
              value={targetBranch ?? ""}
              onValueChange={(value) =>
                setValue("target_commitish", value ?? undefined, {
                  shouldDirty: true,
                  shouldValidate: true,
                })
              }
              disabled={branchesLoading || branchesError}
            >
              <SelectTrigger id="target_commitish" className="w-full">
                <SelectValue
                  placeholder={
                    branchesLoading ? "加载分支中..." : "选择目标分支"
                  }
                />
              </SelectTrigger>
              <SelectContent>
                {branchesData?.branches.map((branch) => (
                  <SelectItem key={branch.name} value={branch.name}>
                    {branch.default ? `${branch.name}（默认）` : branch.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            {branchesError && (
              <div className="flex items-center justify-between gap-3 rounded-md border border-destructive/30 px-3 py-2">
                <p className="text-xs text-destructive">
                  分支加载失败，可稍后在版本详情中补充
                </p>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => void refetchBranches()}
                >
                  重试
                </Button>
              </div>
            )}
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
            <Button type="submit" disabled={isSubmitting || branchesLoading}>
              {isSubmitting
                ? "创建中..."
                : branchesLoading
                  ? "加载分支中..."
                  : "创建版本"}
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
      </DialogContent>
    </Dialog>
  );
}
