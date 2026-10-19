import { useEffect, useRef, useState } from "react";
import { Link, useSearchParams } from "react-router";
import { Loader2 } from "lucide-react";

import { Button } from "@/components/ui/button";
import { SettingsPageShell } from "@/routes/settings/layout";
import { deploymentApi } from "@/lib/api/deployment";

function errorText(error: unknown, fallback: string) {
  return error instanceof Error && error.message ? error.message : fallback;
}

// GitHub App Manifest 创建后的回跳：用 code+state 换部署级 App 凭证，
// 成功后直接跳 App 安装页。
export default function GitHubCallbackPage() {
  const [searchParams] = useSearchParams();
  const [error, setError] = useState<string | null>(null);
  // 请求 Promise 存 ref：code 是一次性的，StrictMode 的 effect→cleanup→effect
  // 只允许发一次请求（??= 保证），但两次挂载都要挂上结果处理器——过期的那次
  // 由 cancelled 丢弃，活着的那个负责跳转/报错，不会出现「响应被吞」。
  const promiseRef = useRef<Promise<GitHubAppDeployment> | null>(null);

  useEffect(() => {
    let cancelled = false;

    const code = searchParams.get("code");
    const state = searchParams.get("state");
    if (!code || !state) {
      setError("GitHub 回调缺少必要参数，请重新创建 App。");
      return;
    }

    promiseRef.current ??= deploymentApi
      .completeManifest({ code, state })
      .then((res) => res.data);

    promiseRef.current.then(
      (data) => {
        if (cancelled) return;
        window.location.href = data.install_url || "/settings/integrations";
      },
      (err: unknown) => {
        if (cancelled) return;
        setError(errorText(err, "创建 GitHub App 失败"));
      },
    );

    return () => {
      cancelled = true;
    };
  }, [searchParams]);

  return (
    <SettingsPageShell>
      <div className="space-y-6">
        <div>
          <h2 className="text-lg font-medium">集成</h2>
          <p className="text-sm text-muted-foreground">
            部署级 GitHub App，用于 PR 评审与 webhook 事件。
          </p>
        </div>

        <div className="rounded-xl border">
          {error ? (
            <div className="space-y-4 p-6 text-center">
              <p className="font-medium">GitHub App 创建未完成</p>
              <p role="alert" className="text-sm text-muted-foreground">
                {error}
              </p>
              <Button
                size="sm"
                render={<Link to="/settings/integrations" />}
              >
                重新创建
              </Button>
            </div>
          ) : (
            <div className="flex items-center justify-center gap-2 p-12 text-sm text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" />
              正在完成 GitHub App 创建...
            </div>
          )}
        </div>
      </div>
    </SettingsPageShell>
  );
}
