import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { toast } from "sonner";
import {
  ChevronLeft,
  ChevronRight,
  Images,
  Loader2,
  MoreHorizontal,
  Pencil,
  Trash2,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Dialog,
  DialogContent,
  DialogTitle,
} from "@/components/ui/dialog";
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
import { useDeleteScreenshotScreen } from "@/lib/hooks/use-screenshots";
import { useScreenshotAnnotations } from "@/lib/hooks/use-screenshot-annotations";
import { screenshotApi } from "@/lib/api/screenshots";
import {
  countAnnotations,
  countOpenAnnotations,
} from "@/lib/screenshot-canvas";
import { screenDisplayName } from "@/lib/screenshots";
import {
  formatFileSize,
  formatRelativeTime,
} from "@/lib/utils/format";
import { cn } from "@/lib/utils";
import { AnnotationOverlay } from "./annotation-overlay";
import { ScreenshotEditDialog } from "./screenshot-edit-dialog";

const EMPTY_ANNOTATIONS: ScreenshotAnnotation[] = [];

interface ScreenshotLightboxProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  projectId: string;
  /** 当前筛选后的导航列表，左右键/箭头在此范围内切换 */
  screens: ScreenshotScreen[];
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

/**
 * 单图视图：默认 object-contain 适配；点击放大到原始像素 ÷ dpr（不低于
 * 适配尺寸 2 倍），以点击处为中心；放大后可滚动或鼠标拖拽，再点回到适配。
 */
function ZoomableImage({
  screen,
  zoom,
  onZoomChange,
  annotations,
}: {
  screen: ScreenshotScreen;
  zoom: ZoomState | null;
  onZoomChange: (zoom: ZoomState | null) => void;
  annotations: ScreenshotAnnotation[];
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

  const src = screenshotApi.contentUrl(screen);
  const name = screenDisplayName(screen);
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
        <div className="relative flex min-h-full w-max min-w-full">
          <img
            ref={zoomImgRef}
            src={src}
            alt={name}
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
          <AnnotationOverlay imgRef={zoomImgRef} annotations={annotations} />
        </div>
      </div>
    );
  }
  return (
    <div
      className="relative flex h-full items-center justify-center"
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={endPointer}
      onPointerLeave={endPointer}
    >
      <img
        ref={fitImgRef}
        src={src}
        alt={name}
        role="button"
        tabIndex={0}
        draggable={false}
        onClick={onImageClick}
        onKeyDown={onImageKeyDown}
        data-zoom="fit"
        className="max-h-full max-w-full cursor-zoom-in select-none object-contain"
      />
      <AnnotationOverlay imgRef={fitImgRef} annotations={annotations} />
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
  const deleteScreen = useDeleteScreenshotScreen(projectId);
  const { data: annotationData, isFetching: annotationsFetching } =
    useScreenshotAnnotations(projectId, {}, open);
  const annotations = annotationData ?? EMPTY_ANNOTATIONS;

  const screen = screens.find((s) => s.id === screenId) ?? null;
  const displayName = screen ? screenDisplayName(screen) : "";

  const [zoom, setZoom] = useState<ZoomState | null>(null);
  const [editOpen, setEditOpen] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);

  // 切界面/关弹窗回到适配窗口：放大是查看状态
  useEffect(() => {
    setZoom(null);
  }, [screenId, open]);

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
    if (!open) return;
    const onKeyDown = (e: KeyboardEvent) => {
      const target = e.target as HTMLElement | null;
      if (target?.closest("input, textarea, select, [contenteditable]")) {
        return;
      }
      // 下拉/菜单/确认框打开时让位给浮层自己的按键处理。
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

  const handleDeleteScreen = async () => {
    if (!screenId) return;
    try {
      await deleteScreen.mutateAsync(screenId);
      toast.success("已删除截图及其标注");
      onOpenChange(false);
    } catch {
      toast.error("删除失败，请稍后重试");
    }
  };

  // 标题栏第二行：screen_key（有 title 时）+ 文件信息与维度
  const infoParts: Array<string | null | undefined> = [];
  if (screen?.title) infoParts.push(screen.screen_key);
  if (screen) {
    infoParts.push(
      screen.platform,
      screen.locale,
      screen.theme === "dark" ? "暗色" : screen.theme === "light" ? "亮色" : null,
      formatFileSize(screen.file_size),
      formatRelativeTime(screen.uploaded_at),
      screen.uploaded_by,
    );
  }
  const infoLine = infoParts.filter(Boolean).join(" · ");

  const screenAnnotations = screen
    ? annotations.filter((a) => a.screen_id === screen.id)
    : EMPTY_ANNOTATIONS;
  // 删除会级联删掉挂在其上的全部标注（不分状态），确认框提示总数与其中未解决数
  const totalOnScreen = screenId
    ? countAnnotations(annotations, { screenId })
    : 0;
  const openOnScreen = screenId
    ? countOpenAnnotations(annotations, { screenId })
    : 0;

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
                {screen?.platform && (
                  <Badge variant="outline">{screen.platform}</Badge>
                )}
                {screen?.locale && (
                  <Badge variant="outline">{screen.locale}</Badge>
                )}
                {screen?.theme && (
                  <Badge variant="outline">
                    {screen.theme === "dark" ? "暗色" : "亮色"}
                  </Badge>
                )}
              </div>
              {infoLine && (
                <p className="truncate text-xs text-muted-foreground">
                  {infoLine}
                </p>
              )}
            </div>

            <Button
              variant="outline"
              size="icon-sm"
              aria-label="编辑截图"
              disabled={!screen}
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
                  disabled={!screen}
                  onClick={() => setConfirmDelete(true)}
                >
                  <Trash2 className="mr-2 h-4 w-4" />
                  删除截图
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>

          <div className="relative min-h-0 flex-1 bg-muted/30">
            {screen ? (
              <ZoomableImage
                screen={screen}
                zoom={zoom}
                onZoomChange={setZoom}
                annotations={screenAnnotations}
              />
            ) : (
              <div className="flex h-full flex-col items-center justify-center gap-2 text-muted-foreground">
                <Images className="h-10 w-10 text-muted-foreground/40" />
                <p className="text-sm">截图不存在或已被删除</p>
              </div>
            )}

            {/* 翻页区始终存在 */}
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

            {annotationsFetching && (
              <div className="absolute top-3 left-1/2 z-20 flex -translate-x-1/2 items-center gap-1.5 rounded-full bg-background/80 px-3 py-1 text-xs text-muted-foreground shadow-sm backdrop-blur">
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
                加载中
              </div>
            )}
          </div>
        </DialogContent>
      </Dialog>

      {screen && (
        <ScreenshotEditDialog
          open={editOpen}
          onOpenChange={setEditOpen}
          projectId={projectId}
          screen={screen}
        />
      )}

      <AlertDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>删除截图？</AlertDialogTitle>
            <AlertDialogDescription>
              将删除「{displayName}」及其标注，不可恢复。
              {totalOnScreen > 0 &&
                `将同时删除 ${totalOnScreen} 条标注${openOnScreen > 0 ? `，其中 ${openOnScreen} 条未解决` : ""}。`}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>取消</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                setConfirmDelete(false);
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
