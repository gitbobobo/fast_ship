import { beforeEach, describe, expect, it, vi } from "vitest";

const { copyToClipboardMock, toast } = vi.hoisted(() => ({
  copyToClipboardMock: vi.fn(),
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));

vi.mock("@/lib/utils", () => ({ copyToClipboard: copyToClipboardMock }));
vi.mock("sonner", () => ({ toast }));

import { copyWithToast } from "./copy";

describe("copyWithToast", () => {
  beforeEach(() => {
    copyToClipboardMock.mockReset();
    toast.success.mockClear();
    toast.error.mockClear();
  });

  it("shows the success toast and copies the text when copy succeeds", async () => {
    copyToClipboardMock.mockResolvedValue(undefined);

    await copyWithToast("hello", "已复制");

    expect(copyToClipboardMock).toHaveBeenCalledWith("hello");
    expect(toast.success).toHaveBeenCalledWith("已复制");
    expect(toast.error).not.toHaveBeenCalled();
  });

  it("shows the default error toast when copy fails", async () => {
    copyToClipboardMock.mockRejectedValue(new Error("denied"));

    await copyWithToast("hello", "已复制");

    expect(toast.error).toHaveBeenCalledWith("复制失败");
    expect(toast.success).not.toHaveBeenCalled();
  });

  it("supports a custom error message", async () => {
    copyToClipboardMock.mockRejectedValue(new Error("denied"));

    await copyWithToast("hello", "已复制", "自定义失败");

    expect(toast.error).toHaveBeenCalledWith("自定义失败");
  });
});
