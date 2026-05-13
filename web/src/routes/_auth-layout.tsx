import { Navigate, Outlet } from "react-router";
import { Rocket } from "lucide-react";
import { useAuthStore } from "@/lib/store/auth-store";

export default function AuthLayout() {
  const token = useAuthStore((s) => s.token);

  if (token) {
    return <Navigate to="/dashboard" replace />;
  }

  return (
    <div className="flex min-h-dvh flex-col items-center justify-center bg-muted/40 px-4 py-8">
      <div className="mb-8 flex items-center gap-2">
        <Rocket className="h-7 w-7 text-primary" />
        <span className="text-2xl font-bold tracking-tight">Fast Ship</span>
      </div>
      <div className="w-full max-w-sm">
        <Outlet />
      </div>
    </div>
  );
}
