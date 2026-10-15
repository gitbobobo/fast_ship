import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
} from "react";
import { toast } from "sonner";
import { cn } from "@/lib/utils";
import { useCreateScreenshotAnnotation } from "@/lib/hooks/use-screenshot-annotations";
import {
  aspectFromSize,
  countOutdatedOpen,
  CANVAS_ZOOM_VAR,
  FIT_PADDING,
  fitViewport,
  focusRect,
  layoutCanvas,
  normalizeRatioRect,
  pickCanvasColumns,
  rectsIntersect,
  visibleWorldRect,
  type Rect,
} from "@/lib/screenshot-canvas";
import { AnnotationPanel } from "./annotation-panel";
import { CanvasCard, type CanvasDraft } from "./canvas-card";
import { CanvasToolbar } from "./canvas-toolbar";
import { isTypingTarget, useCanvasViewport } from "./use-canvas-viewport";

const FLASH_MS = 2400;
const EMPTY_ANNOTATIONS: ScreenshotAnnotation[] = [];
/** 首帧直挂的 <img> 数与之后每批追加数/间隔，摊平深缩时的并发加载 */
const IMAGE_MOUNT_BATCH = 30;
const IMAGE_MOUNT_INTERVAL_MS = 80;
/** 适应全部时顶部留给浮动工具栏的净空（top-3 + 工具栏默认高度 + 余量） */
const FIT_TOP_INSET = 64;
/** 工具栏 top-3 偏移 + 内容间距，净空 = 实测高度 + 该值 */
const TOOLBAR_GAP = 28;
/** 分组标题在屏幕上保持的最小字号（px）；缩放再小就反向放大补齐 */
const GROUP_TITLE_MIN_PX = 14;

export interface CanvasLocateRequest {
  annotationId: string;
  /** 同一条标注重复定位时靠 nonce 区分请求 */
  nonce: number;
}

interface ScreenshotCanvasProps {
  projectId: string;
  /** 画布范围内的截图（已按维度过滤） */
  screens: ScreenshotScreen[];
  /** 维度范围（未按搜索收窄）内的截图 id：面板列表、计数与批量按钮同源，
   * 搜索隐藏截图上的标注仍计入，避免「显示 1 条却解决 2 条」 */
  scopeScreenIds: ReadonlySet<string>;
  /** 范围内全部标注（未按状态过滤；画布叠加与面板按 statusScope 收敛） */
  annotations: ScreenshotAnnotation[];
  annotationsReady: boolean;
  /** 状态过滤范围（来自 URL 筛选条）：哪些状态出现在画布叠加与面板里 */
  statusScope: { open: boolean; resolved: boolean };
  /** 面板状态 tab 切换回写 URL 筛选条 */
  onStatusScopeChange: (scope: { open: boolean; resolved: boolean }) => void;
  /** 批量动作与提示词共用的维度过滤（含 __none__ 桶） */
  scopeFilter: {
    platform?: string[];
    locale?: string[];
    theme?: string[];
  };
  /** 当前范围 open 标注数；工具栏「启动线程」以它置灰 */
  openCount: number;
  /** 项目/筛选变化时变化，触发视图重置 */
  resetKey: string;
  /** 预览弹窗等浮层打开时关闭画布快捷键 */
  controlsEnabled: boolean;
  locateRequest: CanvasLocateRequest | null;
  onOpenPreview: (screenId: string) => void;
  onLaunchThread: () => void;
  onCopyPrompt: () => void;
}

interface DraftState {
  screenId: string;
  contentUrl: string;
  rect: Rect;
}

