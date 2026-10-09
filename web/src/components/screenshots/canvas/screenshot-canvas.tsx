import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { toast } from "sonner";
import { cn } from "@/lib/utils";
import { useCreateScreenshotAnnotation } from "@/lib/hooks/use-screenshot-annotations";
import {
  CARD_WIDTH,
  aspectFromSize,
  countOpenOnOlderVersions,
  fitViewport,
  focusRect,
  layoutCanvas,
  normalizeRatioRect,
  rectsIntersect,
  visibleWorldRect,
  type AnnotationStatusFilter,
  type Rect,
} from "@/lib/screenshot-canvas";
import { AnnotationPanel } from "./annotation-panel";
import { CanvasCard, type CanvasDraft } from "./canvas-card";
import { CanvasToolbar } from "./canvas-toolbar";
import { isTypingTarget, useCanvasViewport } from "./use-canvas-viewport";

/** 卡片屏幕宽度低于此值（像素）时不再加载图片，只画占位 */
const LOW_DETAIL_CARD_PX = 48;
const FLASH_MS = 2400;
const EMPTY_ANNOTATIONS: ScreenshotAnnotation[] = [];

export interface CanvasLocateRequest {
  annotationId: string;
  /** 同一条标注重复定位时靠 nonce 区分请求 */
  nonce: number;
}

interface ScreenshotCanvasProps {
  projectId: string;
  /** 画布范围内的界面（已按分组 tab 与搜索过滤） */
  screens: ScreenshotScreenListItem[];
  /** 项目全部标注，含旧版本上的 */
  annotations: ScreenshotAnnotation[];
  annotationsReady: boolean;
  /** 项目/分组变化时变化，触发视图重置 */
  resetKey: string;
  /** 预览弹窗等浮层打开时关闭画布快捷键 */
  controlsEnabled: boolean;
  locateRequest: CanvasLocateRequest | null;
  onOpenPreview: (
    screenId: string,
    versionId?: string,
    annotationId?: string,
  ) => void;
}

interface DraftState {
  screenId: string;
  versionId: string;
  rect: Rect;
}

