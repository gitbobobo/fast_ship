import { memo, useEffect, useRef, useState } from "react";
import { Expand, Images } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { screenshotApi } from "@/lib/api/screenshots";
import { screenDisplayName } from "@/lib/screenshots";
import {
  CARD_META_HEIGHT,
  CANVAS_ZOOM_VAR,
  dragToRatioRect,
  isAnnotationRectTooSmall,
  type Rect,
} from "@/lib/screenshot-canvas";
import { formatRelativeTime } from "@/lib/utils/format";
import { cn } from "@/lib/utils";
import { AnnotationDraftRect, AnnotationRect } from "../annotation-rect";
import { AnnotationDraft } from "./annotation-draft";

export interface CanvasDraft {
  rect: Rect;
  submitting: boolean;
  onSubmit: (body: string) => void;
  onCancel: () => void;
}

interface CanvasCardProps {
  screen: ScreenshotScreen;
  /** 卡片在世界坐标的位置与尺寸（含元信息行） */
  rect: Rect;
  imageHeight: number;
  /** 本批放行挂载 <img>；未放行的卡片继续显示占位 */
  imageEnabled: boolean;
  /** 图片宽高比已知（接口给了尺寸或 onLoad 量过）；未知时不叠加标注 */
  aspectKnown: boolean;
  annotations: ScreenshotAnnotation[];
  /** 标注 → 面板同序序号（矩形角标） */
  annotationIndex: Map<string, number>;
  /** 覆盖更新后待确认的 open 标注数 */
  outdatedOpenCount: number;
  selectedAnnotationId: string | null;
  hoverAnnotationId: string | null;
  flashAnnotationId: string | null;
  drawEnabled: boolean;
  draft: CanvasDraft | null;
  onFocusCard: (screenId: string) => void;
  onOpenPreview: (screenId: string) => void;
  onImageSize: (screenId: string, width: number, height: number) => void;
  onSelectAnnotation: (annotationId: string) => void;
  onHoverAnnotation: (annotationId: string | null) => void;
  onDrawEnd: (screen: ScreenshotScreen, rect: Rect) => void;
}

