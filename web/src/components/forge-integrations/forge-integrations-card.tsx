import { useState } from "react";
import { GitBranch, Pencil, Plus, RefreshCw, Trash2 } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { GithubIcon } from "@/components/ui/github-icon";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { useAuthStore } from "@/lib/store/auth-store";
import { ApiRequestError, errorText } from "@/lib/api/client";
import {
  useDeleteForgeIntegration,
  useForgeIntegrations,
  useTestForgeIntegration,
} from "@/lib/hooks/use-forge-integrations";
import { ForgeIntegrationDialog } from "./forge-integration-dialog";

// 凭证状态徽章：已配置用绿色（与 GitHub App「已连接」同色系），未配置用中性灰。
function credentialBadge(tokenSet: boolean, builtin: boolean) {
  return tokenSet ? (
    <Badge
      variant="secondary"
      className="bg-emerald-500/10 text-emerald-700 dark:text-emerald-400"
    >
      {builtin ? "GitHub App 已配置" : "凭证已配置"}
    </Badge>
  ) : (
    <Badge variant="secondary">
      {builtin ? "GitHub App 未配置" : "未配置凭证"}
    </Badge>
  );
}

/** 内置 github-default 行：只读映射部署 GitHub App，入口在下方卡片。 */
function BuiltinRow({ integration }: { integration: ForgeIntegration }) {
  return (
    <div className="flex items-start gap-3 border-b p-4">
      <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-full bg-muted text-foreground">
        <GithubIcon className="h-4 w-4" />
      </div>
      <div className="min-w-0 flex-1 space-y-1">
        <div className="flex flex-wrap items-center gap-2">
          <p className="font-medium">{integration.name}</p>
          <Badge variant="outline">内置</Badge>
          {integration.unavailable ? (
            <Badge variant="secondary" className="text-amber-600 dark:text-amber-400">
              不可用——请检查 GitHub App 配置
            </Badge>
          ) : (
            credentialBadge(integration.token_set, true)
          )}
        </div>
        <p className="truncate font-mono text-xs text-muted-foreground">
          {integration.base_url}
        </p>
      </div>
      <p className="hidden shrink-0 text-xs text-muted-foreground sm:block">
        在下方 GitHub App 卡片中管理
      </p>
    </div>
  );
}

