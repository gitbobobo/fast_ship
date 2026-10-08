import { useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import {
  ChevronLeft,
  ChevronRight,
  Columns2,
  Images,
  MoreHorizontal,
  Pencil,
  Trash2,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Dialog,
  DialogContent,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import {
  useDeleteScreenshotScreen,
  useDeleteScreenshotVersion,
  useScreenshotScreen,
} from "@/lib/hooks/use-screenshots";
import { screenshotApi } from "@/lib/api/screenshots";
import { screenDisplayName } from "@/lib/screenshots";
import { formatDate, formatFileSize, formatRelativeTime } from "@/lib/utils/format";
import { cn } from "@/lib/utils";
import { ScreenshotEditDialog } from "./screenshot-edit-dialog";

interface ScreenshotLightboxProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  projectId: string;
  /** 当前分组过滤后的导航列表，左右键/箭头在此范围内切换 */
  screens: ScreenshotScreenListItem[];
  screenId: string | null;
  onNavigate: (screenId: string) => void;
}

function versionLabel(version: ScreenshotVersion): string {
  const stamp = formatDate(version.uploaded_at);
  return version.note ? `${version.note} · ${stamp}` : stamp;
}

function versionOptionLabel(
  version: ScreenshotVersion,
  index: number,
): string {
  return `${index === 0 ? "最新 · " : ""}${versionLabel(version)}`;
}

function VersionSelect({
  versions,
  value,
  onChange,
  className,
}: {
  versions: ScreenshotVersion[];
  value: string;
  onChange: (versionId: string) => void;
  className?: string;
}) {
  return (
    <Select
      value={value || undefined}
      onValueChange={(v) => {
        if (v) onChange(v);
      }}
    >
      <SelectTrigger className={cn("w-56", className)} aria-label="选择版本">
        {/* 下拉收起时 Item 已卸载，Value 需用函数按 value 查回 label */}
        <SelectValue placeholder="选择版本">
          {(selectedId) => {
            const index = versions.findIndex((v) => v.id === selectedId);
            return index >= 0
              ? versionOptionLabel(versions[index], index)
              : "";
          }}
        </SelectValue>
      </SelectTrigger>
      <SelectContent>
        {versions.map((version, index) => (
          <SelectItem
            key={version.id}
            value={version.id}
            label={versionOptionLabel(version, index)}
          >
            {index === 0 ? "最新 · " : ""}
            {versionLabel(version)}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

function ComparePane({
  side,
  versions,
  selectedId,
  onSelect,
}: {
  side: "left" | "right";
  versions: ScreenshotVersion[];
  selectedId: string;
  onSelect: (versionId: string) => void;
}) {
  const version =
    versions.find((v) => v.id === selectedId) ?? versions[0] ?? null;
  return (
    <div className="flex min-h-0 min-w-0 flex-col gap-2">
      <div className="flex items-center gap-2">
        <Badge variant="outline">{side === "left" ? "左" : "右"}</Badge>
        <VersionSelect
          versions={versions}
          value={version?.id ?? ""}
          onChange={onSelect}
          className="w-full flex-1"
        />
      </div>
      <div className="flex min-h-0 flex-1 items-center justify-center overflow-hidden rounded-md bg-muted/30">
        {version ? (
          <img
            src={screenshotApi.contentUrl(version)}
            alt={version.file_name}
            className="max-h-full max-w-full object-contain"
          />
        ) : (
          <Images className="h-10 w-10 text-muted-foreground/40" />
        )}
      </div>
      {version && (
        <p className="truncate text-xs text-muted-foreground">
          {version.file_name} · {formatFileSize(version.file_size)} ·{" "}
          {formatRelativeTime(version.uploaded_at)}
        </p>
      )}
    </div>
  );
}

export function ScreenshotLightbox({
  open,
  onOpenChange,
  projectId,
  screens,
  screenId,
  onNavigate,
}: ScreenshotLightboxProps) {
  const {
    data: detail,
    isLoading,
    isError,
  } = useScreenshotScreen(open && screenId ? screenId : "");
  const deleteScreen = useDeleteScreenshotScreen(projectId);
  const deleteVersion = useDeleteScreenshotVersion(projectId);

  const versions = detail?.versions ?? [];
  const listItem = screens.find((s) => s.id === screenId) ?? null;
  const screen = detail ?? listItem;
  const displayName = screen ? screenDisplayName(screen) : "";

  const [selectedVersionId, setSelectedVersionId] = useState<string | null>(
    null,
  );
  const [comparing, setComparing] = useState(false);
  const [leftVersionId, setLeftVersionId] = useState<string | null>(null);
  const [rightVersionId, setRightVersionId] = useState<string | null>(null);
  const [editOpen, setEditOpen] = useState(false);
  const [confirm, setConfirm] = useState<"version" | "screen" | null>(null);

  // 切换 screen 时重置版本选择与对比模式（render 期间调整 state，避免闪旧图）
  const [prevScreenId, setPrevScreenId] = useState(screenId);
  if (screenId !== prevScreenId) {
    setPrevScreenId(screenId);
    setSelectedVersionId(null);
    setComparing(false);
    setLeftVersionId(null);
    setRightVersionId(null);
  }

  const currentVersion =
    versions.find((v) => v.id === selectedVersionId) ?? versions[0] ?? null;

  const navIndex = screens.findIndex((s) => s.id === screenId);
  const canPrev = navIndex > 0;
  const canNext = navIndex >= 0 && navIndex < screens.length - 1;

  const goPrev = useCallback(() => {
    if (canPrev) onNavigate(screens[navIndex - 1].id);
  }, [canPrev, navIndex, screens, onNavigate]);
  const goNext = useCallback(() => {
    if (canNext) onNavigate(screens[navIndex + 1].id);
  }, [canNext, navIndex, screens, onNavigate]);

  useEffect(() => {
    if (!open || comparing) return;
    const onKeyDown = (e: KeyboardEvent) => {
      const target = e.target as HTMLElement | null;
      if (target?.closest("input, textarea, select, [contenteditable]")) {
        return;
      }
      // Select 下拉/更多菜单/删除确认打开时让位给浮层自己的方向键处理
      if (
        document.querySelector(
          '[role="listbox"], [role="menu"], [role="alertdialog"]',
        )
      ) {
        return;
      }
      // 编辑对话框叠在 lightbox（也是 dialog）上时不导航
      if (document.querySelectorAll('[role="dialog"]').length > 1) {
        return;
      }
      if (e.key === "ArrowLeft") {
        e.preventDefault();
        goPrev();
      } else if (e.key === "ArrowRight") {
        e.preventDefault();
        goNext();
      }
    };
    // capture 阶段：Base UI 组件的 keydown 处理会截断 bubble 阶段事件
    document.addEventListener("keydown", onKeyDown, true);
    return () => document.removeEventListener("keydown", onKeyDown, true);
  }, [open, comparing, goPrev, goNext]);

  useEffect(() => {
    if (open && screenId && isError) {
      toast.error("截图界面不存在或已被删除");
      onOpenChange(false);
    }
  }, [open, screenId, isError, onOpenChange]);

  const enterCompare = () => {
    // 默认左=最新、右=次新
    setLeftVersionId(versions[0]?.id ?? null);
    setRightVersionId((versions[1] ?? versions[0])?.id ?? null);
    setComparing(true);
  };

  const handleDeleteVersion = async () => {
    if (!currentVersion || !screenId) return;
    const lastOne = versions.length <= 1;
    try {
      await deleteVersion.mutateAsync({
        versionId: currentVersion.id,
        screenId,
      });
      toast.success("已删除该版本");
      // 最后一个版本被删会连带删除 screen
      if (lastOne) onOpenChange(false);
    } catch {
      toast.error("删除失败，请稍后重试");
    }
  };

  const handleDeleteScreen = async () => {
    if (!screenId) return;
    try {
      await deleteScreen.mutateAsync(screenId);
      toast.success("已删除界面及其全部版本");
      onOpenChange(false);
    } catch {
      toast.error("删除失败，请稍后重试");
    }
  };

  return (
    <>
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent className="flex h-[85vh] w-full max-w-[calc(100%-2rem)] flex-col gap-0 overflow-hidden p-0 sm:max-w-5xl">
          <div className="flex flex-wrap items-center gap-2 border-b px-4 py-3 pr-12">
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2">
                <DialogTitle className="truncate text-sm">
                  {displayName}
                </DialogTitle>
                {screen && (
                  <Badge variant="secondary">{screen.version_count} 个版本</Badge>
                )}
                {screen?.group && (
                  <Badge variant="outline">{screen.group}</Badge>
                )}
              </div>
              {screen && screen.title && (
                <p className="truncate text-xs text-muted-foreground">
                  {screen.screen_key}
                </p>
              )}
            </div>

            {!comparing && versions.length > 0 && (
              <VersionSelect
                versions={versions}
                value={currentVersion?.id ?? ""}
                onChange={setSelectedVersionId}
              />
            )}
            <Button
              variant={comparing ? "default" : "outline"}
              size="sm"
              onClick={() => (comparing ? setComparing(false) : enterCompare())}
              disabled={versions.length === 0}
            >
              <Columns2 className="mr-1.5 h-3.5 w-3.5" />
              {comparing ? "退出对比" : "对比"}
            </Button>
            <Button
              variant="outline"
              size="icon-sm"
              aria-label="编辑界面"
              disabled={!detail}
              onClick={() => setEditOpen(true)}
            >
              <Pencil className="h-3.5 w-3.5" />
            </Button>
            <DropdownMenu>
              <DropdownMenuTrigger
                render={
                  <Button
                    variant="outline"
                    size="icon-sm"
                    aria-label="更多操作"
                  />
                }
              >
                <MoreHorizontal className="h-3.5 w-3.5" />
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem
                  variant="destructive"
                  disabled={!currentVersion || comparing}
                  onClick={() => setConfirm("version")}
                >
                  <Trash2 className="mr-2 h-4 w-4" />
                  删除当前版本
                </DropdownMenuItem>
                <DropdownMenuItem
                  variant="destructive"
                  onClick={() => setConfirm("screen")}
                >
                  <Trash2 className="mr-2 h-4 w-4" />
                  删除界面
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>

          <div className="relative min-h-0 flex-1">
            {isLoading ? (
              <div className="flex h-full items-center justify-center p-4">
                <Skeleton className="h-full w-full" />
              </div>
            ) : comparing ? (
              <div className="grid h-full grid-cols-1 gap-3 overflow-y-auto p-4 sm:grid-cols-2">
                <ComparePane
                  side="left"
                  versions={versions}
                  selectedId={leftVersionId ?? ""}
                  onSelect={setLeftVersionId}
                />
                <ComparePane
                  side="right"
                  versions={versions}
                  selectedId={rightVersionId ?? ""}
                  onSelect={setRightVersionId}
                />
              </div>
            ) : currentVersion ? (
              <>
                <div className="flex h-full items-center justify-center bg-muted/30 p-4">
                  <img
                    src={screenshotApi.contentUrl(currentVersion)}
                    alt={currentVersion.file_name}
                    className="max-h-full max-w-full object-contain"
                  />
                </div>
                {navIndex >= 0 && screens.length > 1 && (
                  <>
                    <Button
                      variant="outline"
                      size="icon-sm"
                      className="absolute top-1/2 left-3 -translate-y-1/2"
                      aria-label="上一个界面"
                      disabled={!canPrev}
                      onClick={goPrev}
                    >
                      <ChevronLeft className="h-4 w-4" />
                    </Button>
                    <Button
                      variant="outline"
                      size="icon-sm"
                      className="absolute top-1/2 right-3 -translate-y-1/2"
                      aria-label="下一个界面"
                      disabled={!canNext}
                      onClick={goNext}
                    >
                      <ChevronRight className="h-4 w-4" />
                    </Button>
                  </>
                )}
              </>
            ) : (
              <div className="flex h-full flex-col items-center justify-center gap-2 text-muted-foreground">
                <Images className="h-10 w-10 text-muted-foreground/40" />
                <p className="text-sm">暂无截图版本</p>
              </div>
            )}
          </div>

          {!comparing && currentVersion && (
            <div className="space-y-2 border-t px-4 py-2">
              <p className="truncate text-xs text-muted-foreground">
                {currentVersion.note && `${currentVersion.note} · `}
                {currentVersion.file_name} ·{" "}
                {formatFileSize(currentVersion.file_size)} ·{" "}
                {formatRelativeTime(currentVersion.uploaded_at)}
              </p>
              {versions.length > 1 && (
                <div className="flex gap-2 overflow-x-auto pb-1">
                  {versions.map((version) => (
                    <button
                      key={version.id}
                      type="button"
                      className={cn(
                        "h-14 w-20 shrink-0 overflow-hidden rounded-md border bg-muted/30 transition-colors",
                        version.id === currentVersion.id
                          ? "border-primary ring-2 ring-primary/40"
                          : "border-transparent hover:border-primary/40",
                      )}
                      onClick={() => setSelectedVersionId(version.id)}
                      aria-label={versionLabel(version)}
                    >
                      <img
                        src={screenshotApi.contentUrl(version)}
                        alt={version.file_name}
                        className="h-full w-full object-cover"
                        loading="lazy"
                      />
                    </button>
                  ))}
                </div>
              )}
            </div>
          )}
        </DialogContent>
      </Dialog>

      {detail && (
        <ScreenshotEditDialog
          open={editOpen}
          onOpenChange={setEditOpen}
          projectId={projectId}
          screen={detail}
        />
      )}

      <AlertDialog
        open={confirm === "version"}
        onOpenChange={(o) => {
          if (!o) setConfirm(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>删除当前版本？</AlertDialogTitle>
            <AlertDialogDescription>
              {versions.length <= 1
                ? "这是该界面的最后一个版本，删除后界面将一并移除，不可恢复。"
                : "删除后不可恢复。"}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>取消</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                setConfirm(null);
                void handleDeleteVersion();
              }}
            >
              确认删除
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog
        open={confirm === "screen"}
        onOpenChange={(o) => {
          if (!o) setConfirm(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>删除界面？</AlertDialogTitle>
            <AlertDialogDescription>
              将删除「{displayName}」及其全部 {versions.length} 个版本，不可恢复。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>取消</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                setConfirm(null);
                void handleDeleteScreen();
              }}
            >
              确认删除
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
