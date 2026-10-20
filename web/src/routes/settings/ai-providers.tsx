import { useState } from "react";
import { Pencil, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
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
import { HeaderActions } from "@/components/layout/header-actions";
import { ProviderIcon } from "@/components/threads/provider-icon";
import { CreateProviderDialog } from "@/components/ai-providers/create-dialog";
import { EditProviderDialog } from "@/components/ai-providers/edit-dialog";
import { SettingsPageShell } from "@/routes/settings/layout";
import { ApiRequestError, errorText } from "@/lib/api/client";
import {
  AI_PROVIDER_CUSTOM_ID,
  type AIProvider,
} from "@/lib/api/ai-providers";
import {
  useAIProviders,
  useAIProviderCatalog,
  useDeleteAIProvider,
} from "@/lib/hooks/use-ai-providers";
import { formatDate } from "@/lib/utils/format";

/** 名称单元格：提供商名 + 目录名（或「自定义」）标注来源。 */
function ProviderNameCell({ provider }: { provider: AIProvider }) {
  const catalog = useAIProviderCatalog();
  const catalogName =
    provider.catalog_id === AI_PROVIDER_CUSTOM_ID
      ? "自定义"
      : (catalog.data?.find((p) => p.id === provider.catalog_id)?.name ??
        provider.catalog_id);
  return (
    <div className="flex items-center gap-2.5">
      <ProviderIcon
        providerInstanceId={provider.catalog_id}
        className="size-4"
      />
      <div className="min-w-0">
        <div className="truncate font-medium">{provider.name}</div>
        <div className="truncate text-xs text-muted-foreground">
          {catalogName}
        </div>
      </div>
    </div>
  );
}

/**
 * 删除确认框：被机器人引用的提供商删除必然 409，只给「知道了」不给破坏性
 * 按钮；未被引用时才显示取消 + 确认删除。
 */
function DeleteProviderAlert({
  provider,
  onDelete,
}: {
  provider: AIProvider;
  onDelete: (provider: AIProvider) => void;
}) {
  const referenced = provider.bot_count > 0;
  return (
    <AlertDialog>
      <AlertDialogTrigger
        render={
          <Button
            variant="ghost"
            size="sm"
            aria-label={`删除 ${provider.name}`}
          />
        }
      >
        <Trash2 className="h-4 w-4" />
      </AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>删除 AI 提供商？</AlertDialogTitle>
          <AlertDialogDescription>
            {referenced
              ? `该提供商仍被 ${provider.bot_count} 个机器人引用，删除会被拒绝。请先给这些机器人换用其它提供商。`
              : "删除后无法恢复。"}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          {referenced ? (
            <AlertDialogCancel>知道了</AlertDialogCancel>
          ) : (
            <>
              <AlertDialogCancel>取消</AlertDialogCancel>
              <AlertDialogAction onClick={() => onDelete(provider)}>
                确认删除
              </AlertDialogAction>
            </>
          )}
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

export default function AIProvidersPage() {
  const {
    data: providers,
    isLoading,
    isError,
    refetch,
  } = useAIProviders();
  const deleteProvider = useDeleteAIProvider();

  const [createOpen, setCreateOpen] = useState(false);
  const [editing, setEditing] = useState<AIProvider | null>(null);

  const handleDelete = async (provider: AIProvider) => {
    try {
      await deleteProvider.mutateAsync(provider.id);
      toast.success("AI 提供商已删除");
    } catch (err) {
      toast.error(
        err instanceof ApiRequestError && err.status === 409
          ? err.message
          : errorText(err, "删除失败"),
      );
    }
  };

  return (
    <SettingsPageShell
      wide
      actions={
        <HeaderActions
          primary={
            <Button size="sm" onClick={() => setCreateOpen(true)}>
              <Plus className="mr-1.5 h-3.5 w-3.5" />
              添加提供商
            </Button>
          }
        />
      }
    >
      <div className="space-y-6">
        <div>
          <h2 className="text-lg font-medium">AI 提供商</h2>
          <p className="text-sm text-muted-foreground">
            统一管理 OpenAI 兼容接口的地址与密钥，机器人按引用使用；
            更新密钥后所有引用它的机器人下个运行生效。
          </p>
        </div>

        <CreateProviderDialog open={createOpen} onOpenChange={setCreateOpen} />
        <EditProviderDialog
          provider={editing}
          open={editing !== null}
          onOpenChange={(open) => {
            if (!open) setEditing(null);
          }}
        />

        {isLoading ? (
          <div className="space-y-3">
            {Array.from({ length: 3 }).map((_, i) => (
              <Skeleton key={i} className="h-12 rounded" />
            ))}
          </div>
        ) : isError ? (
          <div className="rounded-lg border bg-muted/40 py-12 text-center">
            <p className="text-sm text-muted-foreground">
              AI 提供商加载失败
            </p>
            <Button
              variant="outline"
              size="sm"
              className="mt-3"
              onClick={() => void refetch()}
            >
              重试
            </Button>
          </div>
        ) : !providers?.length ? (
          <div className="rounded-lg border bg-muted/40 py-12 text-center">
            <p className="text-sm text-muted-foreground">
              还没有配置 AI 提供商
            </p>
            <p className="mt-1 text-xs text-muted-foreground">
              点击右上角按钮添加，之后新建机器人只需选模型
            </p>
          </div>
        ) : (
          <div className="overflow-hidden rounded-lg border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>名称</TableHead>
                  <TableHead>API 地址</TableHead>
                  <TableHead>API Key</TableHead>
                  <TableHead>引用机器人</TableHead>
                  <TableHead>创建时间</TableHead>
                  <TableHead className="text-right">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {providers.map((provider) => (
                  <TableRow key={provider.id}>
                    <TableCell className="whitespace-normal">
                      <ProviderNameCell provider={provider} />
                    </TableCell>
                    <TableCell className="max-w-56 font-mono text-sm">
                      <span
                        className="block truncate"
                        title={provider.base_url}
                      >
                        {provider.base_url}
                      </span>
                    </TableCell>
                    <TableCell className="font-mono text-sm">
                      ···{provider.api_key_last4}
                    </TableCell>
                    <TableCell className="text-sm">
                      {provider.bot_count > 0 ? (
                        `${provider.bot_count} 个机器人`
                      ) : (
                        <span className="text-xs text-muted-foreground">
                          未被引用
                        </span>
                      )}
                    </TableCell>
                    <TableCell className="text-sm">
                      {formatDate(provider.created_at)}
                    </TableCell>
                    <TableCell className="text-right">
                      <div className="flex justify-end gap-1">
                        <Button
                          variant="ghost"
                          size="sm"
                          aria-label={`编辑 ${provider.name}`}
                          onClick={() => setEditing(provider)}
                        >
                          <Pencil className="h-4 w-4" />
                        </Button>
                        <DeleteProviderAlert
                          provider={provider}
                          onDelete={(p) => void handleDelete(p)}
                        />
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        )}
      </div>
    </SettingsPageShell>
  );
}
