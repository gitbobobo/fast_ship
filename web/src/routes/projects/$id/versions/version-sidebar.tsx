import {
  Rocket,
  ExternalLink,
  Trash2,
  Pencil,
  Tag,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Card,
  CardAction,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
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
import { formatDate } from "@/lib/utils/format";
import { VersionShipProgressCard } from "./version-ship-progress-card";

interface VersionSidebarProps {
  version: Version;
  isEditable: boolean;
  isPending: boolean;
  isShipping: boolean;
  editingVersionNumber: boolean;
  versionNumber: string;
  setVersionNumber: (v: string) => void;
  setEditingVersionNumber: (v: boolean) => void;
  onSaveVersionNumber: () => void;
  branchesData?: ProjectBranchesResponse | null;
  branchesLoading: boolean;
  branchesError: boolean;
  refetchBranches: () => void;
  targetCommitish: string | null;
  onUpdateTargetCommitish: (branch: string) => void;
  onDeleteVersion: () => void;
  onShipDialogOpen: () => void;
}

export function VersionSidebar({
  version,
  isEditable,
  isPending,
  isShipping,
  editingVersionNumber,
  versionNumber,
  setVersionNumber,
  setEditingVersionNumber,
  onSaveVersionNumber,
  branchesData,
  branchesLoading,
  branchesError,
  refetchBranches,
  targetCommitish,
  onUpdateTargetCommitish,
  onDeleteVersion,
  onShipDialogOpen,
}: VersionSidebarProps) {
  const branchOptions = branchesData?.branches ?? [];
  const targetBranchExists =
    !targetCommitish ||
    branchOptions.some((branch) => branch.name === targetCommitish);

  return (
    <div className="space-y-6" data-testid="version-sidebar">
      {/* 基本信息 */}
      <Card className="shadow-md">
        <CardHeader className="items-center gap-3 space-y-0 border-b px-5 pb-4 pt-5">
          <div className="flex items-center gap-2.5">
            <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-primary/10">
              <Tag className="h-4 w-4 text-primary" />
            </div>
            <CardTitle className="text-base">基本信息</CardTitle>
          </div>
          {(version.github_release_url || isEditable || isPending) && (
          <CardAction className="flex flex-wrap items-center justify-end gap-2">
            {version.github_release_url && (
              <Button
                variant="outline"
                size="sm"
                className="h-7 px-2 text-xs shadow-xs"
                render={
                  <a
                    href={version.github_release_url}
                    target="_blank"
                    rel="noopener noreferrer"
                  />
                }
              >
                <ExternalLink className="mr-1 h-3 w-3" />
                GitHub
              </Button>
            )}
            {isEditable && (
              <AlertDialog>
                <AlertDialogTrigger
                  render={<Button variant="outline" size="sm" className="h-7 px-2 text-xs shadow-xs" />}
                >
                  <Trash2 className="mr-1 h-3 w-3" />
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
                    <AlertDialogAction onClick={onDeleteVersion}>
                      确认删除
                    </AlertDialogAction>
                  </AlertDialogFooter>
                </AlertDialogContent>
              </AlertDialog>
            )}
            {isPending && (
              <Button
                size="sm"
                className="h-7 px-2 text-xs shadow-xs bg-gradient-to-r from-primary to-primary/80 hover:from-primary/90 hover:to-primary/70"
                onClick={onShipDialogOpen}
                disabled={isShipping}
              >
                <Rocket className="mr-1 h-3 w-3" />
                {isShipping ? "发货中" : "发货"}
              </Button>
            )}
          </CardAction>
          )}
        </CardHeader>
        <CardContent className="space-y-3 px-5 pb-4 pt-3">
          {/* 版本号 */}
          <div className="space-y-1">
            <span className="text-xs font-medium text-muted-foreground">
              版本号
            </span>
            {editingVersionNumber ? (
              <div className="space-y-2">
                <Input
                  className="h-8 text-sm"
                  value={versionNumber}
                  onChange={(e) => setVersionNumber(e.target.value)}
                />
                <div className="flex gap-2">
                  <Button
                    size="sm"
                    variant="ghost"
                    className="h-7 text-xs"
                    onClick={onSaveVersionNumber}
                  >
                    保存
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    className="h-7 text-xs"
                    onClick={() => setEditingVersionNumber(false)}
                  >
                    取消
                  </Button>
                </div>
              </div>
            ) : (
              <div className="flex items-center justify-between">
                <span className="font-mono text-sm">
                  {version.version_number}
                </span>
                {isEditable && (
                  <Button
                    variant="ghost"
                    size="icon-xs"
                    onClick={() => {
                      setVersionNumber(version.version_number);
                      setEditingVersionNumber(true);
                    }}
                  >
                    <Pencil className="h-3 w-3" />
                  </Button>
                )}
              </div>
            )}
          </div>

          {/* 状态 */}
          <div className="space-y-1">
            <span className="text-xs font-medium text-muted-foreground">
              状态
            </span>
            <div>
              <Badge
                variant={
                  version.status === "shipped" ? "default" : "secondary"
                }
              >
                {version.status === "shipped" ? "已发货" : "待发货"}
              </Badge>
            </div>
          </div>

          {/* 目标分支 */}
          {isEditable && (
            <div className="space-y-1">
              <span className="text-xs font-medium text-muted-foreground">
                目标分支
              </span>
              <Select
                value={targetCommitish || ""}
                onValueChange={(v) => v && onUpdateTargetCommitish(v)}
                disabled={branchesLoading || branchesError}
              >
                <SelectTrigger className="h-8 text-sm" size="sm">
                  <SelectValue placeholder={targetCommitish || "选择分支"} />
                </SelectTrigger>
                <SelectContent>
                  {branchOptions.map((branch) => (
                    <SelectItem key={branch.name} value={branch.name}>
                      {branch.name}
                      {branch.default && " (默认)"}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {branchesError && (
                <div className="flex items-center justify-between gap-2 rounded-md border border-destructive/30 px-2 py-1.5">
                  <span className="text-xs text-destructive">
                    分支加载失败
                  </span>
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    className="h-6 px-2 text-xs"
                    onClick={() => void refetchBranches()}
                  >
                    重试
                  </Button>
                </div>
              )}
              {!branchesLoading && !branchesError && !targetBranchExists && (
                <p className="text-xs text-destructive">
                  当前目标分支 {targetCommitish} 不存在，请重新选择
                </p>
              )}
              {!branchesLoading && !branchesError && branchOptions.length === 0 && (
                <p className="text-xs text-muted-foreground">
                  当前仓库没有可选分支
                </p>
              )}
            </div>
          )}
          {!isEditable && targetCommitish && (
            <div className="space-y-1">
              <span className="text-xs font-medium text-muted-foreground">
                目标分支
              </span>
              <p className="font-mono text-sm">{targetCommitish}</p>
            </div>
          )}

          {/* 创建时间 */}
          <div className="space-y-1 border-t border-border pt-3">
            <span className="text-xs font-medium text-muted-foreground">
              创建时间
            </span>
            <p className="text-sm leading-snug">{formatDate(version.created_at)}</p>
          </div>
          {version.shipped_at && (
            <div className="space-y-1 border-t border-border pt-3">
              <span className="text-xs font-medium text-muted-foreground">
                发货时间
              </span>
              <p className="text-sm leading-snug">{formatDate(version.shipped_at)}</p>
            </div>
          )}
        </CardContent>
      </Card>

      <VersionShipProgressCard
        version={version}
        isPending={isPending}
        isShipping={isShipping}
      />
    </div>
  );
}
