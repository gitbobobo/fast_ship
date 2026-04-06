import { useEffect, useState } from "react";
import { Navigate, Outlet } from "react-router";
import { Sidebar } from "@/components/layout/sidebar";
import { authApi } from "@/lib/api/auth";
import { useAuthStore } from "@/lib/store/auth-store";

export default function AppLayout() {
  const { token, user, setUser, logout } = useAuthStore();
  const [bootstrapping, setBootstrapping] = useState(false);

  useEffect(() => {
    let cancelled = false;

    if (!token || user) {
      setBootstrapping(false);
      return;
    }

    setBootstrapping(true);
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
          setBootstrapping(false);
        }
      });

    return () => {
      cancelled = true;
    };
  }, [logout, setUser, token, user]);

  if (!token) {
    return <Navigate to="/login" replace />;
  }

  if (bootstrapping) {
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