export function ScreenshotCanvas({
  projectId,
  screens,
  scopeScreenIds,
  annotations,
  annotationsReady,
  statusScope,
  onStatusScopeChange,
  scopeFilter,
  openCount,
  resetKey,
  controlsEnabled,
  locateRequest,
  onOpenPreview,
  onLaunchThread,
  onCopyPrompt,
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
    () => typeof window === "undefined" || window.innerWidth >= 1024,
  );
  const [drawMode, setDrawMode] = useState(false);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [hoverId, setHoverId] = useState<string | null>(null);
  const [flashId, setFlashId] = useState<string | null>(null);
  const [draft, setDraft] = useState<DraftState | null>(null);
  // 接口未给尺寸的存量截图，用图片 onLoad 量到的宽高比校正排版
  const [measured, setMeasured] = useState<Record<string, number>>({});
  const flashTimerRef = useRef<number | null>(null);
  // 已放行挂载 <img> 的截图集合，按 resetKey（项目|筛选）归位：
  // 同范围数据刷新不重置（已加载的图不卸载，避免闪烁与重复下载），
  // 换范围时清空从头分批，不让上个范围的额度带进新范围
  const [releasedImgs, setReleasedImgs] = useState<{
    key: string;
    ids: Set<string>;
  }>(() => ({ key: resetKey, ids: new Set() }));
  const imgTimerRef = useRef<number | null>(null);
  // 浮动工具栏实测高度：适应全部的顶部净空按它算（窄屏折行变高时同步加大）
  const toolbarRef = useRef<HTMLDivElement>(null);
  const [toolbarH, setToolbarH] = useState(FIT_TOP_INSET - 28);
  useEffect(() => {
    const el = toolbarRef.current;
    if (!el) return;
    const measure = () =>
      setToolbarH(el.getBoundingClientRect().height);
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => observer.disconnect();
  }, []);
  if (releasedImgs.key !== resetKey) {
    setReleasedImgs({ key: resetKey, ids: new Set() });
  }

  // 切项目/筛选：清掉选中、草稿、框选态（视图重置在下方 effect）
  const [prevResetKey, setPrevResetKey] = useState(resetKey);
  if (resetKey !== prevResetKey) {
    setPrevResetKey(resetKey);
    setSelectedId(null);
    setHoverId(null);
    setFlashId(null);
    setDraft(null);
    setDrawMode(false);
  }

  const screenById = useMemo(
    () => new Map(screens.map((s) => [s.id, s])),
    [screens],
  );

  // aspectOf 供 pickCanvasColumns/layoutCanvas 高频调用，必须 O(1)
  const aspectOf = useCallback(
    (screenId: string): number | null => {
      const screen = screenById.get(screenId);
      if (!screen) return null;
      return (
        aspectFromSize(screen.width, screen.height) ??
        measured[screenId] ??
        null
      );
    },
    [screenById, measured],
  );

  // 列数随容器宽高比重选，让世界形状贴近容器；只在列数变化时重排，
  // 缩放窗口过程中列数不变就不会重算布局。参与评估的容器高度扣掉
  // 工具栏净空，与「适应全部」实际可用的区域一致
  const columns = useMemo(
    () =>
      pickCanvasColumns(screens, aspectOf, {
        width: size.width,
        height: Math.max(size.height - toolbarH - TOOLBAR_GAP, 1),
      }),
    [screens, aspectOf, size, toolbarH],
  );
  const layout = useMemo(
    () => layoutCanvas(screens, aspectOf, columns),
    [screens, aspectOf, columns],
  );
  const cardRectById = useMemo(
    () => new Map(layout.cards.map((c) => [c.screenId, c])),
    [layout.cards],
  );

  // 视图重置：新范围（项目/筛选）第一次有卡片且量到容器尺寸时适应全部
  const fittedKeyRef = useRef<string | null>(null);
  useEffect(() => {
    if (fittedKeyRef.current === resetKey) return;
    if (size.width <= 0 || layout.cards.length === 0) return;
    fittedKeyRef.current = resetKey;
    jumpTo(fitViewport(layout.bounds, size, FIT_PADDING, toolbarH + TOOLBAR_GAP));
  }, [resetKey, size, layout, toolbarH, jumpTo]);

  // 筛选/数据变化把内容整体挪出视口时自动适应全部，避免整屏空白；
  // 只在布局变化时评估，手动平移到空白处不会触发
  const layoutRef = useRef(layout);
  useEffect(() => {
    if (layoutRef.current === layout) return;
    layoutRef.current = layout;
    if (size.width <= 0 || layout.cards.length === 0) return;
    const view = visibleWorldRect(viewport, size, 0);
    if (!layout.cards.some((card) => rectsIntersect(card.rect, view))) {
      moveTo(
        fitViewport(layout.bounds, size, FIT_PADDING, toolbarH + TOOLBAR_GAP),
      );
    }
  }, [layout, size, viewport, toolbarH, moveTo]);

  // 草稿只认它画上去的那张截图：截图被覆盖（content_url 变化）或被过滤出画布时草稿作废
  useEffect(() => {
    if (!draft) return;
    const screen = screenById.get(draft.screenId);
    if (!screen || screen.content_url !== draft.contentUrl) {
      setDraft(null);
      toast.info("截图已更新，标注草稿已取消");
    }
  }, [draft, screenById]);

  // 画布叠加与面板共同的状态范围
  const inScope = useCallback(
    (a: ScreenshotAnnotation) =>
      (a.status === "open" && statusScope.open) ||
      (a.status === "resolved" && statusScope.resolved),
    [statusScope],
  );

  // 标注按所属截图归到卡片上（只画状态范围内的）
  const annotationsByScreen = useMemo(() => {
    const map = new Map<string, ScreenshotAnnotation[]>();
    for (const a of annotations) {
      if (!inScope(a)) continue;
      if (!screenById.has(a.screen_id)) continue;
      const list = map.get(a.screen_id);
      if (list) list.push(a);
      else map.set(a.screen_id, [a]);
    }
    return map;
  }, [annotations, screenById, inScope]);

  const outdatedOpenCounts = useMemo(
    () => countOutdatedOpen(annotations),
    [annotations],
  );

  // 面板范围 = 维度范围截图（不随搜索收窄，与批量按钮同源）内的状态范围内
  // 标注；新的在上。画布叠加另行按搜索后的 screenById 收敛
  const scopeAnnotations = useMemo(
    () =>
      annotations
        .filter(
          (a) => scopeScreenIds.has(a.screen_id) && inScope(a),
        )
        .sort((a, b) => b.created_at.localeCompare(a.created_at)),
    [annotations, scopeScreenIds, inScope],
  );

  // 标注序号角标与面板条目同序，画布矩形与列表一眼对上
  const annotationIndex = useMemo(
    () => new Map(scopeAnnotations.map((a, i) => [a.id, i + 1])),
    [scopeAnnotations],
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

  // 定位：平移缩放到卡片并闪烁
  const locate = useCallback(
    (annotation: ScreenshotAnnotation) => {
      const screen = screenById.get(annotation.screen_id);
      if (!screen) {
        toast.info("该标注所在的截图不在当前画布范围内");
        return;
      }
      if (!inScope(annotation)) {
        toast.info("该标注不在当前状态筛选范围内，请调整顶部筛选条");
        return;
      }
      setSelectedId(annotation.id);
      setDrawMode(false);
      focusCard(screen.id);
      flash(annotation.id);
    },
    [screenById, inScope, focusCard, flash],
  );

  // URL / 外部触发的定位请求：等数据与容器尺寸就绪后只处理一次
  const handledNonceRef = useRef<number | null>(null);
  useEffect(() => {
    if (!locateRequest || handledNonceRef.current === locateRequest.nonce) {
      return;
    }
    if (!annotationsReady || size.width <= 0 || screens.length === 0) return;
    const target = annotations.find((a) => a.id === locateRequest.annotationId);
    // 所在截图还没进入画布范围（放宽筛选的更新未生效）时继续等
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
    (screenId: string, width: number, height: number) => {
      setMeasured((prev) =>
        prev[screenId] ? prev : { ...prev, [screenId]: height / width },
      );
    },
    [],
  );

  const handleSelectAnnotation = useCallback((annotationId: string) => {
    setSelectedId(annotationId);
    setPanelOpen(true);
  }, []);

  const handleDrawEnd = useCallback(
    (screen: ScreenshotScreen, rect: Rect) => {
      setDraft({
        screenId: screen.id,
        contentUrl: screen.content_url,
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
        screenId: draft.screenId,
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

  const fitAll = () =>
    moveTo(fitViewport(layout.bounds, size, FIT_PADDING, toolbarH + TOOLBAR_GAP));

  // 只渲染与视口（外扩一屏）相交的卡片；memo 保证不相关渲染
  // （选中/悬停）不会产生新数组、打断分批挂载计时器
  const visibleCards = useMemo(() => {
    const margin = Math.max(size.width, size.height);
    const rect = visibleWorldRect(viewport, size, margin);
    return layout.cards.filter((card) => rectsIntersect(card.rect, rect));
  }, [layout.cards, viewport, size]);

  // 深缩/适应全部时几乎所有卡片都进视口，一次性挂几百张原图会瞬时打满
  // 带宽与解码。每范围前 30 张即时放行，之后每 80ms 放行 30 张；
  // 放行进集合后平移换位不会把 <img> 挤回占位。计时器不按帧清理——
  // 手势进行中分批照常推进；回调用 ref 里的最新可见卡片，scope 变了
  // 由 key 守卫兜住。卸载兜底在下方单独 effect
  const visibleCardsRef = useRef(visibleCards);
  useEffect(() => {
    visibleCardsRef.current = visibleCards;
  });
  useEffect(() => {
    const missing = visibleCards.filter(
      (c) => !releasedImgs.ids.has(c.screenId),
    );
    if (missing.length === 0) return;
    const room = IMAGE_MOUNT_BATCH - releasedImgs.ids.size;
    if (room > 0) {
      const seed = missing.slice(0, room).map((c) => c.screenId);
      setReleasedImgs((prev) =>
        prev.key !== resetKey
          ? prev
          : { key: resetKey, ids: new Set([...prev.ids, ...seed]) },
      );
      return;
    }
    if (imgTimerRef.current !== null) return;
    imgTimerRef.current = window.setTimeout(() => {
      imgTimerRef.current = null;
      setReleasedImgs((prev) => {
        if (prev.key !== resetKey) return prev;
        const ids = new Set(prev.ids);
        let added = 0;
        for (const c of visibleCardsRef.current) {
          if (ids.has(c.screenId)) continue;
          ids.add(c.screenId);
          if (++added >= IMAGE_MOUNT_BATCH) break;
        }
        return { key: resetKey, ids };
      });
    }, IMAGE_MOUNT_INTERVAL_MS);
  }, [releasedImgs, visibleCards, resetKey]);
  // 范围切换/卸载时清掉飞行中的批次计时器：它触发时 key 守卫只会跳过
  // 更新，imgTimerRef 置空后没有渲染接力，新范围会停在首批不再放行
  useEffect(
    () => () => {
      if (imgTimerRef.current !== null) {
        window.clearTimeout(imgTimerRef.current);
        imgTimerRef.current = null;
      }
    },
    [resetKey],
  );

  const drawEnabled = drawMode && !spaceHeld;

  // 深缩时标题跟着缩没；反向放大让屏幕上保持 ≥14px 可读字号
  const titleScale = Math.max(1, GROUP_TITLE_MIN_PX / (24 * viewport.scale));

  const cursor = panning ? "grabbing" : spaceHeld ? "grab" : undefined;

  return (
    <div
      className="relative flex h-full min-h-0 overflow-hidden bg-muted/20"
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
        onClick={(e) => {
          const target = e.target as HTMLElement;
          if (target.closest("[data-canvas-card], [data-no-pan]")) return;
          setSelectedId(null);
        }}
      >
        <div
          className="absolute top-0 left-0 origin-top-left will-change-transform"
          style={
            {
              // --canvas-zoom 是缩放率的唯一来源：scale 直接读它，标注皮肤
              // 用 calc(Npx / var(--canvas-zoom)) 反算同一变量，缩放过渡被
              // 新目标打断重启也不会与渲染缩放失配；平移走独立的 translate
              // 过渡。皮肤尺寸全程不需要卡片重渲染
              translate: `${viewport.x}px ${viewport.y}px`,
              scale: `var(${CANVAS_ZOOM_VAR})`,
              transition: smooth
                ? `translate 320ms ease, ${CANVAS_ZOOM_VAR} 320ms ease`
                : undefined,
              [CANVAS_ZOOM_VAR]: String(viewport.scale),
            } as CSSProperties
          }
        >
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
                imageEnabled={releasedImgs.ids.has(card.screenId)}
                aspectKnown={aspectOf(screen.id) !== null}
                annotations={cardAnnotations}
                annotationIndex={annotationIndex}
                outdatedOpenCount={outdatedOpenCounts.get(screen.id) ?? 0}
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

          {/* 分区标题渲染在卡片之后：统一的悬浮标签样式，深缩时
              反放大保持可读并放宽宽度不被 titleRect 截断。以左上为
              原点反放大：标签落回本组首行，不再盖住上一组尾行 */}
          {layout.groups.map((group) => (
            <div
              key={group.key}
              className="pointer-events-none absolute flex items-end gap-3 pb-3"
              style={{
                left: group.titleRect.x,
                top: group.titleRect.y,
                width:
                  titleScale > 1 ? "max-content" : group.titleRect.width,
                height: group.titleRect.height,
                transform: `scale(${titleScale})`,
                transformOrigin: "left top",
              }}
            >
              <h3
                className={cn(
                  "rounded bg-background/95 px-1.5 py-0.5 text-2xl font-semibold shadow-sm ring-1 ring-black/10",
                  titleScale > 1 ? "whitespace-nowrap" : "truncate",
                )}
              >
                {group.title}
              </h3>
              <span className="rounded bg-background/95 px-1 py-0.5 text-sm text-muted-foreground shadow-sm ring-1 ring-black/10">
                {group.count}
              </span>
            </div>
          ))}
        </div>

        <CanvasToolbar
          ref={toolbarRef}
          scale={viewport.scale}
          drawMode={drawMode}
          panelOpen={panelOpen}
          openCount={openCount}
          onToggleDraw={toggleDraw}
          onTogglePanel={() => setPanelOpen((open) => !open)}
          onZoomIn={() => zoomByFactor(1.25)}
          onZoomOut={() => zoomByFactor(0.8)}
          onFit={fitAll}
          onLaunchThread={onLaunchThread}
          onCopyPrompt={onCopyPrompt}
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
            statusFilter={
              // 面板 tab 与 URL 状态过滤双向同源：范围只含一个状态时锁定该 tab
              statusScope.open && !statusScope.resolved
                ? "open"
                : !statusScope.open && statusScope.resolved
                  ? "resolved"
                  : "all"
            }
            onStatusFilterChange={(filter) => {
              onStatusScopeChange({
                open: filter !== "resolved",
                resolved: filter !== "open",
              });
            }}
            scopeFilter={scopeFilter}
            outdatedCount={
              [...outdatedOpenCounts.values()].reduce((a, b) => a + b, 0)
            }
            selectedId={selectedId}
            hoverId={hoverId}
            flashId={flashId}
            onHover={setHoverId}
            onSelect={setSelectedId}
            onLocate={locate}
          />
        </aside>
      )}
    </div>
  );
}
