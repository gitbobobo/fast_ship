import { useEffect, useState } from "react";
import { useParams, useNavigate } from "react-router";
import { Tag, Pencil, Eye, Rocket } from "lucide-react";
import { Header } from "@/components/layout/header";
import { HeaderActions } from "@/components/layout/header-actions";
import { GitHubContent } from "@/components/github-content";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import {
  Card,
  CardAction,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  useVersion,
  useUpdateVersion,
  useDeleteVersion,
  useShipCheck,
  useShipVersion,
} from "@/lib/hooks/use-versions";
import { useProjectBranches, useProject } from "@/lib/hooks/use-projects";
import { useUploadArtifact, useDeleteArtifact } from "@/lib/hooks/use-artifacts";
import { ensureGitHubLinked } from "@/lib/utils/github";
import { versionSchema } from "@/lib/utils/validators";
import { toast } from "sonner";
import { VersionArtifactsCard } from "./version-artifacts-card";
import { VersionShipSection } from "./version-ship-section";
import { VersionSidebar } from "./version-sidebar";

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
  const [uploadPlatform, setUploadPlatform] = useState("");
  const [uploadProgress, setUploadProgress] = useState<UploadProgressState | null>(null);
  const [isDragOver, setIsDragOver] = useState(false);

  const isPending = version?.status === "pending";
  const isShipping =
    shipVersion.isPending || version?.ship_status === "in_progress";
  const shouldPollShip = Boolean(vid && isShipping);
  const isUploading = uploadProgress?.status === "uploading";
  const isEditable = isPending && !isShipping;
  const artifacts = version?.artifacts ?? [];
  const { data: currentProject } = useProject(id!);
  const {
    data: branchesData,
    isLoading: branchesLoading,
    isError: branchesError,
    refetch: refetchBranches,
  } = useProjectBranches(id!, isEditable);

  useEffect(() => {
    if (!shouldPollShip || !vid) return;
    const interval = setInterval(() => {
      void refetchVersion();
    }, 3000);
    return () => clearInterval(interval);
  }, [shouldPollShip, vid, refetchVersion]);

  useEffect(() => {
    if (!isUploading) return;
    const interval = setInterval(() => {
      void refetchVersion();
    }, 2000);
    return () => clearInterval(interval);
  }, [isUploading, refetchVersion]);

  // ---- Handlers ----

  const handleSaveNotes = async () => {
    if (!vid) return;
    try {
      await updateVersion.mutateAsync({ release_notes: notes });
      setEditingNotes(false);
      toast.success("Release 说明已更新");
    } catch {
      toast.error("更新失败");
    }
  };

  const handleSaveVersionNumber = async () => {
    if (!vid) return;
    const result = versionSchema.safeParse({ version_number: versionNumber });
    if (!result.success) {
      toast.error(result.error.issues[0]?.message ?? "版本号格式无效");
      return;
    }
    try {
      await updateVersion.mutateAsync({ version_number: versionNumber });
      setEditingVersionNumber(false);
      toast.success("版本号已更新");
    } catch {
      toast.error("更新失败，版本号可能已存在");
    }
  };

  const handleUpload = (e: React.ChangeEvent<HTMLInputElement>) => {
    const files = Array.from(e.target.files || []);
    void handleUploadFiles(files);
    e.target.value = "";
  };

  const handleUploadFiles = async (files: File[]) => {
    if (files.length === 0) return;
    for (let i = 0; i < files.length; i++) {
      const file = files[i];
      setUploadProgress({
        currentFileName: file.name,
        currentFileIndex: i + 1,
        totalFiles: files.length,
        completedFiles: i,
        failedFiles: 0,
        percent: 0,
        status: "uploading",
      });
      try {
        const formData = new FormData();
        formData.append("file", file);
        if (uploadPlatform) formData.append("platform", uploadPlatform);
        await uploadArtifact.mutateAsync({
          formData,
          onProgress: (percent) => {
            setUploadProgress((prev) =>
              prev ? { ...prev, percent } : null,
            );
          },
        });
        setUploadProgress((prev) =>
          prev
            ? {
                ...prev,
                completedFiles: prev.completedFiles + 1,
                percent: 100,
              }
            : null,
        );
        toast.success(`${file.name} 上传成功`);
      } catch {
        setUploadProgress((prev) =>
          prev
            ? { ...prev, failedFiles: prev.failedFiles + 1, status: "failed" }
            : null,
        );
        toast.error(`${file.name} 上传失败`);
      }
    }
    setUploadProgress((prev) =>
      prev
        ? {
            ...prev,
            status: prev.failedFiles > 0 ? "failed" : "completed",
            percent: 100,
          }
        : null,
    );
    void refetchVersion();
    setTimeout(() => setUploadProgress(null), 2000);
  };

  const handleDeleteArtifact = async (artifactId: string, name: string) => {
    try {
      await deleteArtifact.mutateAsync(artifactId);
      toast.success(`${name} 已删除`);
    } catch {
      toast.error("删除失败");
    }
  };

  const handleDeleteVersion = async () => {
    if (!vid) return;
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
      const latestVersion = result.data;

      if (
        latestVersion?.status === "shipped" ||
        latestVersion?.ship_status === "completed"
      ) {
        toast.success("发货成功！");
        return;
      }

      if (latestVersion?.ship_status === "in_progress") {
        toast.success("发货已开始，正在同步最新进度");
        return;
      }

      const failureMessage =
        latestVersion?.error_log ||
        latestVersion?.ship_message ||
        "发货失败，请修复后重试";
      setShipFailureMessage(failureMessage);
      setShipFailureDialogOpen(true);
      toast.error("发货失败");
    }
  };

  const handleUpdateTargetCommitish = async (branch: string) => {
    if (!vid) return;
    try {
      await updateVersion.mutateAsync({ target_commitish: branch });
      toast.success("目标分支已更新");
    } catch {
      toast.error("更新失败");
    }
  };

  const shipChecks = shipCheck?.items ?? [];
  const canShip = shipCheck?.can_ship ?? false;

  // ---- Render ----

  if (isLoading) {
    return (
      <>
        <Header title="版本详情" />
        <div className="p-4 md:p-6 space-y-4">
          <div className="grid gap-4 lg:grid-cols-3">
            <Skeleton className="h-64 rounded-xl lg:col-span-2" />
            <Skeleton className="h-64 rounded-xl" />
          </div>
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
      <Header
        title={`版本 ${version.version_number}`}
        actions={
          isPending ? (
            <HeaderActions
              primary={
                <Button
                  size="sm"
                  onClick={() => {
                    if (ensureGitHubLinked(currentProject, "发货")) setShipDialogOpen(true);
                  }}
                  disabled={isShipping}
                >
                  <Rocket className="mr-1.5 h-3.5 w-3.5" />
                  {isShipping ? "发货中" : "发货"}
                </Button>
              }
            />
          ) : undefined
        }
      />
      <div className="mx-auto max-w-7xl p-4 md:p-6">

        {/* 主体两栏 */}
        <div className="grid gap-6 lg:grid-cols-[minmax(800px,1fr)_300px] xl:grid-cols-[minmax(800px,1fr)_340px]">
          {/* 左侧：主要内容 */}
          <div className="min-w-0 space-y-6">
            {/* Release 说明 */}
            <Card className="shadow-md hover:shadow-lg transition-shadow">
              <CardHeader className="items-center gap-3 space-y-0 border-b px-5 pb-4 pt-5">
                <div className="flex items-center gap-2.5">
                  <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-primary/10">
                    <Tag className="h-4 w-4 text-primary" />
                  </div>
                  <CardTitle className="text-base">Release 说明</CardTitle>
                </div>
                {isEditable && !editingNotes && (
                  <CardAction>
                    <Button
                      variant="outline"
                      size="sm"
                      className="shadow-sm"
                      onClick={() => {
                        setNotes(version.release_notes || "");
                        setEditingNotes(true);
                      }}
                    >
                      <Pencil className="mr-1.5 h-3.5 w-3.5" />
                      编辑
                    </Button>
                  </CardAction>
                )}
              </CardHeader>
              <CardContent className="space-y-4 px-5 pb-6 pt-5">
                {editingNotes ? (
                  <Tabs defaultValue="edit" className="flex flex-col gap-4">
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
                    <TabsContent value="edit" className="space-y-0">
                      <Textarea
                        value={notes}
                        onChange={(e) => setNotes(e.target.value)}
                        rows={10}
                        placeholder="支持 Markdown 格式"
                        className="min-h-[200px]"
                      />
                    </TabsContent>
                    <TabsContent value="preview" className="space-y-0">
                      <div className="rounded-md border p-4">
                        {notes ? (
                          <GitHubContent
                            markdown={notes}
                            className="markdown-body text-sm"
                          />
                        ) : (
                          <p className="text-muted-foreground">暂无内容</p>
                        )}
                      </div>
                    </TabsContent>
                    <div className="flex gap-2 pt-1">
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
                  <GitHubContent
                    markdown={version.release_notes}
                    className="markdown-body text-sm"
                  />
                ) : (
                  <div className="flex flex-col items-center justify-center gap-2 py-8 text-center">
                    <Tag className="h-8 w-8 text-muted-foreground/40" />
                    <p className="text-sm text-muted-foreground">
                      暂无 Release 说明
                    </p>
                    {isEditable && (
                      <p className="text-xs text-muted-foreground">
                        点击右上角编辑按钮添加说明
                      </p>
                    )}
                  </div>
                )}
              </CardContent>
            </Card>

            {/* 安装包 */}
            <VersionArtifactsCard
              artifacts={artifacts}
              isEditable={isEditable}
              isUploading={isUploading}
              uploadProgress={uploadProgress}
              isDragOver={isDragOver}
              uploadPlatform={uploadPlatform}
              setUploadPlatform={setUploadPlatform}
              setIsDragOver={setIsDragOver}
              onUpload={handleUpload}
              onUploadFiles={handleUploadFiles}
              onDeleteArtifact={handleDeleteArtifact}
            />
          </div>

          {/* 右侧：侧边栏 */}
          <VersionSidebar
            version={version}
            isEditable={isEditable}
            isPending={isPending}
            isShipping={isShipping}
            editingVersionNumber={editingVersionNumber}
            versionNumber={versionNumber}
            setVersionNumber={setVersionNumber}
            setEditingVersionNumber={setEditingVersionNumber}
            onSaveVersionNumber={handleSaveVersionNumber}
            branchesData={branchesData}
            branchesLoading={branchesLoading}
            branchesError={branchesError}
            refetchBranches={refetchBranches}
            targetCommitish={version.target_commitish}
            onUpdateTargetCommitish={handleUpdateTargetCommitish}
            onDeleteVersion={handleDeleteVersion}
          />
        </div>

        {/* 发货相关 UI：错误日志与对话框保留在页面级，进度卡片已移至 VersionSidebar */}
        <VersionShipSection
          version={version}
          isShipping={isShipping}
          shipChecks={shipChecks}
          canShip={canShip}
          shipCheckLoading={shipCheckLoading}
          shipDialogOpen={shipDialogOpen}
          setShipDialogOpen={setShipDialogOpen}
          shipFailureDialogOpen={shipFailureDialogOpen}
          setShipFailureDialogOpen={setShipFailureDialogOpen}
          shipFailureMessage={shipFailureMessage}
          onShip={handleShip}
        />
      </div>
    </>
  );
}
