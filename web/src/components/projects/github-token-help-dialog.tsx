import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { ExternalLinkIcon } from "lucide-react";

function buildFineGrainedTokenUrl(owner?: string) {
  const params = new URLSearchParams({
    name: "Fast Ship",
    description: "Fast Ship release automation",
    contents: "write",
  });

  if (owner) {
    params.set("target_name", owner);
  }

  return `https://github.com/settings/personal-access-tokens/new?${params.toString()}`;
}

interface GitHubTokenHelpDialogProps {
  owner?: string;
  repo?: string;
}

export function GitHubTokenHelpDialog({
  owner,
  repo,
}: GitHubTokenHelpDialogProps) {
  const normalizedOwner = owner?.trim();
  const normalizedRepo = repo?.trim();
  const repoName =
    normalizedOwner && normalizedRepo
      ? `${normalizedOwner}/${normalizedRepo}`
      : "目标仓库";
  const tokenCreateUrl = buildFineGrainedTokenUrl(normalizedOwner);

  return (
    <Dialog>
      <DialogTrigger
        render={
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="h-auto gap-1 px-1 py-0 text-xs text-muted-foreground hover:text-foreground"
          />
        }
      >
        如何获取？
        <ExternalLinkIcon className="size-3" />
      </DialogTrigger>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>如何获取 GitHub Access Token</DialogTitle>
        </DialogHeader>
        <div className="space-y-5 text-sm">
          <div className="rounded-lg border border-green-200 bg-green-50/50 p-4 dark:border-green-900 dark:bg-green-950/20">
            <div className="mb-3 flex items-center gap-2">
              <span className="inline-flex items-center rounded-full bg-green-100 px-2 py-0.5 text-xs font-medium text-green-800 dark:bg-green-900 dark:text-green-200">
                推荐
              </span>
              <span className="font-semibold text-green-900 dark:text-green-200">
                Fine-grained personal access token
              </span>
            </div>
            <p className="mb-3 text-muted-foreground">
              权限更精细、更安全，仅授予特定仓库所需的权限。
            </p>
            <ol className="space-y-2 text-foreground">
              <li className="flex gap-2">
                <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-green-100 text-xs font-semibold text-green-800 dark:bg-green-900 dark:text-green-200">
                  1
                </span>
                <span>
                  前往{" "}
                  <a
                    href={tokenCreateUrl}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="text-primary underline underline-offset-2 hover:no-underline"
                  >
                    GitHub Token 创建页
                  </a>
                </span>
              </li>
              <li className="flex gap-2">
                <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-green-100 text-xs font-semibold text-green-800 dark:bg-green-900 dark:text-green-200">
                  2
                </span>
                <span>填写 Token name（如：Fast Ship）</span>
              </li>
              <li className="flex gap-2">
                <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-green-100 text-xs font-semibold text-green-800 dark:bg-green-900 dark:text-green-200">
                  3
                </span>
                <span>
                  设置 <strong>Expiration</strong> 时，建议选择不超过{" "}
                  <strong>366 天</strong>，部分组织会拒绝更长期限的
                  Fine-grained Token
                </span>
              </li>
              <li className="flex gap-2">
                <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-green-100 text-xs font-semibold text-green-800 dark:bg-green-900 dark:text-green-200">
                  4
                </span>
                <span>
                  在 <strong>Resource owner</strong> 中选择{" "}
                  <code className="rounded bg-muted px-1 py-0.5 font-mono text-xs">
                    {normalizedOwner || "仓库所属个人或组织"}
                  </code>
                  ，它必须与项目里的 GitHub Owner 一致
                </span>
              </li>
              <li className="flex gap-2">
                <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-green-100 text-xs font-semibold text-green-800 dark:bg-green-900 dark:text-green-200">
                  5
                </span>
                <span>
                  在 <strong>Repository access</strong> 中选择包含{" "}
                  <code className="rounded bg-muted px-1 py-0.5 font-mono text-xs">
                    {repoName}
                  </code>{" "}
                  的仓库
                </span>
              </li>
              <li className="flex gap-2">
                <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-green-100 text-xs font-semibold text-green-800 dark:bg-green-900 dark:text-green-200">
                  6
                </span>
                <span>
                  在 <strong>Permissions</strong> →{" "}
                  <strong>Repository permissions</strong> 中设置{" "}
                  <code className="rounded bg-muted px-1 py-0.5 font-mono text-xs">
                    Contents
                  </code>{" "}
                  为 <strong>Read and write</strong>
                </span>
              </li>
              <li className="flex gap-2">
                <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-green-100 text-xs font-semibold text-green-800 dark:bg-green-900 dark:text-green-200">
                  7
                </span>
                <span>
                  点击 <strong>Generate token</strong>，复制生成的 Token
                  （仅显示一次）
                </span>
              </li>
            </ol>
          </div>

          <div className="rounded-lg border border-border p-4">
            <div className="mb-3 flex items-center gap-2">
              <span className="inline-flex items-center rounded-full bg-muted px-2 py-0.5 text-xs font-medium text-muted-foreground">
                备选
              </span>
              <span className="font-semibold">
                Classic personal access token
              </span>
            </div>
            <p className="mb-3 text-muted-foreground">
              如果你需要访问多个仓库或组织仓库，可以使用 Classic Token。
            </p>
            <ol className="space-y-2 text-foreground">
              <li className="flex gap-2">
                <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-primary/10 text-xs font-semibold text-primary">
                  1
                </span>
                <span>
                  前往 <strong>Settings</strong> →{" "}
                  <strong>Developer settings</strong> →{" "}
                  <strong>Personal access tokens</strong> →{" "}
                  <strong>Tokens (classic)</strong>
                </span>
              </li>
              <li className="flex gap-2">
                <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-primary/10 text-xs font-semibold text-primary">
                  2
                </span>
                <span>
                  点击 <strong>Generate new token (classic)</strong>
                </span>
              </li>
              <li className="flex gap-2">
                <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-primary/10 text-xs font-semibold text-primary">
                  3
                </span>
                <span>
                  在 <strong>Scopes</strong> 中勾选{" "}
                  <code className="rounded bg-muted px-1 py-0.5 font-mono text-xs">
                    repo
                  </code>{" "}
                  权限
                </span>
              </li>
              <li className="flex gap-2">
                <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-primary/10 text-xs font-semibold text-primary">
                  4
                </span>
                <span>
                  点击 <strong>Generate token</strong>，复制生成的 Token
                </span>
              </li>
            </ol>
          </div>

          <div className="rounded-lg border border-amber-200 bg-amber-50 p-3 text-amber-800 dark:border-amber-900 dark:bg-amber-950/30 dark:text-amber-400">
            <p className="text-xs">
              如果发货时报 <code>Resource not accessible by personal access token</code>
              ，通常就是 Resource owner、Repository access 或 Contents
              写权限不匹配。
            </p>
          </div>

          <div className="rounded-lg border border-amber-200 bg-amber-50 p-3 text-amber-800 dark:border-amber-900 dark:bg-amber-950/30 dark:text-amber-400">
            <p className="text-xs">
              Token 生成后只显示一次，请立即复制保存。如果遗失，需重新生成。
            </p>
          </div>
        </div>
        <DialogFooter>
          <a
            href={tokenCreateUrl}
            target="_blank"
            rel="noopener noreferrer"
            className="inline-flex items-center gap-1.5 text-xs text-primary underline underline-offset-2 hover:no-underline"
          >
            按当前 Owner 预填 Fine-grained Token
            <ExternalLinkIcon className="size-3" />
          </a>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
