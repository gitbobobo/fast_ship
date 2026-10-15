import { afterEach, describe, expect, it, vi } from "vitest";
import { uuid } from "./uuid";

const V4 = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

afterEach(() => vi.unstubAllGlobals());

describe("uuid", () => {
  it("返回 UUID v4 且每次不同", () => {
    const a = uuid();
    expect(a).toMatch(V4);
    expect(uuid()).not.toBe(a);
  });

  it("crypto.randomUUID 缺失（HTTP 非安全上下文）时走 getRandomValues", () => {
    let calls = 0;
    vi.stubGlobal("crypto", {
      getRandomValues: (b: Uint8Array) => {
        calls++;
        b.fill(0x11);
        return b;
      },
    });
    const id = uuid();
    expect(calls).toBe(1);
    expect(id).toMatch(V4);
  });

  it("crypto 整体缺失时用 Math.random 兜底，不抛异常", () => {
    vi.stubGlobal("crypto", undefined);
    expect(uuid()).toMatch(V4);
  });
});
