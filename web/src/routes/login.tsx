import { Link, useNavigate, useSearchParams } from "react-router";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { loginSchema, type LoginInput } from "@/lib/utils/validators";
import { authApi } from "@/lib/api/auth";
import { useAuthStore } from "@/lib/store/auth-store";
import { DEFAULT_POST_LOGIN_PATH, safeNextPath } from "@/lib/utils/safe-next";
import { useState } from "react";

export default function LoginPage() {
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const setAuth = useAuthStore((s) => s.setAuth);
  const [error, setError] = useState("");
  // AppLayout 把未登录的深链接带过来（如 GitHub App Manifest 回调）：登录后回去，
  // 否则一次性 code/state 会随登录跳转一起丢掉。
  const next = safeNextPath(searchParams.get("next"));

  const {
    register,
    handleSubmit,
    formState: { errors, isSubmitting },
  } = useForm<LoginInput>({ resolver: zodResolver(loginSchema) });

  const onSubmit = async (data: LoginInput) => {
    setError("");
    try {
      const res = await authApi.login(data);
      setAuth(res.data.token, res.data.refresh_token, res.data.user);
      // 时区探测只在注册时做一次（见 register.tsx）：登录路径不写，避免把用户
      // 在设置页选的「跟随部署配置」（存空串）在下次登录时又覆盖回浏览器时区。
      navigate(next ?? DEFAULT_POST_LOGIN_PATH, { replace: true });
    } catch {
      setError("用户名/邮箱或密码错误");
    }
  };

  return (
    <Card>
      <CardHeader className="text-center">
        <CardTitle className="text-xl">登录</CardTitle>
      </CardHeader>
      <CardContent>
        <form onSubmit={handleSubmit(onSubmit)} className="space-y-4">
          {error && (
            <p className="text-sm text-destructive text-center">{error}</p>
          )}
          <div className="space-y-2">
            <Label htmlFor="login">用户名或邮箱</Label>
            <Input
              id="login"
              autoComplete="username"
              autoCorrect="off"
              autoCapitalize="none"
              spellCheck={false}
              {...register("login")}
            />
            {errors.login && (
              <p className="text-xs text-destructive">{errors.login.message}</p>
            )}
          </div>
          <div className="space-y-2">
            <Label htmlFor="password">密码</Label>
            <Input
              id="password"
              type="password"
              autoComplete="current-password"
              autoCorrect="off"
              autoCapitalize="none"
              spellCheck={false}
              {...register("password")}
            />
            {errors.password && (
              <p className="text-xs text-destructive">
                {errors.password.message}
              </p>
            )}
          </div>
          <Button type="submit" className="w-full" disabled={isSubmitting}>
            {isSubmitting ? "登录中..." : "登录"}
          </Button>
        </form>
        <p className="mt-4 text-center text-sm text-muted-foreground">
          还没有账号？
          <Link to="/register" className="ml-1 text-primary hover:underline">
            注册
          </Link>
        </p>
      </CardContent>
    </Card>
  );
}
