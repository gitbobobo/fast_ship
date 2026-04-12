import { useEffect, useState } from "react";
import { Navigate, Outlet } from "react-router";
import { Sidebar } from "@/components/layout/sidebar";
import { authApi } from "@/lib/api/auth";
import { useAuthStore } from "@/lib/store/auth-store";

export default function AppLayout() {
  const { token, user, setUser, logout } = useAuthStore();
  const shouldBootstrapUser = Boolean(token && !user);
  const [hasBootstrappedUser, setHasBootstrappedUser] = useState(
    () => !shouldBootstrapUser,
  );
  const isBootstrapping = shouldBootstrapUser && !hasBootstrappedUser;

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
      <main className="flex flex-1 flex-col overflow-y-auto">
        <Outlet />
      </main>
    </div>
  );
}
