import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  type RefObject,
} from "react";

/** 距底部不超过该像素数仍视为「在底部」。 */
export const BOTTOM_THRESHOLD_PX = 100;

export function distanceFromBottom(el: HTMLElement) {
  return el.scrollHeight - el.scrollTop - el.clientHeight;
}

/** 从锚点向上找最近的纵向可滚动祖先（应用布局里是 <main>）。 */
function findScrollParent(anchor: HTMLElement | null) {
  for (let el = anchor?.parentElement; el; el = el.parentElement) {
    const overflowY = getComputedStyle(el).overflowY;
    if (overflowY === "auto" || overflowY === "scroll") return el;
  }
  return null;
}

/**
 * 聊天流的 scroll-follow：ready 后首次渲染滚到底；之后内容有任何增高
 * （新条目、前条更新、异步正文展开等，经 version 变化与容器尺寸双重侦测），
 * 只有用户原本在底部附近才继续跟随，已经上翻则不打扰。
 * 「是否在底」由 scroll 事件记录——内容增长之后再量距离会把增长算成上翻。
 * anchorRef 指向聊天内容容器，用来定位滚动容器并观察其尺寸。
 *
 * 返回的 `scrollToBottom` 供显式动作（如发送消息）强制贴底：它把内部状态标回
 * 「在底部」，随后的内容增长继续跟随，用户上翻阅读历史时发消息也能立刻回到底。
 */
export function useScrollFollow(
  anchorRef: RefObject<HTMLElement | null>,
  version: string,
  ready: boolean,
) {
  const atBottom = useRef(true);
  const started = useRef(false);

  const scrollToBottom = useCallback(() => {
    const container = findScrollParent(anchorRef.current);
    if (!container) return;
    atBottom.current = true;
    started.current = true;
    container.scrollTop = container.scrollHeight;
  }, [anchorRef]);

  useEffect(() => {
    if (!ready) return;
    const container = findScrollParent(anchorRef.current);
    if (!container) return;
    const onScroll = () => {
      atBottom.current = distanceFromBottom(container) <= BOTTOM_THRESHOLD_PX;
    };
    container.addEventListener("scroll", onScroll, { passive: true });
    return () => container.removeEventListener("scroll", onScroll);
  }, [anchorRef, ready]);

  // 容器尺寸变化（任何条目增高、排队横幅、异步正文）都会触发；version 覆盖
  // 不改变尺寸的内容更新。两者都只在原本接近底部时跟随。
  useLayoutEffect(() => {
    if (!ready) return;
    const container = findScrollParent(anchorRef.current);
    const anchor = anchorRef.current;
    if (!container || !anchor) return;
    const follow = () => {
      if (!started.current || atBottom.current) {
        started.current = true;
        atBottom.current = true;
        container.scrollTop = container.scrollHeight;
      }
    };
    follow();
    const observer = new ResizeObserver(() => follow());
    observer.observe(anchor);
    return () => observer.disconnect();
  }, [anchorRef, ready, version]);

  return scrollToBottom;
}
