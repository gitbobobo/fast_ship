import { useMemo, useState } from "react";
import { Navigate, useLocation, useSearchParams } from "react-router";
import { AlertTriangle, Loader2, ShieldCheck } from "lucide-react";

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Checkbox } from "@/components/ui/checkbox";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { useAuthStore } from "@/lib/store/auth-store";
import { useOAuthAuthorizePreview } from "@/lib/hooks/use-oauth";
import { oauthApi } from "@/lib/api/oauth";
import { errorText, loginRedirectPath } from "@/lib/api/client";
import { authApi } from "@/lib/api/auth";

/** 缺一不可的 PKCE 参数；state 与 scope 不参与预取，只透传。 */
const REQUIRED_PARAMS = [
  "client_id",
  "redirect_uri",
  "code_challenge",
  "code_challenge_method",
] as const;

function missingParams(search: URLSearchParams): string[] {
  return REQUIRED_PARAMS.filter((key) => !search.get(key));
}

export default function OAuthAuthorizePage() {
  const token = useAuthStore((s) => s.token);
  const location = useLocation();
  const [searchParams] = useSearchParams();

  // 未登录：带完整 query 去登录页，登录后由 ?next 机制回到本页，
  // PKCE 的 code_challenge/state 是一次性的，丢了整个授权就得重来。
  if (!token) {
    const next = `${location.pathname}${location.search}`;
    return (
      <Navigate to={`/login?next=${encodeURIComponent(next)}`} replace />
    );
  }

  return <AuthorizeConsent searchParams={searchParams} />;
}

