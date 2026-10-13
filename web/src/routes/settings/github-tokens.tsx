import { useState } from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { Plus, Pencil, RefreshCw, Trash2 } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
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
import { Skeleton } from "@/components/ui/skeleton";
import { HeaderActions } from "@/components/layout/header-actions";
import { SettingsPageShell } from "@/routes/settings/layout";
import { GithubTokenCreateDialog } from "@/components/settings/github-token-create-dialog";
import {
  githubTokenTitleSchema,
  type GithubTokenTitleInput,
} from "@/lib/utils/validators";
import {
  useGithubTokens,
  useUpdateGithubToken,
  useDeleteGithubToken,
  useDetectGithubToken,
} from "@/lib/hooks/use-github-tokens";
import { formatDate } from "@/lib/utils/format";
import { toast } from "sonner";

const detectionStatusMeta: Record<
  GitHubTokenDetectionStatus,
  { label: string; variant: "default" | "secondary" | "destructive" | "outline" }
> = {
  unchecked: { label: "未检测", variant: "outline" },
  ok: { label: "认证成功", variant: "default" },
  invalid: { label: "凭证无效", variant: "destructive" },
  failed: { label: "检测失败", variant: "secondary" },
};

const tokenTypeLabel: Record<GitHubTokenDetectionTokenType, string> = {
  classic: "Classic",
  fine_grained: "Fine-grained",
  unknown: "未知类型",
};

function DetectionCell({ token }: { token: GitHubToken }) {
  const meta = detectionStatusMeta[token.detection_status];
  return (
    <div className="space-y-0.5">
      <Badge variant={meta.variant}>{meta.label}</Badge>
      {token.detection_status !== "unchecked" && (
        <p className="text-xs text-muted-foreground">
          {tokenTypeLabel[token.detection_token_type]}
          {token.detection_login ? ` · ${token.detection_login}` : ""}
        </p>
      )}
      {token.detection_token_type === "classic" &&
        token.detection_scopes.length > 0 && (
          <p className="text-xs text-muted-foreground">
            scopes: {token.detection_scopes.join(", ")}
          </p>
        )}
      {token.detection_token_type === "fine_grained" && (
        <p className="text-xs text-muted-foreground">无法自动获取完整授权</p>
      )}
      {token.detection_detail && (
        <p className="text-xs text-muted-foreground">{token.detection_detail}</p>
      )}
    </div>
  );
}

