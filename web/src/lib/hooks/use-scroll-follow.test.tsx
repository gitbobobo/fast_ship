import { act, fireEvent, render, screen } from "@testing-library/react";
import { useRef } from "react";
import { describe, expect, it } from "vitest";
import { useScrollFollow } from "./use-scroll-follow";

const scrollTops = new WeakMap<HTMLElement, number>();
Object.defineProperty(HTMLElement.prototype, "scrollTop", {
  configurable: true,
  get(this: HTMLElement) {
    return scrollTops.get(this) ?? 0;
  },
  set(this: HTMLElement, value: number) {
    scrollTops.set(this, Number(value) || 0);
  },
});

const geometry = { scrollHeight: 1000, clientHeight: 400 };

function Chat({ version, ready = true }: { version: string; ready?: boolean }) {
  const anchor = useRef<HTMLDivElement>(null);
  useScrollFollow(anchor, version, ready);
  return (
    <div data-testid="scroller" style={{ overflowY: "auto" }}>
      <div ref={anchor}>content</div>
    </div>
  );
}

function setup() {
  const view = render(<Chat version="1" ready={false} />);
  const scroller = screen.getByTestId("scroller");
  Object.defineProperty(scroller, "scrollHeight", {
    configurable: true,
    get: () => geometry.scrollHeight,
  });
  Object.defineProperty(scroller, "clientHeight", {
    configurable: true,
    get: () => geometry.clientHeight,
  });
  return { view, scroller };
}

describe("useScrollFollow", () => {
  it("scrolls to the bottom once content is ready", () => {
    geometry.scrollHeight = 1000;
    const { view, scroller } = setup();
    expect(scroller.scrollTop).toBe(0);
    view.rerender(<Chat version="1" />);
    expect(scroller.scrollTop).toBe(1000);
  });

  it("follows new content while the user stays near the bottom", () => {
    geometry.scrollHeight = 1000;
    const { view, scroller } = setup();
    view.rerender(<Chat version="1" />);
    scroller.scrollTop = 560;
    fireEvent.scroll(scroller);

    geometry.scrollHeight = 1200;
    view.rerender(<Chat version="2" />);
    expect(scroller.scrollTop).toBe(1200);
  });

  it("leaves the user alone once they scrolled up", () => {
    geometry.scrollHeight = 1000;
    const { view, scroller } = setup();
    view.rerender(<Chat version="1" />);
    act(() => {
      scroller.scrollTop = 300;
      fireEvent.scroll(scroller);
    });

    geometry.scrollHeight = 1200;
    view.rerender(<Chat version="2" />);
    expect(scroller.scrollTop).toBe(300);

    act(() => {
      scroller.scrollTop = 800;
      fireEvent.scroll(scroller);
    });
    geometry.scrollHeight = 1400;
    view.rerender(<Chat version="3" />);
    expect(scroller.scrollTop).toBe(1400);
  });
});
