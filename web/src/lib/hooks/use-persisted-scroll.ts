import { useCallback, useLayoutEffect, useRef, useState } from "react";
import { getSavedScroll, setSavedScroll } from "@/lib/scroll-positions";

type ScrollAxis = "top" | "left";

function readScroll(el: HTMLElement, axis: ScrollAxis) {
  return axis === "top" ? el.scrollTop : el.scrollLeft;
}

function writeScroll(el: HTMLElement, axis: ScrollAxis, value: number) {
  if (axis === "top") {
    el.scrollTop = value;
    return;
  }
  el.scrollLeft = value;
}

export function usePersistedScroll<T extends HTMLElement>(
  storageKey: string,
  options?: {
    ready?: boolean;
    axis?: ScrollAxis;
  },
) {
  const ready = options?.ready ?? true;
  const axis = options?.axis ?? "top";
  const [node, setNode] = useState<T | null>(null);
  const ref = useCallback((el: T | null) => {
    setNode((current) => (current === el ? current : el));
  }, []);
  const readyRef = useRef(ready);
  useLayoutEffect(() => {
    readyRef.current = ready;
  }, [ready]);
  const lastKnownRef = useRef(0);
  const lastAppliedRef = useRef(0);
  const pendingRef = useRef<number | null>(null);

  useLayoutEffect(() => {
    lastKnownRef.current = getSavedScroll(storageKey);
    pendingRef.current = lastKnownRef.current;
    lastAppliedRef.current = 0;
  }, [storageKey]);

  useLayoutEffect(() => {
    if (!node) return;

    const handleScroll = () => {
      const current = readScroll(node, axis);
      if (
        pendingRef.current != null &&
        Math.abs(current - lastAppliedRef.current) > 1
      ) {
        pendingRef.current = null;
      }
      if (!readyRef.current) return;
      lastKnownRef.current = current;
      setSavedScroll(storageKey, current);
    };

    node.addEventListener("scroll", handleScroll, { passive: true });
    return () => {
      if (readyRef.current) {
        setSavedScroll(storageKey, lastKnownRef.current);
      }
      node.removeEventListener("scroll", handleScroll);
    };
  }, [node, storageKey, axis]);

  useLayoutEffect(() => {
    if (!node || !ready) return;

    const apply = () => {
      const target = pendingRef.current;
      if (target == null) return;
      writeScroll(node, axis, target);
      const current = readScroll(node, axis);
      lastAppliedRef.current = current;
      lastKnownRef.current = current;
      if (Math.abs(current - target) <= 1) {
        pendingRef.current = null;
      }
    };

    apply();

    const resizeObserver = new ResizeObserver(() => apply());
    const mutationObserver = new MutationObserver(() => {
      if (pendingRef.current == null) return;
      for (const child of node.children) {
        resizeObserver.observe(child);
      }
      apply();
    });

    resizeObserver.observe(node);
    for (const child of node.children) {
      resizeObserver.observe(child);
    }
    mutationObserver.observe(node, { childList: true, subtree: true });

    return () => {
      resizeObserver.disconnect();
      mutationObserver.disconnect();
    };
  }, [node, storageKey, ready, axis]);

  return ref;
}