/** 画布上的一张截图卡片：叠加该截图的标注矩形，标语言/亮度与待确认徽标 */
function CanvasCardImpl({
  screen,
  rect,
  imageHeight,
  imageEnabled,
  aspectKnown,
  annotations,
  annotationIndex,
  outdatedOpenCount,
  selectedAnnotationId,
  hoverAnnotationId,
  flashAnnotationId,
  drawEnabled,
  draft,
  onFocusCard,
  onOpenPreview,
  onImageSize,
  onSelectAnnotation,
  onHoverAnnotation,
  onDrawEnd,
}: CanvasCardProps) {
  const imageAreaRef = useRef<HTMLDivElement>(null);
  const [drawRect, setDrawRect] = useState<Rect | null>(null);
  // 图片加载完成前保留占位；覆盖上传换图后重新占位
  const [loadedScreenRev, setLoadedScreenRev] = useState<string | null>(null);
  // 以 content_url 为图版本指纹：覆盖上传换图即重置占位
  const imageLoaded = loadedScreenRev === screen.content_url;
  const stopDrawRef = useRef<(() => void) | null>(null);

  useEffect(() => () => stopDrawRef.current?.(), []);

  // 在图片区拖出矩形：像素位移直接除以图片区屏幕盒尺寸即得比例坐标，
  // 与画布缩放无关
  const onImagePointerDown = (e: React.PointerEvent<HTMLDivElement>) => {
    if (!drawEnabled || e.button !== 0 || !aspectKnown) return;
    const el = imageAreaRef.current;
    if (!el) return;
    e.stopPropagation();
    e.preventDefault();
    const box = el.getBoundingClientRect();
    const local = (ev: PointerEvent | React.PointerEvent) => ({
      x: ev.clientX - box.left,
      y: ev.clientY - box.top,
    });
    const start = local(e);
    const size = { width: box.width, height: box.height };

    const onMove = (ev: PointerEvent) =>
      setDrawRect(dragToRatioRect(start, local(ev), size));
    const stop = () => {
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", onUp);
      window.removeEventListener("pointercancel", onCancel);
      stopDrawRef.current = null;
      setDrawRect(null);
    };
    const onUp = (ev: PointerEvent) => {
      const ratio = dragToRatioRect(start, local(ev), size);
      stop();
      if (ratio && !isAnnotationRectTooSmall(ratio)) onDrawEnd(screen, ratio);
    };
    const onCancel = () => stop();
    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp);
    window.addEventListener("pointercancel", onCancel);
    stopDrawRef.current = stop;
  };

  const name = screenDisplayName(screen);

  return (
    <div
      data-testid={`canvas-card-${screen.id}`}
      data-canvas-card
      className="absolute"
      style={{
        left: rect.x,
        top: rect.y,
        width: rect.width,
        height: rect.height,
      }}
    >
      <div
        className={cn(
          "group relative flex h-full flex-col",
          drawEnabled ? "cursor-crosshair" : "cursor-pointer",
        )}
        onClick={(e) => {
          if (drawEnabled) return;
          if ((e.target as HTMLElement).closest("[data-card-action]")) return;
          onFocusCard(screen.id);
        }}
      >
        <div className="relative shrink-0" style={{ height: imageHeight }}>
          <div
            ref={imageAreaRef}
            className={cn(
              "absolute inset-0 flex items-center justify-center overflow-hidden rounded-sm bg-muted/30",
              !drawEnabled &&
                "transition-shadow group-hover:ring-2 group-hover:ring-primary/40",
            )}
            onPointerDown={onImagePointerDown}
          >
            {!imageLoaded && (
              <Images className="absolute h-8 w-8 text-muted-foreground/40" />
            )}
            {imageEnabled && (
              <img
                src={screenshotApi.contentUrl(screen)}
                alt={name}
                draggable={false}
                loading="lazy"
                decoding="async"
                className="h-full w-full select-none object-contain"
                onLoad={(e) => {
                  const img = e.currentTarget;
                  setLoadedScreenRev(screen.content_url);
                  // 接口已给尺寸的截图不必再量，避免每次 onLoad 都触发重排
                  const needsMeasure = !screen.width || !screen.height;
                  if (needsMeasure && img.naturalWidth > 0) {
                    onImageSize(
                      screen.id,
                      img.naturalWidth,
                      img.naturalHeight,
                    );
                  }
                }}
              />
            )}
          </div>

          <div className="pointer-events-none absolute inset-0">
            {aspectKnown &&
              annotations.map((a) => (
                <AnnotationRect
                  key={a.id}
                  annotation={a}
                  index={annotationIndex.get(a.id)}
                  interactive={!drawEnabled}
                  selected={a.id === selectedAnnotationId}
                  hovered={a.id === hoverAnnotationId}
                  flashing={a.id === flashAnnotationId}
                  onSelect={onSelectAnnotation}
                  onHover={onHoverAnnotation}
                />
              ))}

            {drawRect && <AnnotationDraftRect rect={drawRect} />}

            {draft && (
              <>
                <AnnotationDraftRect rect={draft.rect} />
                {/* 皮肤尺寸走 --canvas-zoom：输入框在屏幕上保持固定大小，
                    缩放变化经 CSS 变量逐帧生效，不需要卡片重渲染 */}
                <div
                  className="pointer-events-auto absolute z-10 origin-top-left"
                  style={{
                    left: `${draft.rect.x * 100}%`,
                    top: `${(draft.rect.y + draft.rect.height) * 100}%`,
                    transform: `translateY(calc(8px / var(${CANVAS_ZOOM_VAR}, 1))) scale(calc(1 / var(${CANVAS_ZOOM_VAR}, 1)))`,
                  }}
                >
                  <AnnotationDraft
                    submitting={draft.submitting}
                    onSubmit={draft.onSubmit}
                    onCancel={draft.onCancel}
                  />
                </div>
              </>
            )}
          </div>

          {!drawEnabled && (
            <Button
              variant="secondary"
              size="icon-sm"
              data-no-pan
              data-card-action
              aria-label="打开预览"
              title="打开预览"
              className="absolute top-2 right-2 opacity-0 shadow-sm transition-opacity group-hover:opacity-100 focus-visible:opacity-100"
              onClick={(e) => {
                e.stopPropagation();
                onOpenPreview(screen.id);
              }}
            >
              <Expand className="h-3.5 w-3.5" />
            </Button>
          )}
        </div>

        <div
          className="flex shrink-0 items-center gap-1.5"
          style={{ height: CARD_META_HEIGHT }}
        >
          <p className="min-w-0 truncate text-sm font-medium">{name}</p>
          {screen.locale && (
            <Badge variant="outline" className="shrink-0">
              {screen.locale}
            </Badge>
          )}
          {screen.theme && (
            <Badge variant="outline" className="shrink-0">
              {screen.theme === "dark" ? "暗色" : "亮色"}
            </Badge>
          )}
          {outdatedOpenCount > 0 && (
            <Badge
              variant="outline"
              className="shrink-0 border-amber-500/60 text-amber-600 dark:text-amber-400"
              data-testid={`canvas-card-outdated-${screen.id}`}
            >
              {outdatedOpenCount} 条待确认
            </Badge>
          )}
          <span className="ml-auto shrink-0 text-xs text-muted-foreground">
            {formatRelativeTime(screen.uploaded_at)}
          </span>
        </div>
      </div>
    </div>
  );
}

export const CanvasCard = memo(CanvasCardImpl);
