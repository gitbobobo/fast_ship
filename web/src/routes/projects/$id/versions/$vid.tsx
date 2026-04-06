import { useParams, useNavigate, Link } from "react-router";
import { useState, useRef } from "react";
import {
  Rocket,
  Upload,
  Trash2,
  Download,
  ExternalLink,
  ArrowLeft,
  Pencil,
  Eye,
} from "lucide-react";
import ReactMarkdown from "react-markdown";
import { Header } from "@/components/layout/header";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
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
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  useVersion,
  useUpdateVersion,
  useDeleteVersion,
  useShipVersion,
} from "@/lib/hooks/use-versions";
import { useUploadArtifact, useDeleteArtifact } from "@/lib/hooks/use-artifacts";
import { artifactApi } from "@/lib/api/artifacts";
import { formatDate, formatFileSize } from "@/lib/utils/format";
import { toast } from "sonner";

export default function VersionDetailPage() {
  const { id, vid } = useParams();
  const navigate = useNavigate();
  const { data: version, isLoading } = useVersion(vid!);
  const updateVersion = useUpdateVersion(vid!);
  const deleteVersion = useDeleteVersion(id!);
  const shipVersion = useShipVersion(vid!);
  const uploadArtifact = useUploadArtifact(vid!);
  const deleteArtifact = useDeleteArtifact(vid!);

  const [editingNotes, setEditingNotes] = useState(false);
  const [notes, setNotes] = useState("");
  const [editingCommitish, setEditingCommitish] = useState(false);
  const [commitish, setCommitish] = useState("");
  const [uploadPlatform, setUploadPlatform] = useState("");
  const [shipDialogOpen, setShipDialogOpen] = useState(false);
  const fileInputRef = useRef<HTMLInputElement>(null);

  const isPending = version?.status === "pending";
  const artifacts = version?.artifacts ?? [];

  const handleSaveNotes = async () => {
    try {
      await updateVersion.mutateAsync({ release_notes: notes });
      setEditingNotes(false);
      toast.success("Release 说明已更新");
    } catch {
      toast.error("更新失败");
    }
  };

  const handleSaveCommitish = async () => {
    try {
      await updateVersion.mutateAsync({ target_commitish: commitish });
      setEditingCommitish(false);
      toast.success("目标分支已更新");
    } catch {
      toast.error("更新失败");
    }
  };

  const handleUpload = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const files = e.target.files;
    if (!files?.length) return;

    for (const file of Array.from(files)) {
      const formData = new FormData();
      formData.append("file", file);
      if (uploadPlatform.trim()) {
        formData.append("platform", uploadPlatform.trim());
      }
      try {
        await uploadArtifact.mutateAsync(formData);
        toast.success(`${file.name} 上传成功`);
      } catch {
        toast.error(`${file.name} 上传失败`);
      }
    }
    if (fileInputRef.current) fileInputRef.current.value = "";
  };

  const handleDeleteArtifact = async (aid: string, name: string) => {
    try {
      await deleteArtifact.mutateAsync(aid);
      toast.success(`${name} 已删除`);
    } catch {
      toast.error("删除失败");
    }
  };

  const handleDeleteVersion = async () => {
    try {
      await deleteVersion.mutateAsync(vid!);
      toast.success("版本已删除");
      navigate(`/projects/${id}`, { replace: true });
    } catch {
      toast.error("删除失败");
    }
  };

  const handleShip = async () => {
    setShipDialogOpen(false);
    try {
      await shipVersion.mutateAsync();
      toast.success("发货成功！");
    } catch {
      toast.error("发货失败，请查看错误日志");
    }
  };

  // 发货前校验
  const shipChecks = version
    ? [
        { label: "Release 说明", ok: !!version.release_notes },
        { label: "安装包", ok: artifacts.length > 0 },
        { label: "目标分支 / Commit", ok: !!version.target_commitish },
      ]
    : [];
  const canShip = shipChecks.every((c) => c.ok);

  if (isLoading) {
    return (
      <>
        <Header title="版本详情" />
        <div className="p-4 md:p-6 space-y-4">
          <Skeleton className="h-20 rounded-lg" />
          <Skeleton className="h-48 rounded-lg" />
          <Skeleton className="h-32 rounded-lg" />
        </div>
      </>
    );
  }

  if (!version) {
    return (
      <>
        <Header title="版本详情" />
        <div className="p-4 md:p-6">
          <p className="text-sm text-muted-foreground">版本不存在</p>
        </div>
      </>
    );
  }

  return (
    <>
      <Header title={`版本 ${version.version_number}`} />
      <div className="p-4 md:p-6 space-y-6">
        {/* 顶栏 */}
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex items-center gap-3">
            <Button variant="ghost" size="sm" render={<Link to={`/projects/${id}`} />}>
                <ArrowLeft className="mr-1 h-4 w-4" />
                返回
            </Button>
            <span className="font-mono text-lg font-bold">
              {version.version_number}
            </span>
            <Badge
              variant={version.status === "shipped" ? "default" : "secondary"}
            >
              {version.status === "shipped" ? "已发货" : "待发货"}
            </Badge>
          </div>
          <div className="flex gap-2">
            {isPending && (
              <AlertDialog>
                <AlertDialogTrigger render={<Button variant="outline" size="sm" />}>
                    <Trash2 className="mr-1.5 h-3.5 w-3.5" />
                    删除
                </AlertDialogTrigger>
                <AlertDialogContent>
                  <AlertDialogHeader>
                    <AlertDialogTitle>确认删除版本?</AlertDialogTitle>
                    <AlertDialogDescription>
                      删除后相关安装包也将一并清除，且不可恢复。
                    </AlertDialogDescription>
                  </AlertDialogHeader>
                  <AlertDialogFooter>
                    <AlertDialogCancel>取消</AlertDialogCancel>
                    <AlertDialogAction onClick={handleDeleteVersion}>
                      确认删除
                    </AlertDialogAction>
                  </AlertDialogFooter>
                </AlertDialogContent>
              </AlertDialog>
            )}
            {version.github_release_url && (
              <Button variant="outline" size="sm" render={
                <a
                  href={version.github_release_url}
                  target="_blank"
                  rel="noopener noreferrer"
                />
              }>
                  <ExternalLink className="mr-1.5 h-3.5 w-3.5" />
                  GitHub Release
              </Button>
            )}
          </div>
        </div>

        {/* 基本信息 */}
        <Card>
          <CardHeader>
            <CardTitle className="text-base">基本信息</CardTitle>
          </CardHeader>
          <CardContent className="grid gap-3 text-sm sm:grid-cols-2">
            <div>
              <span className="text-muted-foreground">版本号：</span>
              {version.version_number}
            </div>
            <div>
              <span className="text-muted-foreground">状态：</span>
              {version.status === "shipped" ? "已发货" : "待发货"}
            </div>
            <div>
              <span className="text-muted-foreground">目标分支：</span>
              {editingCommitish ? (
                <span className="inline-flex items-center gap-2">
                  <Input
                    className="h-7 w-48 text-sm"
                    value={commitish}
                    onChange={(e) => setCommitish(e.target.value)}
                  />
                  <Button
                    size="sm"
                    variant="ghost"
                    className="h-7"
                    onClick={handleSaveCommitish}
                  >
                    保存
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    className="h-7"
                    onClick={() => setEditingCommitish(false)}
                  >
                    取消
                  </Button>
                </span>
              ) : (
                <span>
                  {version.target_commitish || "未设置"}
                  {isPending && (
                    <Button
                      variant="ghost"
                      size="sm"
                      className="ml-2 h-6 px-2"
                      onClick={() => {
                        setCommitish(version.target_commitish || "");
                        setEditingCommitish(true);
                      }}
                    >
                      <Pencil className="h-3 w-3" />
                    </Button>
                  )}
                </span>
              )}
            </div>
            <div>
              <span className="text-muted-foreground">创建时间：</span>
              {formatDate(version.created_at)}
            </div>
            {version.shipped_at && (
              <div>
                <span className="text-muted-foreground">发货时间：</span>
                {formatDate(version.shipped_at)}
              </div>
            )}
          </CardContent>
        </Card>

        {/* Release 说明 */}
        <Card>
          <CardHeader className="flex-row items-center justify-between space-y-0">
            <CardTitle className="text-base">Release 说明</CardTitle>
            {isPending && !editingNotes && (
              <Button
                variant="outline"
                size="sm"
                onClick={() => {
                  setNotes(version.release_notes || "");
                  setEditingNotes(true);
                }}
              >
                <Pencil className="mr-1.5 h-3.5 w-3.5" />
                编辑
              </Button>
            )}
          </CardHeader>
          <CardContent>
            {editingNotes ? (
              <Tabs defaultValue="edit">
                <TabsList>
                  <TabsTrigger value="edit">
                    <Pencil className="mr-1 h-3.5 w-3.5" />
                    编辑
                  </TabsTrigger>
                  <TabsTrigger value="preview">
                    <Eye className="mr-1 h-3.5 w-3.5" />
                    预览
                  </TabsTrigger>
                </TabsList>
                <TabsContent value="edit">
                  <Textarea
                    value={notes}
                    onChange={(e) => setNotes(e.target.value)}
                    rows={10}
                    placeholder="支持 Markdown 格式"
                  />
                </TabsContent>
                <TabsContent value="preview">
                  <div className="prose prose-sm dark:prose-invert max-w-none rounded-md border p-4">
                    {notes ? (
                      <ReactMarkdown>{notes}</ReactMarkdown>
                    ) : (
                      <p className="text-muted-foreground">暂无内容</p>
                    )}
                  </div>
                </TabsContent>
                <div className="mt-3 flex gap-2">
                  <Button size="sm" onClick={handleSaveNotes}>
                    保存
                  </Button>
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => setEditingNotes(false)}
                  >
                    取消
                  </Button>
                </div>
              </Tabs>
            ) : version.release_notes ? (
              <div className="prose prose-sm dark:prose-invert max-w-none">
                <ReactMarkdown>{version.release_notes}</ReactMarkdown>
              </div>
            ) : (
              <p className="text-sm text-muted-foreground">
                暂无 Release 说明
              </p>
            )}
          </CardContent>
        </Card>

        {/* 安装包 */}
        <Card>
          <CardHeader className="flex-row items-center justify-between space-y-0">
            <CardTitle className="text-base">
              安装包
              {artifacts.length > 0 && (
                <span className="ml-2 text-sm text-muted-foreground font-normal">
                  ({artifacts.length})
                </span>
              )}
            </CardTitle>
            {isPending && (
              <>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => fileInputRef.current?.click()}
                  disabled={uploadArtifact.isPending}
                >
                  <Upload className="mr-1.5 h-3.5 w-3.5" />
                  {uploadArtifact.isPending ? "上传中..." : "上传文件"}
                </Button>
                <input
                  ref={fileInputRef}
                  type="file"
                  multiple
                  className="hidden"
                  onChange={handleUpload}
                />
              </>
            )}
          </CardHeader>
          <CardContent>
            {isPending && (
              <div className="mb-4 flex flex-col gap-2 sm:max-w-xs">
                <span className="text-sm text-muted-foreground">
                  平台标识（可选）
                </span>
                <Input
                  value={uploadPlatform}
                  onChange={(e) => setUploadPlatform(e.target.value)}
                  placeholder="如 android / ios / macos"
                />
                <p className="text-xs text-muted-foreground">
                  同名文件会按替换处理，并更新平台与大小信息
                </p>
              </div>
            )}
            {artifacts.length === 0 ? (
              <p className="text-sm text-muted-foreground">暂无安装包</p>
            ) : (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>文件名</TableHead>
                    <TableHead>大小</TableHead>
                    <TableHead>平台</TableHead>
                    <TableHead>上传时间</TableHead>
                    <TableHead className="text-right">操作</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {artifacts.map((artifact) => (
                    <TableRow key={artifact.id}>
                      <TableCell className="font-mono text-sm">
                        {artifact.file_name}
                      </TableCell>
                      <TableCell>{formatFileSize(artifact.file_size)}</TableCell>
                      <TableCell>{artifact.platform || "-"}</TableCell>
                      <TableCell>{formatDate(artifact.uploaded_at)}</TableCell>
                      <TableCell className="text-right">
                        <div className="flex justify-end gap-1">
                          <Button variant="ghost" size="sm" render={
                            <a
                              href={artifactApi.downloadUrl(artifact.id)}
                              download
                            />
                          }>
                              <Download className="h-4 w-4" />
                          </Button>
                          {isPending && (
                            <AlertDialog>
                              <AlertDialogTrigger render={<Button variant="ghost" size="sm" />}>
                                  <Trash2 className="h-4 w-4" />
                              </AlertDialogTrigger>
                              <AlertDialogContent>
                                <AlertDialogHeader>
                                  <AlertDialogTitle>
                                    删除安装包?
                                  </AlertDialogTitle>
                                  <AlertDialogDescription>
                                    确认删除 {artifact.file_name}？
                                  </AlertDialogDescription>
                                </AlertDialogHeader>
                                <AlertDialogFooter>
                                  <AlertDialogCancel>取消</AlertDialogCancel>
                                  <AlertDialogAction
                                    onClick={() =>
                                      handleDeleteArtifact(
                                        artifact.id,
                                        artifact.file_name,
                                      )
                                    }
                                  >
                                    确认删除
                                  </AlertDialogAction>
                                </AlertDialogFooter>
                              </AlertDialogContent>
                            </AlertDialog>
                          )}
                        </div>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </CardContent>
        </Card>

        {/* 错误日志 */}
        {version.error_log && (
          <Card className="border-destructive/50">
            <CardHeader>
              <CardTitle className="text-base text-destructive">
                错误日志
              </CardTitle>
            </CardHeader>
            <CardContent>
              <pre className="whitespace-pre-wrap text-sm text-destructive">
                {version.error_log}
              </pre>
            </CardContent>
          </Card>
        )}

        {/* 发货按钮 */}
        {isPending && (
          <div className="flex justify-center pt-2">
            <Button
              size="lg"
              onClick={() => setShipDialogOpen(true)}
              disabled={shipVersion.isPending}
            >
              <Rocket className="mr-2 h-4 w-4" />
              {shipVersion.isPending ? "发货中..." : "发货到 GitHub"}
            </Button>
          </div>
        )}

        {/* 发货确认对话框 */}
        <Dialog open={shipDialogOpen} onOpenChange={setShipDialogOpen}>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>确认发货</DialogTitle>
              <DialogDescription>
                将 {version.version_number} 发布到 GitHub Release
              </DialogDescription>
            </DialogHeader>
            <div className="space-y-2 py-2">
              {shipChecks.map((check) => (
                <div key={check.label} className="flex items-center gap-2 text-sm">
                  <span>{check.ok ? "✅" : "❌"}</span>
                  <span className={check.ok ? "" : "text-destructive"}>
                    {check.label}
                  </span>
                </div>
              ))}
            </div>
            {!canShip && (
              <p className="text-sm text-destructive">
                请补充上述缺失项后再发货
              </p>
            )}
            <DialogFooter>
              <Button
                variant="outline"
                onClick={() => setShipDialogOpen(false)}
              >
                取消
              </Button>
              <Button onClick={handleShip} disabled={!canShip}>
                <Rocket className="mr-2 h-4 w-4" />
                确认发货
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </div>
    </>
  );
}
