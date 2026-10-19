import { Navigate, Outlet, useSearchParams } from "react-router";
import { FastShipLogo } from "@/components/ui/fast-ship-logo";
import { useAuthStore } from "@/lib/store/auth-store";
import { DEFAULT_POST_LOGIN_PATH, safeNextPath } from "@/lib/utils/safe-next";

export default function AuthLayout() {
  const token = useAuthStore((s) => s.token);
  const [searchParams] = useSearchParams();

  if (token) {
    // 登录页刚 setAuth 时也会走到这里：必须和登录页用同一个目标，否则这个
    // 重定向会抢在 navigate(next) 之前把用户送回 /dashboard（一次性 code/state
    // 就是这么丢的）。
    return (
      <Navigate
        to={safeNextPath(searchParams.get("next")) ?? DEFAULT_POST_LOGIN_PATH}
        replace
      />
    );
  }

  return (
    <div className="flex min-h-dvh flex-col items-center justify-center bg-muted/40 px-4 py-8">
      <div className="mb-8 flex items-center gap-2">
        <FastShipLogo className="h-7 w-7" />
        <span className="text-2xl font-bold tracking-tight">Fast Ship</span>
      </div>
      <div className="w-full max-w-sm">
        <Outlet />
      </div>
    </div>
  );
}
