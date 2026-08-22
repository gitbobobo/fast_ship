import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { usePersistedScroll } from "./use-persisted-scroll";
import {
  getSavedScroll,
  resetScrollPositions,
  setSavedScroll,
} from "@/lib/scroll-positions";

const scrollTopValues = new WeakMap<HTMLElement, number>();
const scrollLeftValues = new WeakMap<HTMLElement, number>();

function installScrollStubs() {
  Object.defineProperty(HTMLElement.prototype, "scrollTop", {
    configurable: true,
    get(this: HTMLElement) {
      return scrollTopValues.get(this) ?? 0;
    },
    set(this: HTMLElement, value: number) {
      scrollTopValues.set(this, Number(value) || 0);
    },
  });
  Object.defineProperty(HTMLElement.prototype, "scrollLeft", {
    configurable: true,
    get(this: HTMLElement) {
      return scrollLeftValues.get(this) ?? 0;
    },
    set(this: HTMLElement, value: number) {
      scrollLeftValues.set(this, Number(value) || 0);
    },
  });
}

function Scroller({
  storageKey,
  ready = true,
  axis = "top",
}: {
  storageKey: string;
  ready?: boolean;
  axis?: "top" | "left";
}) {
  const ref = usePersistedScroll<HTMLDivElement>(storageKey, { ready, axis });
  return (
    <div ref={ref} data-testid="scroller">
      content
    </div>
  );
}

describe("usePersistedScroll", () => {
  beforeEach(() => {
    resetScrollPositions();
    installScrollStubs();
  });

  afterEach(() => {
    resetScrollPositions();
  });

  it("restores the previous vertical offset after remount", () => {
    const { unmount } = render(<Scroller storageKey="todo" />);
    const first = screen.getByTestId("scroller");
    first.scrollTop = 640;
    fireEvent.scroll(first);
    unmount();

    render(<Scroller storageKey="todo" />);
    expect(screen.getByTestId("scroller").scrollTop).toBe(640);
  });

  it("restores the previous horizontal offset after remount", () => {
    const { unmount } = render(
      <Scroller storageKey="project-1" axis="left" />,
    );
    const first = screen.getByTestId("scroller");
    first.scrollLeft = 240;
    fireEvent.scroll(first);
    unmount();

    render(<Scroller storageKey="project-1" axis="left" />);
    expect(screen.getByTestId("scroller").scrollLeft).toBe(240);
  });

  it("does not overwrite a saved offset while content is not ready", () => {
    setSavedScroll("todo", 500);

    const { unmount } = render(<Scroller storageKey="todo" ready={false} />);
    expect(screen.getByTestId("scroller").scrollTop).toBe(0);

    unmount();
    expect(getSavedScroll("todo")).toBe(500);
  });

  it("applies the saved offset once content becomes ready", () => {
    setSavedScroll("todo", 360);

    const { rerender } = render(<Scroller storageKey="todo" ready={false} />);
    expect(screen.getByTestId("scroller").scrollTop).toBe(0);

    rerender(<Scroller storageKey="todo" ready />);
    expect(screen.getByTestId("scroller").scrollTop).toBe(360);
  });

  it("keeps the previous key's offset when the container is clamped after navigation", () => {
    const { rerender } = render(<Scroller storageKey="list" />);
    const scroller = screen.getByTestId("scroller");
    scroller.scrollTop = 800;
    fireEvent.scroll(scroller);

    scroller.scrollTop = 40;
    rerender(<Scroller storageKey="detail" />);

    expect(getSavedScroll("list")).toBe(800);
    expect(scroller.scrollTop).toBe(0);
  });
});
