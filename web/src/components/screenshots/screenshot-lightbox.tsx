import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { toast } from "sonner";
import { useQueryClient } from "@tanstack/react-query";
import {
  ChevronLeft,
  ChevronRight,
  Columns2,
  Images,
  Loader2,
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
  screenshotScreenDetailQueryOptions,
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

/** 放大态：只有「适配 / 放大」两档，放大后按此宽度渲染 */
interface ZoomState {
  /** 放大渲染宽度（px）；0 表示尺寸未知，退化为自然尺寸 */
  width: number;
  /** 点击点在适配图上归一化坐标，用于放大后对中 */
  u: number;
  v: number;
  /** 点击的视口坐标 */
  px: number;
  py: number;
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

/** 图片区两侧的全高翻页条：悬停整条高亮，到首尾时变暗不可点（不循环） */
function NavZone({
  side,
  disabled,
  onNavigate,
}: {
  side: "left" | "right";
  disabled: boolean;
  onNavigate: () => void;
}) {
  const Icon = side === "left" ? ChevronLeft : ChevronRight;
  return (
    <button
      type="button"
      aria-label={side === "left" ? "上一个界面" : "下一个界面"}
      disabled={disabled}
      onClick={onNavigate}
      className={cn(
        "absolute inset-y-0 z-10 flex w-16 items-center justify-center transition-colors sm:w-20",
        side === "left" ? "left-0" : "right-0",
        disabled
          ? "cursor-default opacity-40"
          : "cursor-pointer text-muted-foreground hover:bg-foreground/10 hover:text-foreground",
      )}
    >
      <span className="rounded-full bg-background/80 p-1.5 shadow-sm">
        <Icon className="h-5 w-5" />
      </span>
    </button>
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
      {/* relative z-20：控件行抬到两侧翻页区（z-10）之上，避免边缘点击被翻页区吃掉 */}
      <div className="relative z-20 flex items-center gap-2">
        <Badge variant="outline">{side === "left" ? "左" : "右"}</Badge>
        <VersionSelect
          versions={versions}
          value={version?.id ?? ""}
          onChange={onSelect}
          className="w-full flex-1"
        />
      </div>
      <div className="flex min-h-0 flex-1 items-center justify-center overflow-hidden bg-muted/30">
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

/**
 * 单图视图：默认 object-contain 适配；点击放大到原始像素 ÷ dpr（不低于
 * 适配尺寸 2 倍），以点击处为中心；放大后可滚动或鼠标拖拽，再点回到适配。
 */
function ZoomableImage({
  version,
  zoom,
  onZoomChange,
}: {
  version: ScreenshotVersion;
  zoom: ZoomState | null;
  onZoomChange: (zoom: ZoomState | null) => void;
}) {
  const scrollRef = useRef<HTMLDivElement>(null);
  const fitImgRef = useRef<HTMLImageElement>(null);
  const zoomImgRef = useRef<HTMLImageElement>(null);
  const dragRef = useRef({
    down: false,
    moved: false,
    downX: 0,
    downY: 0,
    lastX: 0,
    lastY: 0,
  });

  // 把点击点滚到视口对应位置，保持以点击处为中心；图片未就绪时等 onLoad 补算
  const centerOnZoom = useCallback((z: ZoomState) => {
    const container = scrollRef.current;
    const img = zoomImgRef.current;
    if (!container || !img || !img.complete || img.naturalWidth <= 0) return;
    const cRect = container.getBoundingClientRect();
    container.scrollLeft = z.u * img.offsetWidth - (z.px - cRect.left);
    container.scrollTop = z.v * img.offsetHeight - (z.py - cRect.top);
  }, []);

  useLayoutEffect(() => {
    if (zoom) centerOnZoom(zoom);
  }, [zoom, centerOnZoom]);

  // 切换分支会重建 img，把焦点迁到新图上，键盘才能继续切回
  const prevZoomRef = useRef<ZoomState | null>(zoom);
  useEffect(() => {
    const toggled = prevZoomRef.current !== zoom;
    prevZoomRef.current = zoom;
    if (!toggled) return;
    (zoom ? zoomImgRef : fitImgRef).current?.focus({ preventScroll: true });
  }, [zoom]);

  const onPointerDown = (e: React.PointerEvent<HTMLDivElement>) => {
    dragRef.current = {
      down: true,
      moved: false,
      downX: e.clientX,
      downY: e.clientY,
      lastX: e.clientX,
      lastY: e.clientY,
    };
  };
  const onPointerMove = (e: React.PointerEvent<HTMLDivElement>) => {
    const d = dragRef.current;
    if (!d.down) return;
    const dx = e.clientX - d.lastX;
    const dy = e.clientY - d.lastY;
    // 总位移超过几像素才算拖拽，慢速拖动也能识别
    if (Math.hypot(e.clientX - d.downX, e.clientY - d.downY) > 4) {
      d.moved = true;
    }
    // 触控板/滚轮/触屏走容器原生滚动，鼠标拖拽在这里手动滚
    if (d.moved && zoom && e.pointerType === "mouse") {
      const c = scrollRef.current;
      if (c) {
        c.scrollLeft -= dx;
        c.scrollTop -= dy;
      }
    }
    d.lastX = e.clientX;
    d.lastY = e.clientY;
  };
  const endPointer = () => {
    dragRef.current.down = false;
  };

  // 尺寸未知（图未加载/无布局）时不进入放大，无法保证 ≥2 倍适配
  const zoomInAt = (
    img: HTMLImageElement,
    clientX: number,
    clientY: number,
  ) => {
    const rect = img.getBoundingClientRect();
    if (rect.width <= 0 || img.naturalWidth <= 0) return;
    const dpr = window.devicePixelRatio || 1;
    const clamp01 = (n: number) => Math.min(Math.max(n, 0), 1);
    onZoomChange({
      width: Math.max(img.naturalWidth / dpr, rect.width * 2),
      u: clamp01((clientX - rect.left) / rect.width),
      v: clamp01((clientY - rect.top) / rect.height),
      px: clientX,
      py: clientY,
    });
  };

  const onImageClick = (e: React.MouseEvent<HTMLImageElement>) => {
    // 拖拽超过几像素不算点击
    if (dragRef.current.moved) {
      dragRef.current.moved = false;
      return;
    }
    if (zoom) {
      onZoomChange(null);
      return;
    }
    zoomInAt(e.currentTarget, e.clientX, e.clientY);
  };
  const onImageKeyDown = (e: React.KeyboardEvent<HTMLImageElement>) => {
    if (e.key !== "Enter" && e.key !== " ") return;
    e.preventDefault();
    if (zoom) {
      onZoomChange(null);
      return;
    }
    // 键盘触发以图中心为锚点放大
    const rect = e.currentTarget.getBoundingClientRect();
    zoomInAt(
      e.currentTarget,
      rect.left + rect.width / 2,
      rect.top + rect.height / 2,
    );
  };
  const onImageLoad = () => {
    if (zoom) centerOnZoom(zoom);
  };

  const src = screenshotApi.contentUrl(version);
  if (zoom) {
    return (
      <div
        ref={scrollRef}
        className="h-full overflow-auto overscroll-contain"
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={endPointer}
        onPointerLeave={endPointer}
      >
        {/* w-max+min-w-full：内容大于容器时撑出滚动区，小于容器时 m-auto 居中 */}
        <div className="flex min-h-full w-max min-w-full">
          <img
            ref={zoomImgRef}
            src={src}
            alt={version.file_name}
            role="button"
            tabIndex={0}
            draggable={false}
            onClick={onImageClick}
            onKeyDown={onImageKeyDown}
            onLoad={onImageLoad}
            data-zoom="zoomed"
            className="m-auto max-w-none cursor-zoom-out select-none"
            style={zoom.width > 0 ? { width: zoom.width } : undefined}
          />
        </div>
      </div>
    );
  }
  return (
    <div
      className="flex h-full items-center justify-center"
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={endPointer}
      onPointerLeave={endPointer}
    >
      <img
        ref={fitImgRef}
        src={src}
        alt={version.file_name}
        role="button"
        tabIndex={0}
        draggable={false}
        onClick={onImageClick}
        onKeyDown={onImageKeyDown}
        data-zoom="fit"
        className="max-h-full max-w-full cursor-zoom-in select-none object-contain"
      />
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
    isFetching,
    isError,
  } = useScreenshotScreen(open && screenId ? screenId : "");
  const queryClient = useQueryClient();
  const deleteScreen = useDeleteScreenshotScreen(projectId);
  const deleteVersion = useDeleteScreenshotVersion(projectId);

  // 新界面加载期间继续展示上一份成功加载的详情，不整块切成骨架屏；
  // 旧图回退仅限同一次打开期间的翻页——关闭时清空， reopen 不残留
  const [lastDetail, setLastDetail] = useState<ScreenshotScreenDetail | null>(
    null,
  );
  const [prevOpen, setPrevOpen] = useState(open);
  if (open !== prevOpen) {
    setPrevOpen(open);
    if (!open) setLastDetail(null);
  }
  if (open && detail && detail !== lastDetail) {
    setLastDetail(detail);
  }
  // 请求失败不再回退旧图，显示空态（toast+关弹窗在下方 effect）
  const shownDetail = detail ?? (isError ? null : lastDetail);
  const shownDetailId = shownDetail?.id ?? null;
  const versions = shownDetail?.versions ?? [];
  // detail 未到达的旧图回退窗口：只保旧图本体，信息行/缩略图条不混搭旧数据
  const staleView = !detail && shownDetail !== null;

  const listItem = screens.find((s) => s.id === screenId) ?? null;
  const screen = detail ?? listItem;
  const displayName = screen ? screenDisplayName(screen) : "";

  const [selectedVersionId, setSelectedVersionId] = useState<string | null>(
    null,
  );
  const [comparing, setComparing] = useState(false);
  const [leftVersionId, setLeftVersionId] = useState<string | null>(null);
  const [rightVersionId, setRightVersionId] = useState<string | null>(null);
  const [zoom, setZoom] = useState<ZoomState | null>(null);
  const [editOpen, setEditOpen] = useState(false);
  const [confirm, setConfirm] = useState<"version" | "screen" | null>(null);

  // 在新界面详情真正到达时重置版本/对比选择与放大；加载期间保持旧界面的
  // 原选择（配合「继续显示上一张图」），对比模式翻页后回退「最新 / 次新」默认
  const [prevDetailId, setPrevDetailId] = useState(shownDetailId);
  if (shownDetailId !== prevDetailId) {
    setPrevDetailId(shownDetailId);
    setSelectedVersionId(null);
    setLeftVersionId(null);
    setRightVersionId(null);
    setZoom(null);
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

  // 预取前后相邻界面的详情，让翻页不需要等请求
  useEffect(() => {
    if (!open || navIndex < 0) return;
    for (const i of [navIndex - 1, navIndex + 1]) {
      const target = screens[i];
      if (!target) continue;
      void queryClient.prefetchQuery(
        screenshotScreenDetailQueryOptions(target.id),
      );
    }
  }, [open, navIndex, screens, queryClient]);

  // 切换版本、进出对比、关闭弹窗时回到适配窗口（切界面由上方 detail 到达重置）
  const currentVersionId = currentVersion?.id;
  useEffect(() => {
    setZoom(null);
  }, [currentVersionId, comparing, open]);

  useEffect(() => {
    if (!open) return;
    const onKeyDown = (e: KeyboardEvent) => {
      const target = e.target as HTMLElement | null;
      if (target?.closest("input, textarea, select, [contenteditable]")) {
        return;
      }
      // Select 下拉/更多菜单/删除确认打开时让位给浮层自己的按键处理。
      // Base UI 会把关闭的浮层保留在 [hidden] 容器里，只统计未隐藏的才算打开
      const isShown = (el: Element) => !el.closest("[hidden]");
      if (
        [
          ...document.querySelectorAll(
            '[role="listbox"], [role="menu"], [role="alertdialog"]',
          ),
        ].some(isShown)
      ) {
        return;
      }
      // 编辑对话框叠在 lightbox（也是 dialog）上时不导航
      if (
        [...document.querySelectorAll('[role="dialog"]')].filter(isShown)
          .length > 1
      ) {
        return;
      }
      if (e.key === "Escape" && zoom) {
        // 先退放大；capture 阶段拦截，避免 Dialog 的 Esc 关闭同步触发
        e.preventDefault();
        e.stopPropagation();
        e.stopImmediatePropagation();
        setZoom(null);
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
  }, [open, zoom, goPrev, goNext]);

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

  // 标题栏第二行：screen_key（有 title 时）+ 当前版本文件信息
  const infoParts: Array<string | null | undefined> = [];
  if (screen?.title) infoParts.push(screen.screen_key);
  if (!comparing && currentVersion && !staleView) {
    infoParts.push(
      currentVersion.note,
      currentVersion.file_name,
      formatFileSize(currentVersion.file_size),
      formatRelativeTime(currentVersion.uploaded_at),
    );
  }
  const infoLine = infoParts.filter(Boolean).join(" · ");

  // 对比两侧：未选时左=最新、右=次新；只有一个版本时两侧同一张
  const leftCompareId = leftVersionId ?? versions[0]?.id ?? "";
  const rightCompareId =
    rightVersionId ?? versions[1]?.id ?? versions[0]?.id ?? "";

  return (
    <>
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent className="flex h-[calc(100dvh-2rem)] w-[calc(100vw-2rem)] flex-col gap-0 overflow-hidden p-0 sm:max-w-none">
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
              {infoLine && (
                <p className="truncate text-xs text-muted-foreground">
                  {infoLine}
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
                  disabled={!detail || !currentVersion || comparing}
                  onClick={() => setConfirm("version")}
                >
                  <Trash2 className="mr-2 h-4 w-4" />
                  删除当前版本
                </DropdownMenuItem>
                <DropdownMenuItem
                  variant="destructive"
                  disabled={!detail}
                  onClick={() => setConfirm("screen")}
                >
                  <Trash2 className="mr-2 h-4 w-4" />
                  删除界面
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>

          <div className="relative min-h-0 flex-1 bg-muted/30">
            {comparing && versions.length > 0 ? (
              <div className="grid h-full grid-cols-1 gap-2 overflow-y-auto sm:grid-cols-2">
                <ComparePane
                  side="left"
                  versions={versions}
                  selectedId={leftCompareId}
                  onSelect={setLeftVersionId}
                />
                <ComparePane
                  side="right"
                  versions={versions}
                  selectedId={rightCompareId}
                  onSelect={setRightVersionId}
                />
              </div>
            ) : !comparing && currentVersion ? (
              <ZoomableImage
                version={currentVersion}
                zoom={zoom}
                onZoomChange={setZoom}
              />
            ) : !shownDetail && isLoading ? (
              <Skeleton className="h-full w-full" />
            ) : (
              <div className="flex h-full flex-col items-center justify-center gap-2 text-muted-foreground">
                <Images className="h-10 w-10 text-muted-foreground/40" />
                <p className="text-sm">暂无截图版本</p>
              </div>
            )}

            {/* 翻页区始终存在，不受详情加载状态影响 */}
            {navIndex >= 0 && screens.length > 1 && (
              <>
                <NavZone
                  side="left"
                  disabled={!canPrev}
                  onNavigate={goPrev}
                />
                <NavZone
                  side="right"
                  disabled={!canNext}
                  onNavigate={goNext}
                />
              </>
            )}

            {isFetching && (
              <div className="absolute top-3 left-1/2 z-20 flex -translate-x-1/2 items-center gap-1.5 rounded-full bg-background/80 px-3 py-1 text-xs text-muted-foreground shadow-sm backdrop-blur">
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
                加载中
              </div>
            )}

            {!comparing && !staleView && versions.length > 1 && currentVersion && (
              // 两侧各留 5rem，不伸进全高翻页区（w-16/sm:w-20）
              <div className="absolute bottom-3 left-1/2 z-20 flex max-w-[calc(100%-10rem)] -translate-x-1/2 gap-2 overflow-x-auto rounded-lg bg-background/80 p-2 shadow-md backdrop-blur">
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