/** 删除确认：仍被项目引用时删除会被拒绝（40935），提示先换绑项目。 */
function DeleteIntegrationAlert({
  integration,
  onDelete,
}: {
  integration: ForgeIntegration;
  onDelete: () => void;
}) {
  return (
    <AlertDialog>
      <AlertDialogTrigger
        render={
          <Button
            variant="ghost"
            size="sm"
            className="text-muted-foreground hover:text-destructive"
            aria-label={`删除 ${integration.name}`}
          />
        }
      >
        <Trash2 className="mr-1 h-3.5 w-3.5" />
        删除
      </AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>删除 {integration.name}？</AlertDialogTitle>
          <AlertDialogDescription>
            删除后使用该集成访问仓库的项目将无法连接。仍被项目引用时删除会被拒绝，
            需先在项目中换用其它集成。
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>取消</AlertDialogCancel>
          <AlertDialogAction variant="destructive" onClick={onDelete}>
            确认删除
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

function ForgejoRow({
  integration,
  isAdmin,
  onEdit,
}: {
  integration: ForgeIntegration;
  isAdmin: boolean;
  onEdit: () => void;
}) {
  const testConnection = useTestForgeIntegration();
  const deleteIntegration = useDeleteForgeIntegration();

  const handleTest = async () => {
    try {
      const res = await testConnection.mutateAsync(integration.id);
      const result = res.data;
      if (result.status === "ok") {
        // 检测成功给实例版本与机器人账号，用户能确认连到的是哪台。
        const summary = [
          result.version && `实例版本 ${result.version}`,
          result.username && `账号 @${result.username}`,
        ]
          .filter(Boolean)
          .join(" · ");
        toast.success(summary || "连接正常");
      } else if (result.status === "invalid") {
        toast.error(result.detail || "凭证被拒绝，请检查访问令牌");
      } else {
        toast.error(result.detail || "检测失败");
      }
    } catch (error) {
      toast.error(errorText(error, "检测失败"));
    }
  };

  // 40935：仍被项目引用，服务端 message 已含引用项目名，直接展示。
  const handleDelete = async () => {
    try {
      await deleteIntegration.mutateAsync(integration.id);
      toast.success("集成已删除");
    } catch (err) {
      toast.error(
        err instanceof ApiRequestError && err.status === 409
          ? err.message
          : errorText(err, "删除失败"),
      );
    }
  };

  return (
    <div className="flex items-start gap-3 border-b p-4">
      <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-full bg-muted text-foreground">
        <GitBranch className="h-4 w-4" />
      </div>
      <div className="min-w-0 flex-1 space-y-1">
        <div className="flex flex-wrap items-center gap-2">
          <p className="font-medium">{integration.name}</p>
          <Badge variant="outline">Forgejo</Badge>
          {credentialBadge(integration.token_set, false)}
          {integration.allow_private_network && (
            <Badge
              variant="secondary"
              className="bg-amber-500/15 text-amber-700 dark:text-amber-400"
            >
              允许内网
            </Badge>
          )}
          {integration.has_ca_cert && (
            <Badge variant="secondary">自定义 CA</Badge>
          )}
        </div>
        <p className="truncate font-mono text-xs text-muted-foreground">
          <span className="font-sans">站点</span> {integration.base_url}
        </p>
        <p className="truncate font-mono text-xs text-muted-foreground">
          <span className="font-sans">API</span> {integration.api_base_url}
        </p>
      </div>
      {isAdmin && (
        <div className="flex shrink-0 items-center gap-1">
          <Button
            variant="ghost"
            size="sm"
            disabled={testConnection.isPending}
            onClick={() => void handleTest()}
          >
            <RefreshCw className="mr-1 h-3.5 w-3.5" />
            {testConnection.isPending ? "检测中..." : "测试连接"}
          </Button>
          <Button
            variant="ghost"
            size="sm"
            aria-label={`编辑 ${integration.name}`}
            onClick={onEdit}
          >
            <Pencil className="mr-1 h-3.5 w-3.5" />
            编辑
          </Button>
          <DeleteIntegrationAlert
            integration={integration}
            onDelete={() => void handleDelete()}
          />
        </div>
      )}
    </div>
  );
}

/** 部署级代码托管集成：内置 github-default + forgejo 行；写操作仅管理员。 */
export function ForgeIntegrationsCard() {
  const isAdmin = useAuthStore((state) => state.user?.role === "admin");
  const {
    data: integrations,
    isLoading,
    isError,
    isFetching,
    refetch,
  } = useForgeIntegrations();
  const [createOpen, setCreateOpen] = useState(false);
  const [editing, setEditing] = useState<ForgeIntegration | null>(null);

  return (
    <div className="rounded-xl border">
      <div className="flex items-start gap-3 border-b p-4">
        <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-full bg-muted text-foreground">
          <GitBranch className="h-4 w-4" />
        </div>
        <div className="min-w-0 flex-1 space-y-1">
          <p className="font-medium">代码托管集成</p>
          <p className="text-xs text-muted-foreground">
            部署级代码仓连接，项目按此处配置的凭证访问仓库；内置
            GitHub App 走下方卡片。
          </p>
        </div>
        {isAdmin && (
          <Button
            size="sm"
            className="shrink-0"
            onClick={() => setCreateOpen(true)}
          >
            <Plus className="mr-1.5 h-3.5 w-3.5" />
            新建集成
          </Button>
        )}
      </div>

      {isLoading ? (
        <div className="space-y-3 p-4">
          <Skeleton className="h-12 rounded-lg" />
          <Skeleton className="h-12 rounded-lg" />
        </div>
      ) : isError ? (
        <div className="p-4 text-center">
          <p className="text-sm text-muted-foreground">集成列表加载失败</p>
          <Button
            variant="outline"
            size="sm"
            className="mt-3"
            disabled={isFetching}
            onClick={() => void refetch()}
          >
            重试
          </Button>
        </div>
      ) : (
        (integrations ?? []).map((integration) =>
          integration.builtin ? (
            <BuiltinRow key={integration.id} integration={integration} />
          ) : (
            <ForgejoRow
              key={integration.id}
              integration={integration}
              isAdmin={isAdmin}
              onEdit={() => setEditing(integration)}
            />
          ),
        )
      )}

      <ForgeIntegrationDialog
        integration={editing}
        open={createOpen || editing !== null}
        onOpenChange={(open) => {
          if (!open) {
            setCreateOpen(false);
            setEditing(null);
          }
        }}
      />
    </div>
  );
}
