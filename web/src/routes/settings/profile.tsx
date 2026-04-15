import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod/v4";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useAuthStore } from "@/lib/store/auth-store";
import { authApi } from "@/lib/api/auth";
import { toast } from "sonner";
import { useEffect } from "react";

const profileSchema = z.object({
  username: z.string().min(2, "用户名至少 2 个字符"),
  email: z.email("请输入有效的邮箱"),
});

type ProfileInput = z.infer<typeof profileSchema>;

export default function ProfilePage() {
  const { user, setUser } = useAuthStore();

  const {
    register,
    handleSubmit,
    reset,
    formState: { errors, isSubmitting },
  } = useForm<ProfileInput>({
    resolver: zodResolver(profileSchema),
    defaultValues: {
      username: user?.username || "",
      email: user?.email || "",
    },
  });

  useEffect(() => {
    reset({
      username: user?.username || "",
      email: user?.email || "",
    });
  }, [reset, user]);

  const onSubmit = async (data: ProfileInput) => {
    try {
      const res = await authApi.updateMe(data);
      setUser(res.data);
      toast.success("个人信息已更新");
    } catch {
      toast.error("更新失败");
    }
  };

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-lg font-medium">个人信息</h2>
        <p className="text-sm text-muted-foreground">修改你的用户名和邮箱</p>
      </div>

      <form onSubmit={handleSubmit(onSubmit)} className="space-y-4 max-w-xl">
        <div className="space-y-2">
          <Label htmlFor="username">用户名</Label>
          <Input id="username" {...register("username")} />
          {errors.username && (
            <p className="text-xs text-destructive">
              {errors.username.message}
            </p>
          )}
        </div>
        <div className="space-y-2">
          <Label htmlFor="email">邮箱</Label>
          <Input id="email" type="email" {...register("email")} />
          {errors.email && (
            <p className="text-xs text-destructive">
              {errors.email.message}
            </p>
          )}
        </div>
        <Button type="submit" disabled={isSubmitting}>
          {isSubmitting ? "保存中..." : "保存"}
        </Button>
      </form>
    </div>
  );
}
