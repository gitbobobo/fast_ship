import { memo, useEffect, useRef, useState } from "react";
import { Expand, Images } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { screenshotApi } from "@/lib/api/screenshots";
import { screenDisplayName } from "@/lib/screenshots";
import {
  CARD_META_HEIGHT,
  dragToRatioRect,
  isAnnotationRectTooSmall,
  type Rect,
} from "@/lib/screenshot-canvas";
import { formatRelativeTime } from "@/lib/utils/format";
import { cn } from "@/lib/utils";
import { AnnotationRect } from "../annotation-rect";
import { AnnotationDraft } from "./annotation-draft";

export interface CanvasDraft {
  rect: Rect;
  /** 抵消世界缩放，让输入框保持屏幕上的固定大小 */
  counterScale: number;
  submitting: boolean;
  onSubmit: (body: string) => void;
  onCancel: () => void;
}

interface CanvasCardProps {
  screen: ScreenshotScreenListItem;
  /** 卡片在世界坐标的位置与尺寸（含元信息行） */
  rect: Rect;
  imageHeight: number;
  /** 缩得很小时不再加载图片，避免一屏几百张原图 */
  lowDetail: boolean;
  /** 图片宽高比已知（接口给了尺寸或 onLoad 量过）；未知时不叠加标注 */
  aspectKnown: boolean;
  annotations: ScreenshotAnnotation[];
  /** 标注 → 面板同序序号（矩形角标） */
  annotationIndex: Map<string, number>;
  olderOpenCount: number;
  selectedAnnotationId: string | null;
  hoverAnnotationId: string | null;
  flashAnnotationId: string | null;
  drawEnabled: boolean;
  draft: CanvasDraft | null;
  onFocusCard: (screenId: string) => void;
  onOpenPreview: (screenId: string) => void;
  onImageSize: (versionId: string, width: number, height: number) => void;
  onSelectAnnotation: (annotationId: string) => void;
  onHoverAnnotation: (annotationId: string | null) => void;
  onDrawEnd: (screen: ScreenshotScreenListItem, rect: Rect) => void;
}

/** 画布上的一张截图卡片：只展示最新版本，叠加该版本的标注矩形 */
function CanvasCardImpl({
  screen,
  rect,
  imageHeight,
  lowDetail,
  aspectKnown,
  annotations,
  annotationIndex,
  olderOpenCount,
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
  const version = screen.latest_version;
  const imageAreaRef = useRef<HTMLDivElement>(null);
  const [drawRect, setDrawRect] = useState<Rect | null>(null);
  const stopDrawRef = useRef<(() => void) | null>(null);

  useEffect(() => () => stopDrawRef.current?.(), []);

  // 在图片区拖出矩形：像素位移直接除以图片区屏幕盒尺寸即得比例坐标，
  // 与画布缩放无关
  const onImagePointerDown = (e: React.PointerEvent<HTMLDivElement>) => {
    if (!drawEnabled || e.button !== 0 || !version || !aspectKnown) return;
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
            {version && !lowDetail ? (
              <img
                src={screenshotApi.contentUrl(version)}
                alt={name}
                draggable={false}
                className="h-full w-full select-none object-contain"
                onLoad={(e) => {
                  const img = e.currentTarget;
                  if (img.naturalWidth > 0) {
                    onImageSize(version.id, img.naturalWidth, img.naturalHeight);
                  }
                }}
              />
            ) : (
              <Images className="h-8 w-8 text-muted-foreground/40" />
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

            {drawRect && (
              <div
                className="absolute rounded-[2px] border-2 border-dashed border-red-500 bg-red-500/10 shadow-[inset_0_0_0_1px_rgba(255,255,255,0.85),0_0_0_1px_rgba(15,23,42,0.55)]"
                style={{
                  left: `${drawRect.x * 100}%`,
                  top: `${drawRect.y * 100}%`,
                  width: `${drawRect.width * 100}%`,
                  height: `${drawRect.height * 100}%`,
                }}
              />
            )}

            {draft && (
              <>
                <div
                  className="absolute rounded-[2px] border-2 border-dashed border-red-500 bg-red-500/10 shadow-[inset_0_0_0_1px_rgba(255,255,255,0.85),0_0_0_1px_rgba(15,23,42,0.55)]"
                  style={{
                    left: `${draft.rect.x * 100}%`,
                    top: `${draft.rect.y * 100}%`,
                    width: `${draft.rect.width * 100}%`,
                    height: `${draft.rect.height * 100}%`,
                  }}
                />
                <div
                  className="pointer-events-auto absolute z-10 origin-top-left"
                  style={{
                    left: `${draft.rect.x * 100}%`,
                    top: `${(draft.rect.y + draft.rect.height) * 100}%`,
                    transform: `translateY(${8 * draft.counterScale}px) scale(${draft.counterScale})`,
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
          className="flex shrink-0 items-center gap-2"
          style={{ height: CARD_META_HEIGHT }}
        >
          <p className="min-w-0 truncate text-sm font-medium">{name}</p>
          {olderOpenCount > 0 && (
            <Badge
              variant="outline"
              className="shrink-0 border-amber-500/60 text-amber-600 dark:text-amber-400"
            >
              上一版有 {olderOpenCount} 条未解决
            </Badge>
          )}
          <span className="ml-auto shrink-0 text-xs text-muted-foreground">
            {screen.version_count} 个版本 ·{" "}
            {formatRelativeTime(screen.last_uploaded_at)}
          </span>
        </div>
      </div>
    </div>
  );
}

export const CanvasCard = memo(CanvasCardImpl);
