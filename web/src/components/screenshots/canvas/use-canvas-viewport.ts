import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type PointerEvent as ReactPointerEvent,
  type RefObject,
} from "react";
import {
  clampScale,
  wheelZoomFactor,
  zoomAt,
  type Size,
  type Viewport,
} from "@/lib/screenshot-canvas";

/** 点击与拖拽的分界：位移超过该像素即视为拖拽，随后的 click 被吞掉 */
const DRAG_THRESHOLD = 4;

export function isTypingTarget(target: EventTarget | null): boolean {
  return (
    target instanceof HTMLElement &&
    !!target.closest("input, textarea, select, [contenteditable]")
  );
}

type Gesture =
  | { kind: "pan"; startX: number; startY: number; lastX: number; lastY: number }
  | { kind: "pinch"; lastDist: number; lastMidX: number; lastMidY: number };

/**
 * 画布视口：平移（拖空白 / 空格+拖 / 中键 / 单指）、缩放（滚轮、ctrl+滚轮、
 * 双指捏合）。视口只存 translate+scale，世界容器按它做 CSS transform。
 * 带 data-no-pan 的元素（输入框、操作按钮）上按下不会触发平移。
 */
export function useCanvasViewport(
  containerRef: RefObject<HTMLDivElement | null>,
  enabled: boolean,
) {
  const [viewport, setViewportState] = useState<Viewport>({
    x: 0,
    y: 0,
    scale: 1,
  });
  const viewportRef = useRef(viewport);
  // 程序触发的移动（定位/适应/按钮缩放）带过渡，手势直接跟手
  const [smooth, setSmooth] = useState(false);
  const [size, setSize] = useState<Size>({ width: 0, height: 0 });
  const [spaceHeld, setSpaceHeld] = useState(false);
  const spaceRef = useRef(false);
  const [panning, setPanning] = useState(false);
  const movedRef = useRef(false);
  const pointersRef = useRef(new Map<number, { x: number; y: number }>());
  const gestureRef = useRef<Gesture | null>(null);
  // 手势期间挂在 window 上的监听的统一摘除入口
  const detachRef = useRef<(() => void) | null>(null);

  const commit = useCallback((next: Viewport, animated: boolean) => {
    viewportRef.current = next;
    setViewportState(next);
    setSmooth(animated);
  }, []);

  const moveTo = useCallback(
    (next: Viewport) => commit(next, true),
    [commit],
  );

  const jumpTo = useCallback(
    (next: Viewport) => commit(next, false),
    [commit],
  );

  const zoomByFactor = useCallback(
    (factor: number) => {
      const el = containerRef.current;
      if (!el) return;
      const current = viewportRef.current;
      commit(
        zoomAt(
          current,
          current.scale * factor,
          el.clientWidth / 2,
          el.clientHeight / 2,
        ),
        true,
      );
    },
    [containerRef, commit],
  );

  // 容器尺寸
  useEffect(() => {
    const el = containerRef.current;
    if (!el) return;
    const measure = () =>
      setSize((prev) =>
        prev.width === el.clientWidth && prev.height === el.clientHeight
          ? prev
          : { width: el.clientWidth, height: el.clientHeight },
      );
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => observer.disconnect();
  }, [containerRef]);

  // 滚轮必须非 passive 才能 preventDefault，React 的 onWheel 是 passive 的
  useEffect(() => {
    const el = containerRef.current;
    if (!el || !enabled) return;
    const onWheel = (e: WheelEvent) => {
      e.preventDefault();
      const rect = el.getBoundingClientRect();
      const current = viewportRef.current;
      if (!e.ctrlKey && Math.abs(e.deltaX) > Math.abs(e.deltaY)) {
        commit({ ...current, x: current.x - e.deltaX }, false);
        return;
      }
      const factor = wheelZoomFactor(e.deltaY, e.ctrlKey, e.deltaMode);
      commit(
        zoomAt(
          current,
          current.scale * factor,
          e.clientX - rect.left,
          e.clientY - rect.top,
        ),
        false,
      );
    };
    el.addEventListener("wheel", onWheel, { passive: false });
    return () => el.removeEventListener("wheel", onWheel);
  }, [containerRef, enabled, commit]);

  // 空格按住 = 临时平移模式
  useEffect(() => {
    if (!enabled) return;
    const set = (value: boolean) => {
      spaceRef.current = value;
      setSpaceHeld(value);
    };
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.code !== "Space" || isTypingTarget(e.target)) return;
      e.preventDefault();
      if (!e.repeat) set(true);
    };
    const onKeyUp = (e: KeyboardEvent) => {
      if (e.code !== "Space") return;
      set(false);
    };
    const onBlur = () => set(false);
    window.addEventListener("keydown", onKeyDown);
    window.addEventListener("keyup", onKeyUp);
    window.addEventListener("blur", onBlur);
    return () => {
      window.removeEventListener("keydown", onKeyDown);
      window.removeEventListener("keyup", onKeyUp);
      window.removeEventListener("blur", onBlur);
      set(false);
    };
  }, [enabled]);

  const onWindowMove = useCallback(
    (e: PointerEvent) => {
      const pointers = pointersRef.current;
      const gesture = gestureRef.current;
      if (!pointers.has(e.pointerId) || !gesture) return;
      pointers.set(e.pointerId, { x: e.clientX, y: e.clientY });
      const current = viewportRef.current;

      if (gesture.kind === "pan" && pointers.size === 1) {
        if (
          !movedRef.current &&
          Math.hypot(e.clientX - gesture.startX, e.clientY - gesture.startY) >
            DRAG_THRESHOLD
        ) {
          movedRef.current = true;
          setPanning(true);
        }
        if (!movedRef.current) return;
        commit(
          {
            ...current,
            x: current.x + e.clientX - gesture.lastX,
            y: current.y + e.clientY - gesture.lastY,
          },
          false,
        );
        gesture.lastX = e.clientX;
        gesture.lastY = e.clientY;
      } else if (gesture.kind === "pinch" && pointers.size >= 2) {
        const [a, b] = [...pointers.values()];
        const dist = Math.hypot(a.x - b.x, a.y - b.y);
        const midX = (a.x + b.x) / 2;
        const midY = (a.y + b.y) / 2;
        const el = containerRef.current;
        if (!el || gesture.lastDist <= 0) return;
        const rect = el.getBoundingClientRect();
        const zoomed = zoomAt(
          current,
          clampScale(current.scale * (dist / gesture.lastDist)),
          midX - rect.left,
          midY - rect.top,
        );
        commit(
          {
            ...zoomed,
            x: zoomed.x + midX - gesture.lastMidX,
            y: zoomed.y + midY - gesture.lastMidY,
          },
          false,
        );
        gesture.lastDist = dist;
        gesture.lastMidX = midX;
        gesture.lastMidY = midY;
      }
    },
    [commit, containerRef],
  );

  const onWindowUp = useCallback(
    (e: PointerEvent) => {
      const pointers = pointersRef.current;
      pointers.delete(e.pointerId);
      if (pointers.size === 0) {
        gestureRef.current = null;
        setPanning(false);
        detachRef.current?.();
      } else if (pointers.size === 1) {
        // 捏合松掉一指后，剩下的手指接着平移
        const [rest] = [...pointers.values()];
        gestureRef.current = {
          kind: "pan",
          startX: rest.x,
          startY: rest.y,
          lastX: rest.x,
          lastY: rest.y,
        };
      }
    },
    [],
  );

  useEffect(() => () => detachRef.current?.(), []);

  const onPointerDown = useCallback(
    (e: ReactPointerEvent<HTMLElement>) => {
      if (!enabled) return;
      const forcePan = e.button === 1 || spaceRef.current;
      if (e.pointerType === "mouse" && e.button !== 0 && e.button !== 1) return;
      if (!forcePan && (e.target as HTMLElement).closest("[data-no-pan]")) {
        return;
      }
      if (e.button === 1) e.preventDefault();

      const pointers = pointersRef.current;
      if (pointers.size === 0) {
        movedRef.current = false;
        window.addEventListener("pointermove", onWindowMove);
        window.addEventListener("pointerup", onWindowUp);
        window.addEventListener("pointercancel", onWindowUp);
        detachRef.current = () => {
          window.removeEventListener("pointermove", onWindowMove);
          window.removeEventListener("pointerup", onWindowUp);
          window.removeEventListener("pointercancel", onWindowUp);
          detachRef.current = null;
        };
      }
      pointers.set(e.pointerId, { x: e.clientX, y: e.clientY });

      if (pointers.size === 1) {
        gestureRef.current = {
          kind: "pan",
          startX: e.clientX,
          startY: e.clientY,
          lastX: e.clientX,
          lastY: e.clientY,
        };
      } else if (pointers.size === 2) {
        const [a, b] = [...pointers.values()];
        movedRef.current = true;
        setPanning(true);
        gestureRef.current = {
          kind: "pinch",
          lastDist: Math.hypot(a.x - b.x, a.y - b.y),
          lastMidX: (a.x + b.x) / 2,
          lastMidY: (a.y + b.y) / 2,
        };
      }
    },
    [enabled, onWindowMove, onWindowUp],
  );

  // 拖拽结束后紧跟的 click 不应触发卡片/标注的点击
  const onClickCapture = useCallback((e: React.MouseEvent) => {
    if (movedRef.current) {
      e.stopPropagation();
      movedRef.current = false;
    }
  }, []);

  return {
    viewport,
    smooth,
    size,
    spaceHeld,
    panning,
    moveTo,
    jumpTo,
    zoomByFactor,
    handlers: { onPointerDown, onClickCapture },
  };
}