function EditTokenDialog({
  token,
  open,
  onOpenChange,
}: {
  token: GitHubToken | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const updateToken = useUpdateGithubToken();
  // 替换密钥是两段确认：填入新密钥提交后先展示受影响项目，确认才真正提交。
  const [pendingToken, setPendingToken] = useState<string | null>(null);
  const [confirmingReplace, setConfirmingReplace] = useState(false);

  const {
    register,
    handleSubmit,
    reset,
    formState: { errors },
  } = useForm<GithubTokenTitleInput>({
    resolver: zodResolver(githubTokenTitleSchema),
    values: { title: token?.title ?? "" },
  });

  const handleClose = () => {
    setPendingToken(null);
    setConfirmingReplace(false);
    onOpenChange(false);
    reset();
  };

  const doUpdate = async (title: string, tokenValue: string | null) => {
    if (!token) return;
    try {
      await updateToken.mutateAsync({
        id: token.id,
        ...(title !== token.title ? { title } : {}),
        ...(tokenValue ? { token: tokenValue } : {}),
      });
      toast.success(tokenValue ? "Token 已替换并对所有引用生效" : "标题已更新");
      handleClose();
    } catch {
      toast.error("更新失败：相同 Token 可能已存在");
    }
  };

  const onSubmit = async (data: GithubTokenTitleInput) => {
    const tokenValue = (pendingToken ?? "").trim();
    if (tokenValue) {
      if (!confirmingReplace) {
        setConfirmingReplace(true);
        return;
      }
      await doUpdate(data.title, tokenValue);
      return;
    }
    await doUpdate(data.title, null);
  };

  return (
    <Dialog open={open} onOpenChange={(next) => (next ? undefined : handleClose())}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>编辑 Token</DialogTitle>
          <DialogDescription>
            替换密钥即对所有引用项目生效；仅改标题不会重新检测
          </DialogDescription>
        </DialogHeader>
        <form
          onSubmit={(event) => {
            event.preventDefault();
            void handleSubmit(onSubmit)(event);
          }}
        >
          <div className="space-y-4 py-2">
            <div className="space-y-2">
              <Label htmlFor="edit-token-title">标题</Label>
              <Input id="edit-token-title" {...register("title")} />
              {errors.title && (
                <p className="text-xs text-destructive">{errors.title.message}</p>
              )}
            </div>
            <div className="space-y-2">
              <Label htmlFor="edit-token-value">替换密钥（可选）</Label>
              <Input
                id="edit-token-value"
                type="password"
                placeholder="留空则不替换"
                onChange={(event) => {
                  setPendingToken(event.target.value);
                  setConfirmingReplace(false);
                }}
              />
              <p className="text-xs text-muted-foreground">
                {pendingToken
                  ? "提交前会列出受影响的项目"
                  : `当前密钥前缀 ${token?.token_prefix ?? ""}••••••`}
              </p>
            </div>
            {confirmingReplace && token && (
              <div className="rounded-md border bg-muted p-3 text-xs">
                <p className="font-medium">
                  该 Token 被 {token.referenced_projects.length} 个项目引用：
                </p>
                <ul className="mt-1 list-disc pl-4">
                  {token.referenced_projects.map((p) => (
                    <li key={p.id}>{p.name}</li>
                  ))}
                </ul>
                <p className="mt-1 text-muted-foreground">
                  确认后以上项目立即改用新 Token。若与你的另一条 Token
                  重复，替换会被拒绝。
                </p>
              </div>
            )}
          </div>
          <DialogFooter className="mt-4">
            <Button type="button" variant="outline" onClick={handleClose}>
              取消
            </Button>
            <Button type="submit" disabled={updateToken.isPending}>
              {updateToken.isPending
                ? "保存中..."
                : confirmingReplace
                  ? "确认替换"
                  : "保存"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

export default function GithubTokensPage() {
  const { data: tokens, isLoading } = useGithubTokens();
  const deleteToken = useDeleteGithubToken();
  const detectToken = useDetectGithubToken();

  const [createOpen, setCreateOpen] = useState(false);
  const [editing, setEditing] = useState<GitHubToken | null>(null);

  const handleDelete = async (token: GitHubToken) => {
    try {
      await deleteToken.mutateAsync(token.id);
      toast.success("GitHub Token 已删除");
    } catch {
      toast.error("删除失败：该 Token 仍被项目引用，请先解除或更换引用");
    }
  };

  const handleDetect = async (token: GitHubToken) => {
    try {
      const res = await detectToken.mutateAsync(token.id);
      if (res.data.detection_status === "ok") {
        toast.success("检测通过");
      } else if (res.data.detection_status === "invalid") {
        toast.error("凭证无效：GitHub 拒绝了该 Token");
      } else {
        toast.error("检测失败：网络或 GitHub 服务异常，不代表凭证无效");
      }
    } catch {
      toast.error("检测失败");
    }
  };

  return (
    <SettingsPageShell
      actions={
        <HeaderActions
          primary={
            <Button size="sm" onClick={() => setCreateOpen(true)}>
              <Plus className="mr-1.5 h-3.5 w-3.5" />
              添加 Token
            </Button>
          }
        />
      }
    >
      <div className="space-y-6">
        <div>
          <h2 className="text-lg font-medium">GitHub Tokens</h2>
          <p className="text-sm text-muted-foreground">
            统一管理你的 GitHub Token，项目通过引用使用它们。
            <br />
            公开仓库 Token 用于 Issue 同步与 GitHub 写操作；代码仓库 Token
            仅用于读取关联 PR。
          </p>
        </div>

        <GithubTokenCreateDialog open={createOpen} onOpenChange={setCreateOpen} />
        <EditTokenDialog
          token={editing}
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
        ) : !tokens?.length ? (
          <div className="rounded-lg border bg-muted/40 py-12 text-center">
            <p className="text-sm text-muted-foreground">暂无 GitHub Token</p>
            <p className="text-xs text-muted-foreground mt-1">
              点击右上角按钮添加第一个 Token，之后在项目设置中引用
            </p>
          </div>
        ) : (
          <div className="rounded-lg border overflow-hidden">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>标题</TableHead>
                  <TableHead>Token</TableHead>
                  <TableHead>检测状态</TableHead>
                  <TableHead>引用项目</TableHead>
                  <TableHead>最近检测</TableHead>
                  <TableHead className="text-right">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {tokens.map((token) => (
                  <TableRow key={token.id}>
                    <TableCell className="font-medium">{token.title}</TableCell>
                    <TableCell className="font-mono text-sm">
                      {token.token_prefix}••••••••
                    </TableCell>
                    <TableCell>
                      <DetectionCell token={token} />
                    </TableCell>
                    <TableCell>
                      {token.referenced_projects.length === 0 ? (
                        <span className="text-xs text-muted-foreground">未被引用</span>
                      ) : (
                        <span className="text-xs">
                          {token.referenced_projects.map((p) => p.name).join("、")}
                        </span>
                      )}
                    </TableCell>
                    <TableCell className="text-sm">
                      {token.detected_at ? formatDate(token.detected_at) : "从未检测"}
                    </TableCell>
                    <TableCell className="text-right">
                      <div className="flex justify-end gap-1">
                        <Button
                          variant="ghost"
                          size="sm"
                          aria-label={`刷新检测 ${token.title}`}
                          disabled={detectToken.isPending}
                          onClick={() => handleDetect(token)}
                        >
                          <RefreshCw className="h-4 w-4" />
                        </Button>
                        <Button
                          variant="ghost"
                          size="sm"
                          aria-label={`编辑 ${token.title}`}
                          onClick={() => setEditing(token)}
                        >
                          <Pencil className="h-4 w-4" />
                        </Button>
                        <AlertDialog>
                          <AlertDialogTrigger
                            render={
                              <Button
                                variant="ghost"
                                size="sm"
                                aria-label={`删除 ${token.title}`}
                              />
                            }
                          >
                            <Trash2 className="h-4 w-4" />
                          </AlertDialogTrigger>
                          <AlertDialogContent>
                            <AlertDialogHeader>
                              <AlertDialogTitle>删除 GitHub Token?</AlertDialogTitle>
                              <AlertDialogDescription>
                                {token.referenced_projects.length > 0
                                  ? `该 Token 仍被 ${token.referenced_projects
                                      .map((p) => p.name)
                                      .join("、")} 引用，删除会先被拒绝。请先在项目中解除或更换引用。`
                                  : "删除后无法恢复，项目中的引用需另行清理。"}
                              </AlertDialogDescription>
                            </AlertDialogHeader>
                            <AlertDialogFooter>
                              <AlertDialogCancel>取消</AlertDialogCancel>
                              <AlertDialogAction onClick={() => handleDelete(token)}>
                                确认删除
                              </AlertDialogAction>
                            </AlertDialogFooter>
                          </AlertDialogContent>
                        </AlertDialog>
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
