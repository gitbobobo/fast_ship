import { Suspense, lazy, useEffect, type ReactNode } from "react";
import { BrowserRouter, Routes, Route, Navigate, useParams } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Toaster } from "@/components/ui/sonner";
import { TooltipProvider } from "@/components/ui/tooltip";
import { watchGithubTokensIdentity } from "@/lib/hooks/use-github-tokens";
import {
  useThreadStream,
  watchThreadsIdentity,
} from "@/lib/hooks/use-threads";
import { watchBotsIdentity } from "@/lib/hooks/use-bots";
import { watchAIProvidersIdentity } from "@/lib/hooks/use-ai-providers";
import { watchQuotaIdentity } from "@/lib/hooks/use-quota";
import { watchDeploymentIdentity } from "@/lib/hooks/use-deployment";
import { watchForgeIntegrationsIdentity } from "@/lib/hooks/use-forge-integrations";
import { watchMembersIdentity } from "@/lib/hooks/use-members";
import { watchReviewsIdentity } from "@/lib/hooks/use-reviews";

import AppLayout from "@/routes/_layout";
import AuthLayout from "@/routes/_auth-layout";
import SettingsLayout from "@/routes/settings/layout";

const LoginPage = lazy(() => import("@/routes/login"));
const RegisterPage = lazy(() => import("@/routes/register"));
const DashboardPage = lazy(() => import("@/routes/dashboard/index"));
const ProjectsPage = lazy(() => import("@/routes/projects/index"));
const VersionsPage = lazy(() => import("@/routes/versions/index"));
const ScreenshotsPage = lazy(() => import("@/routes/screenshots/index"));
const IssuesPage = lazy(() => import("@/routes/issues/index"));
const LogsPage = lazy(() => import("@/routes/logs/index"));
const LogRunDetailPage = lazy(() => import("@/routes/logs/$runId"));
const DocumentsPage = lazy(() => import("@/routes/documents/index"));
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
const AIProvidersPage = lazy(() => import("@/routes/settings/ai-providers"));
const SettingsIssuePromptsPage = lazy(
  () => import("@/routes/settings/issue-prompts"),
);
const ApiKeysPage = lazy(() => import("@/routes/settings/api-keys"));
const GithubTokensPage = lazy(
  () => import("@/routes/settings/github-tokens"),
);
const IntegrationsPage = lazy(() => import("@/routes/settings/integrations"));
const SettingsDeploymentPage = lazy(
  () => import("@/routes/settings/deployment"),
);
const SettingsMembersPage = lazy(() => import("@/routes/settings/members"));
const GitHubCallbackPage = lazy(
  () => import("@/routes/settings/github-callback"),
);
const EnvironmentsPage = lazy(() => import("@/routes/settings/environments"));
const ReviewsPage = lazy(() => import("@/routes/reviews/index"));
const ReviewConfigPage = lazy(() => import("@/routes/reviews/config"));
const ReviewProjectPage = lazy(() => import("@/routes/reviews/$projectId"));
const NotificationsPage = lazy(() => import("@/routes/notifications/index"));
const QuotaPage = lazy(() => import("@/routes/quota/index"));
const BotsPage = lazy(() => import("@/routes/bots/index"));
const BotDetailPage = lazy(() => import("@/routes/bots/$botId"));
const ThreadsLayout = lazy(() => import("@/routes/threads/layout"));
const ThreadsPage = lazy(() => import("@/routes/threads/index"));
const NewThreadPage = lazy(() => import("@/routes/threads/new"));
const ThreadDetailPage = lazy(
  () => import("@/routes/threads/$environmentId/$threadId"),
);

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: 1,
      refetchOnWindowFocus: false,
    },
  },
});

export default function App() {
  // 线程壳 SSE：登录期间由服务端推送失效线程查询，取代轮询。
  useThreadStream(queryClient);

  useEffect(() => {
    const stopThreads = watchThreadsIdentity(queryClient);
    const stopGithubTokens = watchGithubTokensIdentity(queryClient);
    const stopBots = watchBotsIdentity(queryClient);
    const stopAIProviders = watchAIProvidersIdentity(queryClient);
    const stopQuota = watchQuotaIdentity(queryClient);
    const stopDeployment = watchDeploymentIdentity(queryClient);
    const stopForgeIntegrations = watchForgeIntegrationsIdentity(queryClient);
    const stopMembers = watchMembersIdentity(queryClient);
    const stopReviews = watchReviewsIdentity(queryClient);
    return () => {
      stopThreads();
      stopGithubTokens();
      stopBots();
      stopAIProviders();
      stopQuota();
      stopDeployment();
      stopForgeIntegrations();
      stopMembers();
      stopReviews();
    };
  }, []);

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
                path="/screenshots"
                element={<LazyPage render={<ScreenshotsPage />} />}
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
                path="/logs/:runId"
                element={<LazyPage render={<LogRunDetailPage />} />}
              />
              <Route
                path="/documents"
                element={<LazyPage render={<DocumentsPage />} />}
              />
              {/* 新建页保持整页，不套 /threads 的主从两栏 */}
              <Route
                path="/threads/new"
                element={<LazyPage render={<NewThreadPage />} />}
              />
              <Route
                path="/threads"
                element={<LazyPage render={<ThreadsLayout />} />}
              >
                <Route index element={<LazyPage render={<ThreadsPage />} />} />
                <Route
                  path=":environmentId/:threadId"
                  element={<LazyPage render={<ThreadDetailPage />} />}
                />
              </Route>
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
                  path="ai-providers"
                  element={<LazyPage render={<AIProvidersPage />} />}
                />
                <Route
                  path="issue-prompts"
                  element={<LazyPage render={<SettingsIssuePromptsPage />} />}
                />
                <Route
                  path="api-keys"
                  element={<LazyPage render={<ApiKeysPage />} />}
                />
                <Route
                  path="github-tokens"
                  element={<LazyPage render={<GithubTokensPage />} />}
                />
                <Route
                  path="integrations"
                  element={<LazyPage render={<IntegrationsPage />} />}
                />
                <Route
                  path="integrations/github/callback"
                  element={<LazyPage render={<GitHubCallbackPage />} />}
                />
                <Route
                  path="deployment"
                  element={<LazyPage render={<SettingsDeploymentPage />} />}
                />
                <Route
                  path="members"
                  element={<LazyPage render={<SettingsMembersPage />} />}
                />
                <Route
                  path="environments"
                  element={<LazyPage render={<EnvironmentsPage />} />}
                />
              </Route>
              <Route
                path="/reviews"
                element={<LazyPage render={<ReviewsPage />} />}
              />
              {/* 静态路由必须在 :projectId 之前，避免 "config" 被当成项目 id。 */}
              <Route
                path="/reviews/config"
                element={<LazyPage render={<ReviewConfigPage />} />}
              />
              <Route
                path="/reviews/:projectId"
                element={<LazyPage render={<ReviewProjectPage />} />}
              />
              <Route
                path="/notifications"
                element={<LazyPage render={<NotificationsPage />} />}
              />
              <Route
                path="/quota"
                element={<LazyPage render={<QuotaPage />} />}
              />
              <Route path="/bots" element={<LazyPage render={<BotsPage />} />} />
              <Route
                path="/bots/:botId"
                element={<LazyPage render={<BotDetailPage />} />}
              />
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
