import { Link } from "react-router";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { loginSchema, type LoginInput } from "@/lib/utils/validators";
import { authApi } from "@/lib/api/auth";
import { useAuthStore } from "@/lib/store/auth-store";
import { useNavigate } from "react-router";
import { useState } from "react";

export default function LoginPage() {
  const navigate = useNavigate();
  const setAuth = useAuthStore((s) => s.setAuth);
  const [error, setError] = useState("");

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
      navigate("/dashboard", { replace: true });
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
