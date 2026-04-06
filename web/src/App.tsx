import { BrowserRouter, Routes, Route, Navigate } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Toaster } from "@/components/ui/sonner";
import { TooltipProvider } from "@/components/ui/tooltip";

import AppLayout from "@/routes/_layout";
import AuthLayout from "@/routes/_auth-layout";
import LoginPage from "@/routes/login";
import RegisterPage from "@/routes/register";
import ProjectsPage from "@/routes/projects/index";
import NewProjectPage from "@/routes/projects/new";
import ProjectDetailPage from "@/routes/projects/$id/index";
import EditProjectPage from "@/routes/projects/$id/edit";
import NewVersionPage from "@/routes/projects/$id/versions/new";
import VersionDetailPage from "@/routes/projects/$id/versions/$vid";
import SettingsPage from "@/routes/settings/index";
import ApiKeysPage from "@/routes/settings/api-keys";

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
              <Route path="/login" element={<LoginPage />} />
              <Route path="/register" element={<RegisterPage />} />
            </Route>

            {/* 已登录路由 */}
            <Route element={<AppLayout />}>
              <Route path="/projects" element={<ProjectsPage />} />
              <Route path="/projects/new" element={<NewProjectPage />} />
              <Route path="/projects/:id" element={<ProjectDetailPage />} />
              <Route path="/projects/:id/edit" element={<EditProjectPage />} />
              <Route
                path="/projects/:id/versions/new"
                element={<NewVersionPage />}
              />
              <Route
                path="/projects/:id/versions/:vid"
                element={<VersionDetailPage />}
              />
              <Route path="/settings" element={<SettingsPage />} />
              <Route path="/settings/api-keys" element={<ApiKeysPage />} />
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
