import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod/v4";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { authApi } from "@/lib/api/auth";
import { toast } from "sonner";

const passwordSchema = z
  .object({
    old_password: z.string().min(1, "请输入当前密码"),
    new_password: z
      .string()
      .min(8, "密码至少 8 位")
      .regex(/[a-z]/, "需包含小写字母")
      .regex(/[A-Z]/, "需包含大写字母")
      .regex(/[0-9]/, "需包含数字"),
    confirm_password: z.string(),
  })
  .refine((data) => data.new_password === data.confirm_password, {
    message: "两次密码不一致",
    path: ["confirm_password"],
  });

type PasswordInput = z.infer<typeof passwordSchema>;

export default function PasswordPage() {
  const {
    register,
    handleSubmit,
    reset,
    formState: { errors, isSubmitting },
  } = useForm<PasswordInput>({
    resolver: zodResolver(passwordSchema),
  });

  const onSubmit = async (data: PasswordInput) => {
    try {
      await authApi.updatePassword({
        old_password: data.old_password,
        new_password: data.new_password,
      });
      reset();
      toast.success("密码已修改");
    } catch {
      toast.error("密码修改失败，请检查当前密码");
    }
  };

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-lg font-medium">修改密码</h2>
        <p className="text-sm text-muted-foreground">设置新的登录密码</p>
      </div>

      <form onSubmit={handleSubmit(onSubmit)} className="space-y-4 max-w-xl">
        <div className="space-y-2">
          <Label htmlFor="old_password">当前密码</Label>
          <Input
            id="old_password"
            type="password"
            {...register("old_password")}
          />
          {errors.old_password && (
            <p className="text-xs text-destructive">
              {errors.old_password.message}
            </p>
          )}
        </div>
        <div className="space-y-2">
          <Label htmlFor="new_password">新密码</Label>
          <Input
            id="new_password"
            type="password"
            {...register("new_password")}
          />
          {errors.new_password && (
            <p className="text-xs text-destructive">
              {errors.new_password.message}
            </p>
          )}
        </div>
        <div className="space-y-2">
          <Label htmlFor="confirm_password">确认新密码</Label>
          <Input
            id="confirm_password"
            type="password"
            {...register("confirm_password")}
          />
          {errors.confirm_password && (
            <p className="text-xs text-destructive">
              {errors.confirm_password.message}
            </p>
          )}
        </div>
        <Button type="submit" disabled={isSubmitting}>
          {isSubmitting ? "修改中..." : "修改密码"}
        </Button>
      </form>
    </div>
  );
}
