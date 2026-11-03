import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HTTPError, type NormalizedOptions } from "ky";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { CloseAllDoneButton, formatBatchCloseError } from "./close-all-done-button";

const { closeMutateAsync, toastWarning, toastInfo, toastSuccess, toastError } = vi.hoisted(() => ({
  closeMutateAsync: vi.fn(),
  toastWarning: vi.fn(),
  toastInfo: vi.fn(),
  toastSuccess: vi.fn(),
  toastError: vi.fn(),
}));

vi.mock("@/lib/hooks/use-issues", () => ({
  useBatchClosePreviewCount: () => ({
    data: 2,
    isLoading: false,
  }),
  useCloseIssuesBatch: () => ({
    mutateAsync: closeMutateAsync,
    isPending: false,
  }),
}));

vi.mock("sonner", () => ({
  toast: {
    info: toastInfo,
    warning: toastWarning,
    success: toastSuccess,
    error: toastError,
  },
}));

describe("CloseAllDoneButton select label", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("shows source label in the closed select trigger", async () => {
    const user = userEvent.setup();
    render(<CloseAllDoneButton projectId="proj-1" />);

    await user.click(screen.getByRole("button", { name: /关闭全部/ }));

    const trigger = screen.getByRole("combobox");
    expect(trigger).toHaveTextContent("内部问题");
    expect(trigger).not.toHaveTextContent(/^internal$/);
  });
});

// INT-185（复审 R2）：failures[].error 是服务端分类文案，混合批次可能同时
// 出现多类原因，去重后全部展示，避免首条（内部行恒先处理）掩盖 GitHub 类别。
describe("CloseAllDoneButton failure toast", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("surfaces all distinct failure reasons with refs", async () => {
    closeMutateAsync.mockResolvedValue({
      total: 3,
      succeeded: 1,
      failed: 3,
      failures: [
        { id: "i-1", reference: "INT-1", error: "该问题已在其他操作中被更新，已跳过关闭" },
        { id: "i-5", reference: "INT-5", error: "关闭结果未确认：GitHub 侧可能已关闭，请刷新查看" },
        { id: "i-6", reference: "INT-6", error: "关闭结果未确认：GitHub 侧可能已关闭，请刷新查看" },
      ],
    });
    const user = userEvent.setup();
    render(<CloseAllDoneButton projectId="proj-1" />);
    await user.click(screen.getByRole("button", { name: /关闭全部/ }));
    await user.click(screen.getByRole("button", { name: "确认关闭" }));
    await waitFor(() => {
      expect(toastWarning).toHaveBeenCalledWith(
        "已关闭 1 个问题，3 个失败：该问题已在其他操作中被更新，已跳过关闭；关闭结果未确认：GitHub 侧可能已关闭，请刷新查看（INT-1、INT-5、INT-6）",
      );
    });
  });

  it("keeps the count-only copy when failures carry no error", async () => {
    closeMutateAsync.mockResolvedValue({
      total: 1,
      succeeded: 0,
      failed: 1,
      failures: [{ id: "i-1", reference: "INT-1" }],
    });
    const user = userEvent.setup();
    render(<CloseAllDoneButton projectId="proj-1" />);
    await user.click(screen.getByRole("button", { name: /关闭全部/ }));
    await user.click(screen.getByRole("button", { name: "确认关闭" }));
    await waitFor(() => {
      expect(toastWarning).toHaveBeenCalledWith("已关闭 0 个问题，1 个失败（INT-1）");
    });
  });
});

// INT-185：超时与取消的文案分两层——ky 超时提示刷新确认，HTTPError 仍优先
// 取服务端返回的 message。
describe("formatBatchCloseError", () => {
  it("maps TimeoutError to a refresh hint", async () => {
    const err = new Error("Request timed out");
    err.name = "TimeoutError";
    await expect(formatBatchCloseError(err)).resolves.toBe(
      "请求超时：部分问题可能已关闭，请刷新看板确认结果",
    );
  });

  it("keeps HTTPError body message", async () => {
    const response = new Response(JSON.stringify({ message: "项目尚未关联 GitHub 仓库" }), {
      status: 400,
      headers: { "content-type": "application/json" },
    });
    const request = new Request("https://fast-ship.test/api/projects/p1/issues/batch-close", {
      method: "POST",
    });
    const options = { method: "POST" } as NormalizedOptions;
    await expect(formatBatchCloseError(new HTTPError(response, request, options))).resolves.toBe(
      "项目尚未关联 GitHub 仓库",
    );
  });

  it("falls back to generic copy for other errors", async () => {
    await expect(formatBatchCloseError(new Error("boom"))).resolves.toBe("批量关闭失败");

    const request = new Request("https://fast-ship.test/api/projects/p1/issues/batch-close", {
      method: "POST",
    });
    const options = { method: "POST" } as NormalizedOptions;
    const html = new Response("<html>oops</html>", { status: 502 });
    await expect(formatBatchCloseError(new HTTPError(html, request, options))).resolves.toBe(
      "批量关闭失败",
    );
  });
});
