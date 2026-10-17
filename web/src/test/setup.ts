import { vi } from "vitest";
import "@testing-library/jest-dom/vitest";

class ResizeObserverMock {
  observe() {}
  unobserve() {}
  disconnect() {}
}

if (!globalThis.ResizeObserver) {
  globalThis.ResizeObserver = ResizeObserverMock as typeof ResizeObserver;
}

// happy-dom 的 localStorage 实现与 zustand persist 不完全兼容，提供完整 mock
const localStorageMock: Storage = {
  getItem: vi.fn(() => null),
  setItem: vi.fn(),
  removeItem: vi.fn(),
  clear: vi.fn(),
  length: 0,
  key: vi.fn(() => null),
};

Object.defineProperty(window, "localStorage", {
  value: localStorageMock,
  writable: true,
});

// 测试环境不联网：没有显式 mock 的请求在这里快速失败。否则 happy-dom 会把
// 相对路径解析成 http://localhost:3000 并真的去拨号，ECONNREFUSED 的聚合错误
// 会被 Node 当成未处理拒绝刷进测试输出。需要断言网络行为的用例自行
// `vi.stubGlobal("fetch", ...)` 覆盖。
vi.stubGlobal("fetch", async () => {
  return new Response(
    JSON.stringify({ code: 50300, message: "测试环境不联网" }),
    { status: 503, headers: { "content-type": "application/json" } },
  );
});
