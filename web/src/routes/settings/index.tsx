import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod/v4";
import { Header } from "@/components/layout/header";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Separator } from "@/components/ui/separator";
import { useAuthStore } from "@/lib/store/auth-store";
import { authApi } from "@/lib/api/auth";
import { toast } from "sonner";
import { useEffect } from "react";

const profileSchema = z.object({
  username: z.string().min(2, "用户名至少 2 个字符"),
  email: z.email("请输入有效的邮箱"),
});

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

type ProfileInput = z.infer<typeof profileSchema>;
type PasswordInput = z.infer<typeof passwordSchema>;

export default function SettingsPage() {
  const { user, setUser } = useAuthStore();

  const {
    register: registerProfile,
    handleSubmit: handleProfileSubmit,
    reset: resetProfile,
    formState: { errors: profileErrors, isSubmitting: profileSubmitting },
  } = useForm<ProfileInput>({
    resolver: zodResolver(profileSchema),
    defaultValues: {
      username: user?.username || "",
      email: user?.email || "",
    },
  });

  const {
    register: registerPassword,
    handleSubmit: handlePasswordSubmit,
    reset: resetPassword,
    formState: { errors: passwordErrors, isSubmitting: passwordSubmitting },
  } = useForm<PasswordInput>({
    resolver: zodResolver(passwordSchema),
  });

  useEffect(() => {
    resetProfile({
      username: user?.username || "",
      email: user?.email || "",
    });
  }, [resetProfile, user]);

  const onProfileSubmit = async (data: ProfileInput) => {
    try {
      const res = await authApi.updateMe(data);
      setUser(res.data);
      toast.success("个人信息已更新");
    } catch {
      toast.error("更新失败");
    }
  };

  const onPasswordSubmit = async (data: PasswordInput) => {
    try {
      await authApi.updatePassword({
        old_password: data.old_password,
        new_password: data.new_password,
      });
      resetPassword();
      toast.success("密码已修改");
    } catch {
      toast.error("密码修改失败，请检查当前密码");
    }
  };

  return (
    <>
      <Header title="个人设置" />
      <div className="p-4 md:p-6 space-y-6">
        <Card className="max-w-xl">
          <CardHeader>
            <CardTitle>个人信息</CardTitle>
            <CardDescription>修改你的用户名和邮箱</CardDescription>
          </CardHeader>
          <CardContent>
            <form
              onSubmit={handleProfileSubmit(onProfileSubmit)}
              className="space-y-4"
            >
              <div className="space-y-2">
                <Label htmlFor="username">用户名</Label>
                <Input id="username" {...registerProfile("username")} />
                {profileErrors.username && (
                  <p className="text-xs text-destructive">
                    {profileErrors.username.message}
                  </p>
                )}
              </div>
              <div className="space-y-2">
                <Label htmlFor="email">邮箱</Label>
                <Input id="email" type="email" {...registerProfile("email")} />
                {profileErrors.email && (
                  <p className="text-xs text-destructive">
                    {profileErrors.email.message}
                  </p>
                )}
              </div>
              <Button type="submit" disabled={profileSubmitting}>
                {profileSubmitting ? "保存中..." : "保存"}
              </Button>
            </form>
          </CardContent>
        </Card>

        <Separator className="max-w-xl" />

        <Card className="max-w-xl">
          <CardHeader>
            <CardTitle>修改密码</CardTitle>
            <CardDescription>设置新的登录密码</CardDescription>
          </CardHeader>
          <CardContent>
            <form
              onSubmit={handlePasswordSubmit(onPasswordSubmit)}
              className="space-y-4"
            >
              <div className="space-y-2">
                <Label htmlFor="old_password">当前密码</Label>
                <Input
                  id="old_password"
                  type="password"
                  {...registerPassword("old_password")}
                />
                {passwordErrors.old_password && (
                  <p className="text-xs text-destructive">
                    {passwordErrors.old_password.message}
                  </p>
                )}
              </div>
              <div className="space-y-2">
                <Label htmlFor="new_password">新密码</Label>
                <Input
                  id="new_password"
                  type="password"
                  {...registerPassword("new_password")}
                />
                {passwordErrors.new_password && (
                  <p className="text-xs text-destructive">
                    {passwordErrors.new_password.message}
                  </p>
                )}
              </div>
              <div className="space-y-2">
                <Label htmlFor="confirm_password">确认新密码</Label>
                <Input
                  id="confirm_password"
                  type="password"
                  {...registerPassword("confirm_password")}
                />
                {passwordErrors.confirm_password && (
                  <p className="text-xs text-destructive">
                    {passwordErrors.confirm_password.message}
                  </p>
                )}
              </div>
              <Button type="submit" disabled={passwordSubmitting}>
                {passwordSubmitting ? "修改中..." : "修改密码"}
              </Button>
            </form>
          </CardContent>
        </Card>
      </div>
    </>
  );
}
