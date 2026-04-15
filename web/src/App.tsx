import { Suspense, lazy, type ReactNode } from "react";
import { BrowserRouter, Routes, Route, Navigate } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Toaster } from "@/components/ui/sonner";
import { TooltipProvider } from "@/components/ui/tooltip";

import AppLayout from "@/routes/_layout";
import AuthLayout from "@/routes/_auth-layout";
import SettingsLayout from "@/routes/settings/layout";

const LoginPage = lazy(() => import("@/routes/login"));
const RegisterPage = lazy(() => import("@/routes/register"));
const ProjectsPage = lazy(() => import("@/routes/projects/index"));
const NewProjectPage = lazy(() => import("@/routes/projects/new"));
const ProjectDetailPage = lazy(() => import("@/routes/projects/$id/index"));
const EditProjectPage = lazy(() => import("@/routes/projects/$id/edit"));
const NewVersionPage = lazy(() => import("@/routes/projects/$id/versions/new"));
const VersionDetailPage = lazy(
  () => import("@/routes/projects/$id/versions/$vid"),
);
const SettingsGeneralPage = lazy(() => import("@/routes/settings/general"));
const SettingsProfilePage = lazy(() => import("@/routes/settings/profile"));
const SettingsPasswordPage = lazy(() => import("@/routes/settings/password"));
const ApiKeysPage = lazy(() => import("@/routes/settings/api-keys"));

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: 1,
      refetchOnWindowFocus: false,
    },
  },
});

export default function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <TooltipProvider>
        <BrowserRouter>
          <Routes>
            {/* 未登录路由 */}
            <Route element={<AuthLayout />}>
              <Route path="/login" element={<LazyPage render={<LoginPage />} />} />
              <Route
                path="/register"
                element={<LazyPage render={<RegisterPage />} />}
              />
            </Route>

            {/* 已登录路由 */}
            <Route element={<AppLayout />}>
              <Route
                path="/projects"
                element={<LazyPage render={<ProjectsPage />} />}
              />
              <Route
                path="/projects/new"
                element={<LazyPage render={<NewProjectPage />} />}
              />
              <Route
                path="/projects/:id"
                element={<LazyPage render={<ProjectDetailPage />} />}
              />
              <Route
                path="/projects/:id/edit"
                element={<LazyPage render={<EditProjectPage />} />}
              />
              <Route
                path="/projects/:id/versions/new"
                element={<LazyPage render={<NewVersionPage />} />}
              />
              <Route
                path="/projects/:id/versions/:vid"
                element={<LazyPage render={<VersionDetailPage />} />}
              />
              {/* 设置页面嵌套路由 */}
              <Route path="/settings" element={<LazyPage render={<SettingsLayout />} />}>
                <Route
                  path="general"
                  element={<LazyPage render={<SettingsGeneralPage />} />}
                />
                <Route
                  path="profile"
                  element={<LazyPage render={<SettingsProfilePage />} />}
                />
                <Route
                  path="password"
                  element={<LazyPage render={<SettingsPasswordPage />} />}
                />
                <Route
                  path="api-keys"
                  element={<LazyPage render={<ApiKeysPage />} />}
                />
              </Route>
            </Route>

            {/* 默认重定向 */}
            <Route path="*" element={<Navigate to="/projects" replace />} />
          </Routes>
        </BrowserRouter>
        <Toaster position="top-center" richColors />
      </TooltipProvider>
    </QueryClientProvider>
  );
}

function LazyPage({ render }: { render: ReactNode }) {
  return (
    <Suspense
      fallback={
        <div className="flex min-h-[40vh] items-center justify-center text-sm text-muted-foreground">
          页面加载中...
        </div>
      }
    >
      {render}
    </Suspense>
  );
}