function AuthorizeConsent({ searchParams }: { searchParams: URLSearchParams }) {
  const logout = useAuthStore((s) => s.logout);
  const missing = useMemo(() => missingParams(searchParams), [searchParams]);
  const previewParams = missing.length
    ? null
    : {
        client_id: searchParams.get("client_id")!,
        redirect_uri: searchParams.get("redirect_uri")!,
        code_challenge: searchParams.get("code_challenge")!,
        code_challenge_method: searchParams.get(
          "code_challenge_method",
        )! as "S256",
      };

  const preview = useOAuthAuthorizePreview(previewParams);

  // 项目多选与读写档：至少选 1 个项目才允许提交。
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());
  const [write, setWrite] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState<string | null>(null);

  if (missing.length) {
    return (
      <ConsentShell>
        <ErrorPanel
          title="授权链接无效"
          message={`缺少必要参数：${missing.join("、")}。请从应用发起授权流程重新进入。`}
        />
      </ConsentShell>
    );
  }

  if (preview.isLoading) {
    return (
      <ConsentShell>
        <div className="flex items-center justify-center gap-2 py-12 text-sm text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" />
          正在加载授权信息...
        </div>
      </ConsentShell>
    );
  }

  if (preview.isError || !preview.data) {
    return (
      <ConsentShell>
        <ErrorPanel
          title="无法发起授权"
          message={errorText(
            preview.error,
            "该应用不存在或授权参数非法，请确认链接来自可信来源后重试。",
          )}
        />
      </ConsentShell>
    );
  }

  const { client_name, projects, username, email } = preview.data;

  const toggleProject = (id: string, checked: boolean) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (checked) next.add(id);
      else next.delete(id);
      return next;
    });
  };

  const onSubmit = async () => {
    setSubmitError(null);
    setSubmitting(true);
    try {
      const res = await oauthApi.authorize({
        client_id: previewParams!.client_id,
        redirect_uri: previewParams!.redirect_uri,
        code_challenge: previewParams!.code_challenge,
        code_challenge_method: "S256",
        state: searchParams.get("state") ?? undefined,
        project_ids: [...selectedIds],
        write,
      });
      // redirect 由服务端拼好（含 code 与 state），整页跳转出应用。
      window.location.href = res.data.redirect;
    } catch (err) {
      setSubmitError(errorText(err, "授权失败，请重试"));
      setSubmitting(false);
    }
  };

  // 拒绝授权：以 error=access_denied 回调客户端（RFC 6749 §4.1.2.1），
  // redirect_uri 已经过服务端注册校验，客户端会收到明确的中止信号而不是干等。
  const onDeny = () => {
    const u = new URL(previewParams!.redirect_uri);
    u.searchParams.set("error", "access_denied");
    const state = searchParams.get("state");
    if (state) u.searchParams.set("state", state);
    window.location.href = u.toString();
  };

  // 切换账号：与 UserNav 同语义——先吊销服务端会话（失败也继续），再清
  // 本地凭证进登录页，回跳参数保留本次授权请求。
  const onSwitchAccount = async () => {
    const refreshToken = useAuthStore.getState().refreshToken;
    try {
      await authApi.logout(refreshToken ?? undefined);
    } finally {
      logout();
      window.location.href = loginRedirectPath();
    }
  };

  return (
    <ConsentShell>
      <Card>
        <CardHeader>
          <CardTitle className="text-xl">授权访问</CardTitle>
        </CardHeader>
        <CardContent className="space-y-5">
          <p className="text-sm text-muted-foreground">
            <span className="font-medium text-foreground">{client_name}</span>
            {" "}请求访问你的 Fast Ship 账号。该应用将获得所选项目的
            {write ? "读写" : "只读"}权限。
          </p>
          <div className="flex items-center justify-between rounded-md bg-muted/50 px-3 py-2 text-sm">
            <span className="text-muted-foreground">
              当前登录：
              <span className="font-medium text-foreground">{username}</span>
              {email && <span className="text-muted-foreground">（{email}）</span>}
            </span>
            <Button
              variant="link"
              size="sm"
              className="h-auto p-0"
              onClick={onSwitchAccount}
            >
              切换账号
            </Button>
          </div>

          <div className="space-y-2">
            <Label>选择要授权的项目</Label>
            {projects.length === 0 ? (
              <p className="rounded-md border bg-muted/40 p-3 text-sm text-muted-foreground">
                你还没有任何项目，无法授权。
              </p>
            ) : (
              <div className="space-y-2">
                {projects.map((project) => (
                  <label
                    key={project.id}
                    className="flex cursor-pointer items-center gap-2 rounded-md border p-2.5 text-sm"
                  >
                    <Checkbox
                      checked={selectedIds.has(project.id)}
                      onCheckedChange={(checked) =>
                        toggleProject(project.id, checked === true)
                      }
                    />
                    {project.name}
                  </label>
                ))}
              </div>
            )}
            {/* 常驻中性引导：占位固定不跳动；红色报错留给真正的错误 */}
            {projects.length > 0 && (
              <p className="text-xs text-muted-foreground">
                至少选择 1 个项目后才能授权
              </p>
            )}
          </div>

          <div className="space-y-2">
            <Label>访问权限</Label>
            <RadioGroup
              value={write ? "write" : "read"}
              onValueChange={(value) => setWrite(value === "write")}
              disabled={projects.length === 0}
            >
              <RadioGroupItem value="read">只读（查看项目与问题）</RadioGroupItem>
              <RadioGroupItem value="write">读写（可创建和更新问题）</RadioGroupItem>
            </RadioGroup>
          </div>

          {submitError && (
            <p className="text-sm text-destructive">{submitError}</p>
          )}

          <div className="flex gap-3">
            <Button
              variant="outline"
              className="flex-1"
              disabled={submitting}
              onClick={onDeny}
            >
              取消
            </Button>
            <Button
              className="flex-1"
              disabled={selectedIds.size === 0 || submitting}
              onClick={onSubmit}
            >
              {submitting ? "授权中..." : "同意授权"}
            </Button>
          </div>
        </CardContent>
      </Card>
    </ConsentShell>
  );
}

function ConsentShell({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex min-h-dvh flex-col items-center justify-center bg-muted/40 px-4 py-8">
      <div className="mb-8 flex items-center gap-2">
        <ShieldCheck className="h-6 w-6 text-primary" />
        <span className="text-2xl font-bold tracking-tight">Fast Ship</span>
      </div>
      <div className="w-full max-w-md">{children}</div>
    </div>
  );
}

function ErrorPanel({ title, message }: { title: string; message: string }) {
  return (
    <Card>
      <CardContent className="flex flex-col items-center gap-3 py-10 text-center">
        <AlertTriangle className="h-8 w-8 text-destructive" />
        <p className="text-base font-medium">{title}</p>
        <p className="text-sm text-muted-foreground">{message}</p>
      </CardContent>
    </Card>
  );
}