export function ScreenshotCanvas({
  projectId,
  screens,
  annotations,
  annotationsReady,
  resetKey,
  controlsEnabled,
  locateRequest,
  onOpenPreview,
}: ScreenshotCanvasProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const {
    viewport,
    smooth,
    size,
    spaceHeld,
    panning,
    moveTo,
    jumpTo,
    zoomByFactor,
    handlers,
  } = useCanvasViewport(containerRef, controlsEnabled);
  const createAnnotation = useCreateScreenshotAnnotation(projectId);

  const [panelOpen, setPanelOpen] = useState(
    () => typeof window === "undefined" || window.innerWidth >= 768,
  );
  const [drawMode, setDrawMode] = useState(false);
  const [showResolved, setShowResolved] = useState(false);
  const [statusFilter, setStatusFilter] =
    useState<AnnotationStatusFilter>("open");
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [hoverId, setHoverId] = useState<string | null>(null);
  const [flashId, setFlashId] = useState<string | null>(null);
  const [draft, setDraft] = useState<DraftState | null>(null);
  // 接口未给尺寸的存量版本，用图片 onLoad 量到的宽高比校正排版
  const [measured, setMeasured] = useState<Record<string, number>>({});
  const flashTimerRef = useRef<number | null>(null);

  // 切项目/分组：清掉选中、草稿、框选态（视图重置在下方 effect）
  const [prevResetKey, setPrevResetKey] = useState(resetKey);
  if (resetKey !== prevResetKey) {
    setPrevResetKey(resetKey);
    setSelectedId(null);
    setHoverId(null);
    setFlashId(null);
    setDraft(null);
    setDrawMode(false);
  }

  const aspectOf = useCallback(
    (screenId: string): number | null => {
      const screen = screens.find((s) => s.id === screenId);
      const version = screen?.latest_version;
      if (!version) return null;
      return (
        aspectFromSize(version.width, version.height) ??
        measured[version.id] ??
        null
      );
    },
    [screens, measured],
  );

  const layout = useMemo(
    () => layoutCanvas(screens, aspectOf),
    [screens, aspectOf],
  );
  const cardRectById = useMemo(
    () => new Map(layout.cards.map((c) => [c.screenId, c])),
    [layout.cards],
  );
  const screenById = useMemo(
    () => new Map(screens.map((s) => [s.id, s])),
    [screens],
  );

  // 视图重置：新范围（项目/分组）第一次有卡片且量到容器尺寸时适应全部
  const fittedKeyRef = useRef<string | null>(null);
  useEffect(() => {
    if (fittedKeyRef.current === resetKey) return;
    if (size.width <= 0 || layout.cards.length === 0) return;
    fittedKeyRef.current = resetKey;
    jumpTo(fitViewport(layout.bounds, size));
  }, [resetKey, size, layout, jumpTo]);

  // 画布只展示最新版本：标注按「挂在该界面最新版本」归到卡片上
  const annotationsByScreen = useMemo(() => {
    const map = new Map<string, ScreenshotAnnotation[]>();
    for (const a of annotations) {
      const latestId = screenById.get(a.screen_id)?.latest_version?.id;
      if (!latestId || a.version_id !== latestId) continue;
      if (a.status === "resolved" && !showResolved) continue;
      const list = map.get(a.screen_id);
      if (list) list.push(a);
      else map.set(a.screen_id, [a]);
    }
    return map;
  }, [annotations, screenById, showResolved]);

  const olderOpenCounts = useMemo(
    () =>
      countOpenOnOlderVersions(
        annotations,
        (screenId) => screenById.get(screenId)?.latest_version?.id,
      ),
    [annotations, screenById],
  );

  // 面板范围 = 当前画布内的界面；新的在上
  const scopeAnnotations = useMemo(
    () =>
      annotations
        .filter((a) => screenById.has(a.screen_id))
        .sort((a, b) => b.created_at.localeCompare(a.created_at)),
    [annotations, screenById],
  );

  const flash = useCallback((annotationId: string) => {
    setFlashId(annotationId);
    if (flashTimerRef.current) window.clearTimeout(flashTimerRef.current);
    flashTimerRef.current = window.setTimeout(() => {
      setFlashId(null);
      flashTimerRef.current = null;
    }, FLASH_MS);
  }, []);
  useEffect(
    () => () => {
      if (flashTimerRef.current) window.clearTimeout(flashTimerRef.current);
    },
    [],
  );

  const focusCard = useCallback(
    (screenId: string) => {
      const card = cardRectById.get(screenId);
      if (!card || size.width <= 0) return;
      moveTo(focusRect(card.rect, size, { padding: 64, maxScale: 2 }));
    },
    [cardRectById, size, moveTo],
  );

  // 定位：最新版本上的标注平移缩放到卡片并闪烁；旧版本交给预览弹窗
  const locate = useCallback(
    (annotation: ScreenshotAnnotation) => {
      const screen = screenById.get(annotation.screen_id);
      if (!screen) {
        toast.info("该标注所在的界面不在当前画布范围内");
        return;
      }
      if (screen.latest_version?.id !== annotation.version_id) {
        onOpenPreview(screen.id, annotation.version_id, annotation.id);
        return;
      }
      if (annotation.status === "resolved") setShowResolved(true);
      setSelectedId(annotation.id);
      setDrawMode(false);
      focusCard(screen.id);
      flash(annotation.id);
    },
    [screenById, onOpenPreview, focusCard, flash],
  );

  // URL / 外部触发的定位请求：等数据与容器尺寸就绪后只处理一次
  const handledNonceRef = useRef<number | null>(null);
  useEffect(() => {
    if (!locateRequest || handledNonceRef.current === locateRequest.nonce) {
      return;
    }
    if (!annotationsReady || size.width <= 0 || screens.length === 0) return;
    const target = annotations.find((a) => a.id === locateRequest.annotationId);
    // 所在界面还没进入画布范围（放宽 tab/搜索的更新未生效）时继续等
    if (target && !screenById.has(target.screen_id)) return;
    handledNonceRef.current = locateRequest.nonce;
    if (!target) {
      toast.error("标注不存在或已被删除");
      return;
    }
    // 定位优先于首次「适应全部」
    fittedKeyRef.current = resetKey;
    locate(target);
  }, [
    locateRequest,
    annotationsReady,
    annotations,
    screens.length,
    screenById,
    size,
    resetKey,
    locate,
  ]);

  const toggleDraw = useCallback(() => {
    setDrawMode((on) => !on);
    setDraft(null);
  }, []);

  // 快捷键：R 切换框选，Esc 逐级退出（草稿 → 框选态 → 选中）
  useEffect(() => {
    if (!controlsEnabled) return;
    const onKeyDown = (e: KeyboardEvent) => {
      if (isTypingTarget(e.target) || e.metaKey || e.ctrlKey || e.altKey) {
        return;
      }
      if (e.key === "r" || e.key === "R") {
        e.preventDefault();
        toggleDraw();
      } else if (e.key === "Escape") {
        if (draft) setDraft(null);
        else if (drawMode) setDrawMode(false);
        else setSelectedId(null);
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [controlsEnabled, toggleDraw, draft, drawMode]);

  const handleImageSize = useCallback(
    (versionId: string, width: number, height: number) => {
      setMeasured((prev) =>
        prev[versionId] ? prev : { ...prev, [versionId]: height / width },
      );
    },
    [],
  );

  const handleSelectAnnotation = useCallback((annotationId: string) => {
    setSelectedId(annotationId);
    setPanelOpen(true);
  }, []);

  const handleDrawEnd = useCallback(
    (screen: ScreenshotScreenListItem, rect: Rect) => {
      if (!screen.latest_version) return;
      setDraft({
        screenId: screen.id,
        versionId: screen.latest_version.id,
        rect,
      });
    },
    [],
  );

  const submitDraft = async (body: string) => {
    if (!draft) return;
    const rect = normalizeRatioRect(draft.rect);
    try {
      const res = await createAnnotation.mutateAsync({
        versionId: draft.versionId,
        payload: { ...rect, body },
      });
      toast.success("已添加标注");
      setDraft(null);
      setSelectedId(res.data.id);
      setPanelOpen(true);
    } catch {
      toast.error("添加标注失败，请稍后重试");
    }
  };

  const fitAll = () => moveTo(fitViewport(layout.bounds, size));

  // 只渲染与视口（外扩一屏）相交的卡片
  const margin = Math.max(size.width, size.height);
  const visibleRect = visibleWorldRect(viewport, size, margin);
  const visibleCards = layout.cards.filter((card) =>
    rectsIntersect(card.rect, visibleRect),
  );
  const lowDetail = CARD_WIDTH * viewport.scale < LOW_DETAIL_CARD_PX;
  const drawEnabled = drawMode && !spaceHeld;

  const cursor = panning ? "grabbing" : spaceHeld ? "grab" : undefined;

  return (
    <div
      className="relative flex h-full min-h-0 overflow-hidden rounded-lg border bg-muted/20"
      data-testid="screenshot-canvas"
    >
      <div
        ref={containerRef}
        className={cn(
          "relative min-w-0 flex-1 touch-none overflow-hidden select-none",
          drawEnabled && "cursor-crosshair",
        )}
        style={cursor ? { cursor } : undefined}
        onPointerDown={handlers.onPointerDown}
        onClickCapture={handlers.onClickCapture}
        onClick={(e) => {
          const target = e.target as HTMLElement;
          if (target.closest("[data-canvas-card], [data-no-pan]")) return;
          setSelectedId(null);
        }}
      >
        <div
          className="absolute top-0 left-0 origin-top-left will-change-transform"
          style={{
            transform: `translate(${viewport.x}px, ${viewport.y}px) scale(${viewport.scale})`,
            transition: smooth ? "transform 320ms ease" : undefined,
          }}
        >
          {layout.groups.map((group) => (
            <div
              key={group.key}
              className="absolute flex items-end gap-3 pb-3"
              style={{
                left: group.titleRect.x,
                top: group.titleRect.y,
                width: group.titleRect.width,
                height: group.titleRect.height,
              }}
            >
              <h3 className="truncate text-2xl font-semibold">{group.title}</h3>
              <span className="pb-1 text-sm text-muted-foreground">
                {group.count}
              </span>
            </div>
          ))}

          {visibleCards.map((card) => {
            const screen = screenById.get(card.screenId);
            if (!screen) return null;
            const cardAnnotations =
              annotationsByScreen.get(screen.id) ?? EMPTY_ANNOTATIONS;
            const owns = (id: string | null) =>
              id !== null && cardAnnotations.some((a) => a.id === id)
                ? id
                : null;
            const cardDraft: CanvasDraft | null =
              draft && draft.screenId === screen.id
                ? {
                    rect: draft.rect,
                    counterScale: 1 / viewport.scale,
                    submitting: createAnnotation.isPending,
                    onSubmit: (body) => void submitDraft(body),
                    onCancel: () => setDraft(null),
                  }
                : null;
            return (
              <CanvasCard
                key={card.screenId}
                screen={screen}
                rect={card.rect}
                imageHeight={card.imageHeight}
                lowDetail={lowDetail}
                aspectKnown={aspectOf(screen.id) !== null}
                annotations={cardAnnotations}
                olderOpenCount={olderOpenCounts.get(screen.id) ?? 0}
                selectedAnnotationId={owns(selectedId)}
                hoverAnnotationId={owns(hoverId)}
                flashAnnotationId={owns(flashId)}
                drawEnabled={drawEnabled}
                draft={cardDraft}
                onFocusCard={focusCard}
                onOpenPreview={onOpenPreview}
                onImageSize={handleImageSize}
                onSelectAnnotation={handleSelectAnnotation}
                onHoverAnnotation={setHoverId}
                onDrawEnd={handleDrawEnd}
              />
            );
          })}
        </div>

        <CanvasToolbar
          scale={viewport.scale}
          drawMode={drawMode}
          showResolved={showResolved}
          panelOpen={panelOpen}
          onToggleDraw={toggleDraw}
          onToggleResolved={() => setShowResolved((on) => !on)}
          onTogglePanel={() => setPanelOpen((open) => !open)}
          onZoomIn={() => zoomByFactor(1.25)}
          onZoomOut={() => zoomByFactor(0.8)}
          onFit={fitAll}
        />

        {drawEnabled && !draft && (
          <p className="pointer-events-none absolute bottom-3 left-1/2 z-20 -translate-x-1/2 rounded-full bg-foreground/80 px-3 py-1 text-xs text-background">
            在截图上拖出矩形添加标注 · Esc 退出
          </p>
        )}
      </div>

      {panelOpen && (
        <aside
          data-no-pan
          className="z-30 w-80 shrink-0 border-l bg-background max-md:absolute max-md:inset-y-0 max-md:right-0 max-md:shadow-lg"
        >
          <AnnotationPanel
            projectId={projectId}
            annotations={scopeAnnotations}
            statusFilter={statusFilter}
            onStatusFilterChange={setStatusFilter}
            selectedId={selectedId}
            hoverId={hoverId}
            onHover={setHoverId}
            onSelect={setSelectedId}
            onLocate={locate}
          />
        </aside>
      )}
    </div>
  );
}
