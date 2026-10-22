import { useEffect, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { Loader2 } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { registerSchema, type RegisterInput } from "@/lib/utils/validators";
import { authApi } from "@/lib/api/auth";
import { ApiRequestError } from "@/lib/api/client";
import { useAuthStore } from "@/lib/store/auth-store";
import { formatDate } from "@/lib/utils/format";
import { syncBrowserTimezone } from "@/lib/utils/timezone";

type InviteCheck =
  | { state: "none" }
  | { state: "checking" }
  | { state: "valid"; expires_at: string }
  | { state: "invalid"; message: string };

function RegisterForm({
  inviteToken,
  title,
  hint,
}: {
  inviteToken: string;
  title: string;
  hint?: string;
}) {
  const navigate = useNavigate();
  const setAuth = useAuthStore((s) => s.setAuth);
  const [error, setError] = useState("");

  const {
    register,
    handleSubmit,
    formState: { errors, isSubmitting },
  } = useForm<RegisterInput>({ resolver: zodResolver(registerSchema) });

  const onSubmit = async (data: RegisterInput) => {
    setError("");
    try {
      const res = await authApi.register({
        ...data,
        invite_token: inviteToken || undefined,
      });
      setAuth(res.data.token, res.data.refresh_token, res.data.user);
      // 用户还没声明时区时按浏览器时区写一次（helper 内部静默失败，不阻断跳转）；
      // 只有真正拿到新 user 才回写 store。
      const syncedUser = await syncBrowserTimezone(res.data.user);
      if (syncedUser !== res.data.user) {
        useAuthStore.getState().setUser(syncedUser);
      }
      navigate("/dashboard", { replace: true });
    } catch (err) {
      // 失败文案用服务端 message（40306 注册关闭、40432 邀请失效、
      // 40900/40901 用户名/邮箱重复），非服务端错误统一兜底。
      setError(
        err instanceof ApiRequestError ? err.message : "注册失败，请稍后重试",
      );
    }
  };

  return (
    <Card>
      <CardHeader className="text-center">
        <CardTitle className="text-xl">{title}</CardTitle>
      </CardHeader>
      <CardContent>
        <form onSubmit={handleSubmit(onSubmit)} className="space-y-4">
          {hint && (
            <p className="text-sm text-muted-foreground text-center">{hint}</p>
          )}
          {error && (
            <p className="text-sm text-destructive text-center">{error}</p>
          )}
          <div className="space-y-2">
            <Label htmlFor="username">用户名</Label>
            <Input
              id="username"
              autoComplete="username"
              autoCorrect="off"
              autoCapitalize="none"
              spellCheck={false}
              {...register("username")}
            />
            {errors.username && (
              <p className="text-xs text-destructive">
                {errors.username.message}
              </p>
            )}
          </div>
          <div className="space-y-2">
            <Label htmlFor="email">邮箱</Label>
            <Input
              id="email"
              type="email"
              autoComplete="email"
              autoCorrect="off"
              autoCapitalize="none"
              spellCheck={false}
              {...register("email")}
            />
            {errors.email && (
              <p className="text-xs text-destructive">
                {errors.email.message}
              </p>
            )}
          </div>
          <div className="space-y-2">
            <Label htmlFor="password">密码</Label>
            <Input
              id="password"
              type="password"
              autoComplete="new-password"
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
            <p className="text-xs text-muted-foreground">
              至少 8 位，需包含大小写字母和数字
            </p>
          </div>
          <Button type="submit" className="w-full" disabled={isSubmitting}>
            {isSubmitting ? "注册中..." : "注册"}
          </Button>
        </form>
        <p className="mt-4 text-center text-sm text-muted-foreground">
          已有账号？
          <Link to="/login" className="ml-1 text-primary hover:underline">
            登录
          </Link>
        </p>
      </CardContent>
    </Card>
  );
}

export default function RegisterPage() {
  const [searchParams] = useSearchParams();
  const inviteToken = searchParams.get("invite")?.trim() ?? "";
  const [invite, setInvite] = useState<InviteCheck>(
    inviteToken ? { state: "checking" } : { state: "none" },
  );

  // 每次 effect 都发一次校验请求，用本次运行的 cancelled 丢弃过期结果；
  // token 变化时重新校验。StrictMode 下开发环境会发两次（可接受），生产只发一次。
  useEffect(() => {
    if (!inviteToken) return;
    let cancelled = false;
    setInvite({ state: "checking" });
    authApi
      .validateInvite(inviteToken)
      .then((res) => {
        if (cancelled) return;
        setInvite({ state: "valid", expires_at: res.data.expires_at });
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        // 文案用服务端 message（40432 无效、40932 已被使用），非服务端错误
        // 统一兜底，不细分具体原因。
        setInvite({
          state: "invalid",
          message:
            err instanceof ApiRequestError && err.message
              ? err.message
              : "邀请链接无效或已过期",
        });
      });
    return () => {
      cancelled = true;
    };
  }, [inviteToken]);

  if (invite.state === "invalid") {
    return (
      <Card>
        <CardHeader className="text-center">
          <CardTitle className="text-xl">注册</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4 text-center">
          <p className="text-sm text-destructive">{invite.message}</p>
          <p className="text-sm text-muted-foreground">
            请联系管理员重新获取邀请链接，或
            <Link to="/login" className="ml-1 text-primary hover:underline">
              去登录
            </Link>
          </p>
        </CardContent>
      </Card>
    );
  }

  if (invite.state === "checking") {
    return (
      <Card>
        <CardHeader className="text-center">
          <CardTitle className="text-xl">注册</CardTitle>
        </CardHeader>
        <CardContent>
          <p className="flex items-center justify-center gap-2 py-8 text-sm text-muted-foreground">
            <Loader2 className="h-4 w-4 animate-spin" />
            正在验证邀请链接...
          </p>
        </CardContent>
      </Card>
    );
  }

  if (invite.state === "valid") {
    return (
      <RegisterForm
        inviteToken={inviteToken}
        title="接受邀请加入"
        hint={`邀请有效至 ${formatDate(invite.expires_at)}`}
      />
    );
  }

  // 无 token：公开注册已关闭，但首个账号（将成为管理员）仍可注册——
  // 是否有用户由后端判定，40306 文案原样展示。
  return (
    <RegisterForm
      inviteToken=""
      title="注册"
      hint="公开注册已关闭：请联系管理员获取邀请链接"
    />
  );
}
