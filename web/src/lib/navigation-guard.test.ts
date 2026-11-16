import { afterEach, describe, expect, it, vi } from "vitest";

function stubConfirm(returnValue: boolean) {
  const confirmMock = vi.fn().mockReturnValue(returnValue);
  vi.stubGlobal("confirm", confirmMock);
  return confirmMock;
}
import {
  confirmNavigationLeave,
  registerNavigationGuard,
} from "./navigation-guard";

// 守卫集合是模块级单例：每个用例后确保清空，避免串味
afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  // 反登记由各用例负责；这里兜底确认已清空
  expect(confirmNavigationLeave()).toBe(true);
});

describe("confirmNavigationLeave", () => {
  it("allows navigation when no guard is registered", () => {
    expect(confirmNavigationLeave()).toBe(true);
  });

  it("allows navigation when guards return null", () => {
    const unregister = registerNavigationGuard(() => null);
    try {
      expect(confirmNavigationLeave()).toBe(true);
      expect(globalThis.confirm).toBeUndefined();
    } finally {
      unregister();
    }
  });

  it("blocks navigation when the user cancels the confirmation", () => {
    const confirmMock = vi.fn().mockReturnValue(false);
    vi.stubGlobal("confirm", confirmMock);
    const unregister = registerNavigationGuard(() => "有未保存的修改");
    try {
      expect(confirmNavigationLeave()).toBe(false);
      expect(confirmMock).toHaveBeenCalledWith("有未保存的修改");
    } finally {
      unregister();
    }
  });

  it("proceeds when the user accepts the confirmation", () => {
    stubConfirm(true);
    const unregister = registerNavigationGuard(() => "有未保存的修改");
    try {
      expect(confirmNavigationLeave()).toBe(true);
    } finally {
      unregister();
    }
  });

  it("asks every registered guard and stops at the first refusal", () => {
    const confirmMock = vi
      .fn()
      .mockReturnValueOnce(true)
      .mockReturnValueOnce(false);
    vi.stubGlobal("confirm", confirmMock);
    const calls: string[] = [];
    const u1 = registerNavigationGuard(() => {
      calls.push("first");
      return "第一个守卫";
    });
    const u2 = registerNavigationGuard(() => {
      calls.push("second");
      return "第二个守卫";
    });
    try {
      expect(confirmNavigationLeave()).toBe(false);
      expect(calls).toEqual(["first", "second"]);
      expect(confirmMock).toHaveBeenCalledTimes(2);
    } finally {
      u1();
      u2();
    }
  });

  it("stops asking after the guard is unregistered", () => {
    const confirmMock = stubConfirm(true);
    const unregister = registerNavigationGuard(() => "有未保存的修改");
    unregister();
    expect(confirmNavigationLeave()).toBe(true);
    expect(confirmMock).not.toHaveBeenCalled();
  });
});
