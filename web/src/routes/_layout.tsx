import { useEffect, useState } from "react";
import { Navigate, Outlet, useLocation } from "react-router";
import { Sidebar } from "@/components/layout/sidebar";
import { authApi } from "@/lib/api/auth";
import { usePersistedScroll } from "@/lib/hooks/use-persisted-scroll";
import { getLocationScrollKey } from "@/lib/scroll-positions";
import { useAuthStore } from "@/lib/store/auth-store";

export default function AppLayout() {
  const location = useLocation();
  const { token, user, setUser, logout } = useAuthStore();
  const shouldBootstrapUser = Boolean(token && !user);
  const [hasBootstrappedUser, setHasBootstrappedUser] = useState(
    () => !shouldBootstrapUser,
  );
  const isBootstrapping = shouldBootstrapUser && !hasBootstrappedUser;
  const mainRef = usePersistedScroll<HTMLElement>(
    getLocationScrollKey(location.pathname, location.search),
    { ready: Boolean(token) && !isBootstrapping },
  );

  useEffect(() => {
    let cancelled = false;

    if (!shouldBootstrapUser) {
      return;
    }

    authApi
      .me()
      .then((res) => {
        if (!cancelled) {
          setUser(res.data);
        }
      })
      .catch(() => {
        if (!cancelled) {
          logout();
        }
      })
      .finally(() => {
        if (!cancelled) {
          setHasBootstrappedUser(true);
        }
      });

    return () => {
      cancelled = true;
    };
  }, [logout, setUser, shouldBootstrapUser]);

  if (!token) {
    return <Navigate to="/login" replace />;
  }

  if (isBootstrapping) {
    return <div className="flex min-h-dvh items-center justify-center text-sm text-muted-foreground">加载用户信息中...</div>;
  }

  return (
    <div className="flex h-dvh overflow-hidden bg-background">
      <Sidebar />
      <main ref={mainRef} className="flex flex-1 flex-col overflow-y-auto">
        <Outlet />
      </main>
    </div>
  );
}
