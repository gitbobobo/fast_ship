import { useParams, useNavigate, Link } from "react-router";
import { useEffect, useRef, useState } from "react";
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
  useShipCheck,
  useShipVersion,
} from "@/lib/hooks/use-versions";
import { useUploadArtifact, useDeleteArtifact } from "@/lib/hooks/use-artifacts";
import { artifactApi } from "@/lib/api/artifacts";
import { formatDate, formatFileSize } from "@/lib/utils/format";
import { cn } from "@/lib/utils";
import { versionSchema } from "@/lib/utils/validators";
import { toast } from "sonner";

interface UploadProgressState {
  currentFileName: string;
  currentFileIndex: number;
  totalFiles: number;
  completedFiles: number;
  failedFiles: number;
  percent: number;
  status: "uploading" | "completed" | "failed";
}

export default function VersionDetailPage() {
  const { id, vid } = useParams();
  const navigate = useNavigate();
  const {
    data: version,
    isLoading,
    refetch: refetchVersion,
  } = useVersion(vid!);
  const updateVersion = useUpdateVersion(vid!, id);
  const deleteVersion = useDeleteVersion(id!);
  const shipVersion = useShipVersion(vid!, id);
  const [shipDialogOpen, setShipDialogOpen] = useState(false);
  const [shipFailureDialogOpen, setShipFailureDialogOpen] = useState(false);
  const [shipFailureMessage, setShipFailureMessage] = useState("");
  const {
    data: shipCheck,
    isLoading: shipCheckLoading,
    refetch: refetchShipCheck,
  } = useShipCheck(vid!, shipDialogOpen);
  const uploadArtifact = useUploadArtifact(vid!);
  const deleteArtifact = useDeleteArtifact(vid!);

  const [editingNotes, setEditingNotes] = useState(false);
  const [notes, setNotes] = useState("");
  const [editingVersionNumber, setEditingVersionNumber] = useState(false);
  const [versionNumber, setVersionNumber] = useState("");
  const [editingCommitish, setEditingCommitish] = useState(false);
  const [commitish, setCommitish] = useState("");
  const [uploadPlatform, setUploadPlatform] = useState("");
  const [isDragOver, setIsDragOver] = useState(false);
  const [uploadProgress, setUploadProgress] = useState<UploadProgressState | null>(
    null,
  );
  const fileInputRef = useRef<HTMLInputElement>(null);

  const isPending = version?.status === "pending";
  const isShipping =
    shipVersion.isPending || version?.ship_status === "in_progress";
  const shouldPollShip = Boolean(vid && isShipping);
  const isUploading = uploadProgress?.status === "uploading";
  const isEditable = isPending && !isShipping;
  const artifacts = version?.artifacts ?? [];

  useEffect(() => {
    if (!shouldPollShip || !vid) return;

    const timer = window.setInterval(() => {
      void refetchVersion();
    }, 1000);

    return () => window.clearInterval(timer);
  }, [refetchVersion, shouldPollShip, vid]);

  const handleSaveNotes = async () => {
    try {
      await updateVersion.mutateAsync({ release_notes: notes });
      setEditingNotes(false);
      toast.success("Release 说明已更新");
    } catch {
      toast.error("更新失败");
    }
  };

  const handleSaveVersionNumber = async () => {
    const parsed = versionSchema.shape.version_number.safeParse(versionNumber);
    if (!parsed.success) {
      toast.error(parsed.error.issues[0]?.message ?? "版本号格式无效");
      return;
    }

    try {
      await updateVersion.mutateAsync({ version_number: parsed.data });
      setEditingVersionNumber(false);
      toast.success("版本号已更新");
    } catch {
      toast.error("更新失败，版本号可能已存在");
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

  const handleUploadFiles = async (files: File[]) => {
    if (!files.length || isUploading) return;

    let completedFiles = 0;
    let failedFiles = 0;

    for (const [index, file] of files.entries()) {
      setUploadProgress({
        currentFileName: file.name,
        currentFileIndex: index + 1,
        totalFiles: files.length,
        completedFiles,
        failedFiles,
        percent: 0,
        status: "uploading",
      });

      const formData = new FormData();
      formData.append("file", file);
      if (uploadPlatform.trim()) {
        formData.append("platform", uploadPlatform.trim());
      }

      try {
        await uploadArtifact.mutateAsync({
          formData,
          onProgress: (percent) => {
            setUploadProgress((current) => {
              if (!current) return current;
              return {
                ...current,
                currentFileName: file.name,
                currentFileIndex: index + 1,
                percent,
              };
            });
          },
        });
        completedFiles += 1;
        toast.success(`${file.name} 上传成功`);
      } catch {
        failedFiles += 1;
        toast.error(`${file.name} 上传失败`);
      }
    }

    setUploadProgress({
      currentFileName: files.at(-1)?.name || "",
      currentFileIndex: files.length,
      totalFiles: files.length,
      completedFiles,
      failedFiles,
      percent: 100,
      status: failedFiles > 0 ? "failed" : "completed",
    });

    if (fileInputRef.current) {
      fileInputRef.current.value = "";
    }
  };

  const handleUpload = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const files = e.target.files;
    if (!files?.length) return;

    await handleUploadFiles(Array.from(files));
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
    const checkResult = await refetchShipCheck();
    if (!checkResult.data?.can_ship) {
      toast.error("发货校验未通过");
      return;
    }

    setShipDialogOpen(false);
    try {
      await shipVersion.mutateAsync();
      toast.success("发货成功！");
      await refetchVersion();
    } catch {
      const result = await refetchVersion();
      const failureMessage =
        result.data?.error_log ||
        result.data?.ship_message ||
        "发货失败，请修复后重试";
      setShipFailureMessage(failureMessage);
      setShipFailureDialogOpen(true);
      toast.error("发货失败");
    }
  };

  const shipChecks = shipCheck?.items ?? [];
  const canShip = shipCheck?.can_ship ?? false;
  const shipSteps = [
    { key: "precheck", label: "发货前校验" },
    { key: "create_tag", label: "创建 Git Tag" },
    { key: "create_release", label: "创建 GitHub Release" },
    { key: "upload_assets", label: "上传安装包" },
    { key: "finalize", label: "更新版本状态" },
  ] as const;

  const currentShipStepIndex = shipSteps.findIndex(
    (step) => step.key === version?.ship_stage,
  );

  const getShipStepState = (index: number) => {
    if (version?.status === "shipped" || version?.ship_status === "completed") {
      return "done";
    }
    if (version?.ship_status === "failed") {
      if (index < currentShipStepIndex) return "done";
      if (index === currentShipStepIndex) return "failed";
      return "todo";
    }
    if (version?.ship_status === "in_progress") {
      if (index < currentShipStepIndex) return "done";
      if (index === currentShipStepIndex) return "doing";
      return "todo";
    }
    return "todo";
  };

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
            {isEditable && (
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
              {editingVersionNumber ? (
                <span className="inline-flex items-center gap-2">
                  <Input
                    className="h-7 w-40 text-sm"
                    value={versionNumber}
                    onChange={(e) => setVersionNumber(e.target.value)}
                  />
                  <Button
                    size="sm"
                    variant="ghost"
                    className="h-7"
                    onClick={handleSaveVersionNumber}
                  >
                    保存
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    className="h-7"
                    onClick={() => setEditingVersionNumber(false)}
                  >
                    取消
                  </Button>
                </span>
              ) : (
                <span>
                  {version.version_number}
                  {isEditable && (
                    <Button
                      variant="ghost"
                      size="sm"
                      className="ml-2 h-6 px-2"
                      onClick={() => {
                        setVersionNumber(version.version_number);
                        setEditingVersionNumber(true);
                      }}
                    >
                      <Pencil className="h-3 w-3" />
                    </Button>
                  )}
                </span>
              )}
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
                  {isEditable && (
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
            {isEditable && !editingNotes && (
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
            {isEditable && (
              <>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => fileInputRef.current?.click()}
                  disabled={isUploading}
                >
                  <Upload className="mr-1.5 h-3.5 w-3.5" />
                  {isUploading ? "上传中..." : "上传文件"}
                </Button>
                <input
                  ref={fileInputRef}
                  type="file"
                  multiple
                  className="hidden"
                  onChange={handleUpload}
                  disabled={isUploading}
                />
              </>
            )}
          </CardHeader>
          <CardContent>
            {isEditable && (
              <div className="mb-4 space-y-4">
                <div className="flex flex-col gap-2 sm:max-w-xs">
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
                <div
                  role="button"
                  tabIndex={0}
                  className={cn(
                    "rounded-lg border border-dashed px-4 py-8 text-center transition-colors",
                    isUploading
                      ? "cursor-not-allowed border-muted-foreground/30 bg-muted/40"
                      : isDragOver
                        ? "border-primary bg-primary/5"
                        : "border-muted-foreground/30 hover:border-primary/50 hover:bg-muted/30",
                  )}
                  onClick={() => {
                    if (isUploading) return;
                    fileInputRef.current?.click();
                  }}
                  onKeyDown={(e) => {
                    if (isUploading) return;
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      fileInputRef.current?.click();
                    }
                  }}
                  onDragEnter={(e) => {
                    e.preventDefault();
                    if (!isUploading) setIsDragOver(true);
                  }}
                  onDragOver={(e) => {
                    e.preventDefault();
                    if (!isUploading) setIsDragOver(true);
                  }}
                  onDragLeave={(e) => {
                    e.preventDefault();
                    const nextTarget = e.relatedTarget;
                    if (!e.currentTarget.contains(nextTarget as Node | null)) {
                      setIsDragOver(false);
                    }
                  }}
                  onDrop={(e) => {
                    e.preventDefault();
                    setIsDragOver(false);
                    if (isUploading) return;
                    const droppedFiles = Array.from(e.dataTransfer.files || []);
                    void handleUploadFiles(droppedFiles);
                  }}
                >
                  <Upload className="mx-auto mb-3 h-8 w-8 text-muted-foreground" />
                  <p className="text-sm font-medium">
                    拖拽文件到这里，或点击选择安装包
                  </p>
                  <p className="mt-1 text-xs text-muted-foreground">
                    支持多文件上传，上传期间会禁止重复提交
                  </p>
                </div>
                {uploadProgress && (
                  <div className="rounded-lg border px-4 py-3">
                    <div className="mb-2 flex items-center justify-between gap-3 text-sm">
                      <div className="min-w-0">
                        <p className="truncate font-medium">
                          {uploadProgress.currentFileName}
                        </p>
                        <p className="text-xs text-muted-foreground">
                          {uploadProgress.status === "uploading"
                            ? `正在上传第 ${uploadProgress.currentFileIndex}/${uploadProgress.totalFiles} 个文件`
                            : uploadProgress.status === "completed"
                              ? `上传完成，共 ${uploadProgress.completedFiles} 个文件`
                              : `上传结束，成功 ${uploadProgress.completedFiles} 个，失败 ${uploadProgress.failedFiles} 个`}
                        </p>
                      </div>
                      <span className="shrink-0 text-sm font-medium">
                        {uploadProgress.percent}%
                      </span>
                    </div>
                    <div className="h-2 overflow-hidden rounded-full bg-muted">
                      <div
                        className={cn(
                          "h-full transition-all",
                          uploadProgress.status === "failed"
                            ? "bg-destructive"
                            : "bg-primary",
                        )}
                        style={{ width: `${uploadProgress.percent}%` }}
                      />
                    </div>
                  </div>
                )}
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
                      <TableHead>上传者</TableHead>
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
                      <TableCell>{artifact.uploaded_by || "-"}</TableCell>
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
                          {isEditable && (
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

        {/* 发货进度 */}
        {isPending &&
          (isShipping ||
            version.ship_status === "failed" ||
            version.ship_status === "completed") && (
            <Card>
              <CardHeader>
                <CardTitle className="text-base">发货进度</CardTitle>
              </CardHeader>
              <CardContent className="space-y-3">
                {shipSteps.map((step, index) => {
                  const state = getShipStepState(index);
                  return (
                    <div
                      key={step.key}
                      className="flex items-center justify-between rounded-md border px-3 py-2 text-sm"
                    >
                      <span>{step.label}</span>
                      <span
                        className={
                          state === "done"
                            ? "text-emerald-600"
                            : state === "doing"
                              ? "text-amber-600"
                              : state === "failed"
                                ? "text-destructive"
                                : "text-muted-foreground"
                        }
                      >
                        {state === "done"
                          ? "已完成"
                          : state === "doing"
                            ? "进行中"
                            : state === "failed"
                              ? "失败"
                              : "等待中"}
                      </span>
                    </div>
                  );
                })}
                {version.ship_message && (
                  <p className="text-sm text-muted-foreground">
                    {version.ship_message}
                  </p>
                )}
              </CardContent>
            </Card>
          )}

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
              disabled={isShipping}
            >
              <Rocket className="mr-2 h-4 w-4" />
              {isShipping ? "发货中..." : "发货到 GitHub"}
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
              {shipCheckLoading ? (
                <p className="text-sm text-muted-foreground">正在校验发货条件...</p>
              ) : (
                shipChecks.map((check) => (
                  <div
                    key={check.key}
                    className="flex items-start gap-2 rounded-md border px-3 py-2 text-sm"
                  >
                    <span>{check.ok ? "✅" : "❌"}</span>
                    <div className="space-y-0.5">
                      <p className={check.ok ? "" : "text-destructive"}>
                        {check.label}
                      </p>
                      {!check.ok && check.detail && (
                        <p className="text-xs text-muted-foreground">
                          {check.detail}
                        </p>
                      )}
                    </div>
                  </div>
                ))
              )}
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
                disabled={shipVersion.isPending}
              >
                取消
              </Button>
              <Button
                onClick={handleShip}
                disabled={!canShip || shipCheckLoading || shipVersion.isPending}
              >
                <Rocket className="mr-2 h-4 w-4" />
                确认发货
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>

        <Dialog
          open={shipFailureDialogOpen}
          onOpenChange={setShipFailureDialogOpen}
        >
          <DialogContent>
            <DialogHeader>
              <DialogTitle>发货失败</DialogTitle>
              <DialogDescription>
                GitHub 发货流程未完成，版本状态保持为待发货。
              </DialogDescription>
            </DialogHeader>
            <div className="rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm text-destructive">
              {shipFailureMessage}
            </div>
            <p className="text-sm text-muted-foreground">
              请根据失败原因修复配置或重新上传安装包后，再次发货。
            </p>
            <DialogFooter>
              <Button onClick={() => setShipFailureDialogOpen(false)}>
                我知道了
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </div>
    </>
  );
}
