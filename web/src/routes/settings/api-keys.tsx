import { useState } from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { Plus, Copy, KeyRound, Trash2 } from "lucide-react";
import { Header } from "@/components/layout/header";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
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
  DialogTrigger,
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
import { apiKeySchema, type ApiKeyInput } from "@/lib/utils/validators";
import {
  useApiKeys,
  useCreateApiKey,
  useDeleteApiKey,
} from "@/lib/hooks/use-api-keys";
import { formatDate } from "@/lib/utils/format";
import { toast } from "sonner";

export default function ApiKeysPage() {
  const { data: apiKeys, isLoading } = useApiKeys();
  const createApiKey = useCreateApiKey();
  const deleteApiKey = useDeleteApiKey();

  const [createOpen, setCreateOpen] = useState(false);
  const [createdKey, setCreatedKey] = useState<string | null>(null);

  const {
    register,
    handleSubmit,
    reset,
    formState: { errors, isSubmitting },
  } = useForm<ApiKeyInput>({ resolver: zodResolver(apiKeySchema) });

  const onSubmit = async (data: ApiKeyInput) => {
    try {
      const res = await createApiKey.mutateAsync(data);
      setCreatedKey(res.data.key);
      reset();
    } catch {
      toast.error("创建失败");
    }
  };

  const handleCopy = async (key: string) => {
    await navigator.clipboard.writeText(key);
    toast.success("已复制到剪贴板");
  };

  const handleDelete = async (id: string) => {
    try {
      await deleteApiKey.mutateAsync(id);
      toast.success("API Key 已删除");
    } catch {
      toast.error("删除失败");
    }
  };

  const handleCloseCreate = () => {
    setCreateOpen(false);
    setCreatedKey(null);
    reset();
  };

  return (
    <>
      <Header title="API Keys" />
      <div className="p-4 md:p-6 space-y-6">
        <Card>
          <CardHeader className="flex-row items-start justify-between space-y-0">
            <div>
              <CardTitle>API Key 管理</CardTitle>
              <CardDescription className="mt-1">
                API Key 用于 CI/CD 等自动化场景，仅拥有受限权限
              </CardDescription>
            </div>
            <Dialog open={createOpen} onOpenChange={setCreateOpen}>
              <DialogTrigger render={<Button size="sm" />}>
                  <Plus className="mr-1.5 h-3.5 w-3.5" />
                  创建 API Key
              </DialogTrigger>
              <DialogContent>
                {createdKey ? (
                  <>
                    <DialogHeader>
                      <DialogTitle>API Key 创建成功</DialogTitle>
                      <DialogDescription>
                        请立即复制保存，此 Key 仅展示一次
                      </DialogDescription>
                    </DialogHeader>
                    <div className="flex items-center gap-2 rounded-md border bg-muted p-3">
                      <code className="flex-1 break-all text-sm">
                        {createdKey}
                      </code>
                      <Button
                        variant="ghost"
                        size="sm"
                        aria-label="复制 API Key"
                        onClick={() => handleCopy(createdKey)}
                      >
                        <Copy className="h-4 w-4" />
                      </Button>
                    </div>
                    <DialogFooter>
                      <Button onClick={handleCloseCreate}>我已保存</Button>
                    </DialogFooter>
                  </>
                ) : (
                  <>
                    <DialogHeader>
                      <DialogTitle>创建 API Key</DialogTitle>
                      <DialogDescription>
                        为你的 CI/CD 流水线创建一个访问令牌
                      </DialogDescription>
                    </DialogHeader>
                    <form onSubmit={handleSubmit(onSubmit)}>
                      <div className="space-y-2 py-2">
                        <Label htmlFor="name">备注名称</Label>
                        <Input
                          id="name"
                          placeholder="例如 CI-Android"
                          {...register("name")}
                        />
                        {errors.name && (
                          <p className="text-xs text-destructive">
                            {errors.name.message}
                          </p>
                        )}
                      </div>
                      <DialogFooter className="mt-4">
                        <Button
                          type="button"
                          variant="outline"
                          onClick={handleCloseCreate}
                        >
                          取消
                        </Button>
                        <Button type="submit" disabled={isSubmitting}>
                          {isSubmitting ? "创建中..." : "创建"}
                        </Button>
                      </DialogFooter>
                    </form>
                  </>
                )}
              </DialogContent>
            </Dialog>
          </CardHeader>
          <CardContent>
            {isLoading ? (
              <div className="space-y-3">
                {Array.from({ length: 3 }).map((_, i) => (
                  <Skeleton key={i} className="h-10 rounded" />
                ))}
              </div>
            ) : !apiKeys?.length ? (
              <div className="flex flex-col items-center py-10">
                <KeyRound className="mb-3 h-10 w-10 text-muted-foreground/50" />
                <p className="text-sm text-muted-foreground">暂无 API Key</p>
              </div>
            ) : (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>名称</TableHead>
                    <TableHead>Key 前缀</TableHead>
                    <TableHead>创建时间</TableHead>
                    <TableHead>最后使用</TableHead>
                    <TableHead className="text-right">操作</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {apiKeys.map((key) => (
                    <TableRow key={key.id}>
                      <TableCell className="font-medium">{key.name}</TableCell>
                      <TableCell className="font-mono text-sm">
                        {key.key_prefix}••••••••
                      </TableCell>
                      <TableCell>{formatDate(key.created_at)}</TableCell>
                      <TableCell>
                        {key.last_used_at
                          ? formatDate(key.last_used_at)
                          : "从未使用"}
                      </TableCell>
                      <TableCell className="text-right">
                        <AlertDialog>
                          <AlertDialogTrigger
                            render={
                              <Button
                                variant="ghost"
                                size="sm"
                                aria-label={`删除 API Key ${key.name}`}
                              />
                            }
                          >
                              <Trash2 className="h-4 w-4" />
                        </AlertDialogTrigger>
                          <AlertDialogContent>
                            <AlertDialogHeader>
                              <AlertDialogTitle>
                                删除 API Key?
                              </AlertDialogTitle>
                              <AlertDialogDescription>
                                删除后使用此 Key 的 CI/CD 流水线将立即失效。
                              </AlertDialogDescription>
                            </AlertDialogHeader>
                            <AlertDialogFooter>
                              <AlertDialogCancel>取消</AlertDialogCancel>
                              <AlertDialogAction
                                onClick={() => handleDelete(key.id)}
                              >
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
            )}
          </CardContent>
        </Card>
      </div>
    </>
  );
}
