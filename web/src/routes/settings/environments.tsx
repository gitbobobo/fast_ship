import { useState, type FormEvent } from "react";
import { HTTPError } from "ky";
import { Link2, Pencil, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
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
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
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
import { SettingsPageShell } from "@/routes/settings/layout";
import {
  useCreateEnvironment,
  useDeleteEnvironment,
  useEnvironments,
  useUpdateEnvironment,
} from "@/lib/hooks/use-threads";
import type { Environment, EnvironmentStatus } from "@/lib/api/threads";
import { ENVIRONMENT_STATUS_LABELS } from "@/lib/threads";
import { cn } from "@/lib/utils";

type DialogState =
  | { mode: "create" }
  | { mode: "rename"; environment: Environment }
  | { mode: "repair"; environment: Environment };

const STATUS_STYLES: Record<EnvironmentStatus, string> = {
  ok: "bg-emerald-500/10 text-emerald-700 dark:text-emerald-400",
  unreachable: "bg-amber-500/15 text-amber-700 dark:text-amber-400",
  repair: "bg-destructive/10 text-destructive",
  degraded: "bg-amber-500/15 text-amber-700 dark:text-amber-400",
};

async function errorMessage(error: unknown, fallback: string) {
  if (error instanceof HTTPError) {
    const body = await error.response
      .json<ApiResponse<unknown>>()
      .catch(() => null);
    if (body?.message) return body.message;
  }
  return fallback;
}

function EnvironmentDialog({
  state,
  onClose,
}: {
  state: DialogState;
  onClose: () => void;
}) {
  const create = useCreateEnvironment();
  const update = useUpdateEnvironment();
  const [name, setName] = useState(
    state.mode === "create" ? "" : state.environment.name,
  );
  const [pairingUrl, setPairingUrl] = useState("");
  const [error, setError] = useState("");
  const pending = create.isPending || update.isPending;

  const needsName = state.mode !== "repair";
  const needsPairing = state.mode !== "rename";
  const title = {
    create: "添加环境",
    rename: "修改环境名称",
    repair: "重新配对",
  }[state.mode];

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    const trimmedName = name.trim();
    const trimmedUrl = pairingUrl.trim();
    if (needsName && !trimmedName) return setError("请填写环境名称");
    if (needsPairing && !trimmedUrl) return setError("请粘贴配对链接");
    setError("");

    try {
      if (state.mode === "create") {
        await create.mutateAsync({ name: trimmedName, pairing_url: trimmedUrl });
        toast.success("环境已添加");
      } else if (state.mode === "rename") {
        await update.mutateAsync({ id: state.environment.id, name: trimmedName });
        toast.success("环境名称已更新");
      } else {
        await update.mutateAsync({
          id: state.environment.id,
          pairing_url: trimmedUrl,
        });
        toast.success("已重新配对");
      }
      onClose();
    } catch (err) {
      setError(await errorMessage(err, "保存失败，请检查配对链接后重试"));
    }
  };

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>
            {state.mode === "rename"
              ? `当前环境：${state.environment.address}`
              : "在 T3 Code 中生成配对链接，粘贴到下方即可。配对凭证只保存在服务端，不会再次显示。"}
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={handleSubmit} className="space-y-4">
          {needsName && (
            <div className="space-y-2">
              <Label htmlFor="environment-name">环境名称</Label>
              <Input
                id="environment-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="例如 工作电脑"
                autoFocus
              />
            </div>
          )}
          {needsPairing && (
            <div className="space-y-2">
              <Label htmlFor="environment-pairing-url">配对链接</Label>
              <Input
                id="environment-pairing-url"
                value={pairingUrl}
                onChange={(e) => setPairingUrl(e.target.value)}
                placeholder="粘贴配对链接"
                autoComplete="off"
                spellCheck={false}
              />
            </div>
          )}
          {error && (
            <p role="alert" className="text-xs text-destructive">
              {error}
            </p>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              取消
            </Button>
            <Button type="submit" disabled={pending}>
              {pending ? "保存中..." : "保存"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

export default function EnvironmentsPage() {
  const { data: environments, isLoading, isError, isFetching, refetch } =
    useEnvironments();
  const deleteEnvironment = useDeleteEnvironment();
  const [dialog, setDialog] = useState<DialogState | null>(null);

  const handleDelete = async (id: string) => {
    try {
      await deleteEnvironment.mutateAsync(id);
      toast.success("环境已删除");
    } catch (err) {
      toast.error(await errorMessage(err, "删除失败"));
    }
  };

  return (
    <SettingsPageShell>
      <div className="space-y-6">
        <div className="flex items-center justify-between gap-3">
          <div>
            <h2 className="text-lg font-medium">环境</h2>
            <p className="text-sm text-muted-foreground">
              每个环境对应一个 T3 Code 实例，线程页会汇总所有环境里的线程。
            </p>
          </div>
          <Button size="sm" onClick={() => setDialog({ mode: "create" })}>
            <Plus className="mr-1.5 h-3.5 w-3.5" />
            添加环境
          </Button>
        </div>

        {dialog && (
          <EnvironmentDialog
            key={dialog.mode === "create" ? "create" : `${dialog.mode}:${dialog.environment.id}`}
            state={dialog}
            onClose={() => setDialog(null)}
          />
        )}

        {isError && !!environments?.length && (
          <div
            role="alert"
            className="flex items-center justify-between gap-2 rounded-lg bg-amber-500/10 px-3 py-2 text-sm text-amber-800 dark:text-amber-300"
          >
            <span>刷新失败，当前显示的可能不是最新数据</span>
            <Button
              variant="outline"
              size="sm"
              onClick={() => refetch()}
              disabled={isFetching}
            >
              重试
            </Button>
          </div>
        )}

        {isLoading ? (
          <div className="space-y-3">
            {Array.from({ length: 2 }).map((_, i) => (
              <Skeleton key={i} className="h-10 rounded" />
            ))}
          </div>
        ) : !environments || (isError && environments.length === 0) ? (
          <div
            role="alert"
            className="rounded-lg border bg-muted/40 py-12 text-center"
          >
            <p className="text-sm text-muted-foreground">环境加载失败</p>
            <Button
              variant="outline"
              size="sm"
              className="mt-3"
              onClick={() => refetch()}
              disabled={isFetching}
            >
              重试
            </Button>
          </div>
        ) : environments.length === 0 ? (
          <div className="rounded-lg border bg-muted/40 py-12 text-center">
            <p className="text-sm text-muted-foreground">暂无环境</p>
            <p className="mt-1 text-xs text-muted-foreground">
              点击「添加环境」，粘贴配对链接添加第一个环境
            </p>
          </div>
        ) : (
          <div className="overflow-hidden rounded-lg border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>名称</TableHead>
                  <TableHead>状态</TableHead>
                  <TableHead className="text-right">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {environments.map((env) => (
                  <TableRow key={env.id}>
                    <TableCell className="max-w-48">
                      <div className="truncate font-medium">{env.name}</div>
                      <div className="truncate text-xs text-muted-foreground">
                        {env.address}
                      </div>
                    </TableCell>
                    <TableCell>
                      <span
                        className={cn(
                          "inline-flex rounded-full px-2 py-0.5 text-xs font-medium whitespace-nowrap",
                          STATUS_STYLES[env.status],
                        )}
                        title={env.error}
                      >
                        {ENVIRONMENT_STATUS_LABELS[env.status] ?? env.status}
                      </span>
                      {env.error && env.status !== "ok" && (
                        <div className="mt-1 max-w-48 truncate text-xs text-muted-foreground">
                          {env.error}
                        </div>
                      )}
                    </TableCell>
                    <TableCell className="text-right whitespace-nowrap">
                      <Button
                        variant="ghost"
                        size="sm"
                        aria-label={`修改名称 ${env.name}`}
                        onClick={() => setDialog({ mode: "rename", environment: env })}
                      >
                        <Pencil className="h-4 w-4" />
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        aria-label={`重新配对 ${env.name}`}
                        onClick={() => setDialog({ mode: "repair", environment: env })}
                      >
                        <Link2 className="h-4 w-4" />
                      </Button>
                      <AlertDialog>
                        <AlertDialogTrigger
                          render={
                            <Button
                              variant="ghost"
                              size="sm"
                              aria-label={`删除环境 ${env.name}`}
                            />
                          }
                        >
                          <Trash2 className="h-4 w-4" />
                        </AlertDialogTrigger>
                        <AlertDialogContent>
                          <AlertDialogHeader>
                            <AlertDialogTitle>删除环境?</AlertDialogTitle>
                            <AlertDialogDescription>
                              删除后将不再显示「{env.name}」里的线程。T3 Code 中的线程不受影响。
                            </AlertDialogDescription>
                          </AlertDialogHeader>
                          <AlertDialogFooter>
                            <AlertDialogCancel>取消</AlertDialogCancel>
                            <AlertDialogAction onClick={() => handleDelete(env.id)}>
                              确认删除
                            </AlertDialogAction>
                          </AlertDialogFooter>
                        </AlertDialogContent>
                      </AlertDialog>
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
