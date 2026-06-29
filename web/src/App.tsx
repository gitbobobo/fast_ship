import { Suspense, lazy, type ReactNode } from "react";
import { BrowserRouter, Routes, Route, Navigate, useParams } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Toaster } from "@/components/ui/sonner";
import { TooltipProvider } from "@/components/ui/tooltip";

import AppLayout from "@/routes/_layout";
import AuthLayout from "@/routes/_auth-layout";
import SettingsLayout from "@/routes/settings/layout";

const LoginPage = lazy(() => import("@/routes/login"));
const RegisterPage = lazy(() => import("@/routes/register"));
const DashboardPage = lazy(() => import("@/routes/dashboard/index"));
const ProjectsPage = lazy(() => import("@/routes/projects/index"));
const VersionsPage = lazy(() => import("@/routes/versions/index"));
const IssuesPage = lazy(() => import("@/routes/issues/index"));
const LogsPage = lazy(() => import("@/routes/logs/index"));
const BoardPage = lazy(() => import("@/routes/board/index"));
const VersionDetailPage = lazy(
  () => import("@/routes/projects/$id/versions/$vid"),
);
const NewInternalIssuePage = lazy(() => import("@/routes/projects/$id/issues/new"));
const IssueDetailPage = lazy(() => import("@/routes/projects/$id/issues/$iid"));
const EditInternalIssuePage = lazy(
  () => import("@/routes/projects/$id/issues/$iid/edit"),
);
const SettingsGeneralPage = lazy(() => import("@/routes/settings/general"));
const SettingsProfilePage = lazy(() => import("@/routes/settings/profile"));
const SettingsPasswordPage = lazy(() => import("@/routes/settings/password"));
const SettingsAIPage = lazy(() => import("@/routes/settings/ai"));
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
                path="/dashboard"
                element={<LazyPage render={<DashboardPage />} />}
              />
              <Route
                path="/projects"
                element={<LazyPage render={<ProjectsPage />} />}
              />
              <Route
                path="/versions"
                element={<LazyPage render={<VersionsPage />} />}
              />
              <Route
                path="/issues"
                element={<LazyPage render={<IssuesPage />} />}
              />
              <Route
                path="/logs"
                element={<LazyPage render={<LogsPage />} />}
              />
              <Route
                path="/board"
                element={<LazyPage render={<BoardPage />} />}
              />

              <Route
                path="/projects/:id/versions/new"
                element={<LegacyNewVersionRedirect />}
              />
              <Route
                path="/projects/:id/versions/:vid"
                element={<LazyPage render={<VersionDetailPage />} />}
              />
              <Route
                path="/projects/:id/issues/new"
                element={<LazyPage render={<NewInternalIssuePage />} />}
              />
              <Route
                path="/projects/:id/issues/:iid"
                element={<LazyPage render={<IssueDetailPage />} />}
              />
              <Route
                path="/projects/:id/issues/:iid/edit"
                element={<LazyPage render={<EditInternalIssuePage />} />}
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
                  path="ai"
                  element={<LazyPage render={<SettingsAIPage />} />}
                />
                <Route
                  path="api-keys"
                  element={<LazyPage render={<ApiKeysPage />} />}
                />
              </Route>
            </Route>

            {/* 默认重定向 */}
            <Route path="*" element={<Navigate to="/dashboard" replace />} />
          </Routes>
        </BrowserRouter>
        <Toaster position="top-center" richColors />
      </TooltipProvider>
    </QueryClientProvider>
  );
}

function LegacyNewVersionRedirect() {
  const { id } = useParams();
  const to = id
    ? `/versions?project=${encodeURIComponent(id)}&create=1`
    : "/versions?create=1";
  return <Navigate to={to} replace />;
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
