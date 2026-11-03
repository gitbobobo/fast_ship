import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HTTPError, type NormalizedOptions } from "ky";
import type { ReactElement } from "react";
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

// INT-185（复审 R2/R3）：服务端失败文案已收敛为少量固定类别，按文案分组
// 计数全部展示（每组取样例引用），不做容量截断——截断是三轮审查反例的根因；
// 明细差额（failed > failures.length）显式标注。
describe("CloseAllDoneButton failure toast", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  // description 现为 ReactNode（逐行 div，R4）；渲染后逐行断言文本。
  async function closeAndCaptureDescription() {
    const user = userEvent.setup();
    render(<CloseAllDoneButton projectId="proj-1" />);
    await user.click(screen.getByRole("button", { name: /关闭全部/ }));
    await user.click(screen.getByRole("button", { name: "确认关闭" }));
    await waitFor(() => expect(toastWarning).toHaveBeenCalled());
    const description = toastWarning.mock.calls[0][1]
      .description as ReactElement;
    return description;
  }

  it("groups mixed failure categories with per-group sample refs", async () => {
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
    const description = await closeAndCaptureDescription();
    render(description);
    expect(screen.getByText("该问题已在其他操作中被更新，已跳过关闭 ×1（如 INT-1）")).toBeInTheDocument();
    expect(screen.getByText("关闭结果未确认：GitHub 侧可能已关闭，请刷新查看 ×2（如 INT-5）")).toBeInTheDocument();
  });

  it("shows every category even when there are four, plus the unlisted remainder", async () => {
    closeMutateAsync.mockResolvedValue({
      total: 204,
      succeeded: 0,
      failed: 204,
      failures: [
        { id: "i-1", reference: "INT-1", error: "该问题已在其他操作中被更新，已跳过关闭" },
        { id: "i-2", reference: "INT-2", error: "关闭结果未确认：GitHub 侧可能已关闭，请刷新查看" },
        { id: "i-3", reference: "INT-3", error: "更新 GitHub Issue 失败：GitHub API 限流，请稍后重试" },
        { id: "i-4", reference: "INT-4", error: "GitHub API 限流，未尝试关闭" },
      ],
    });
    const description = await closeAndCaptureDescription();
    render(description);
    // 四类全部可见，不做容量截断；差额显式标注。
    expect(screen.getByText("GitHub API 限流，未尝试关闭 ×1（如 INT-4）")).toBeInTheDocument();
    expect(screen.getByText("另有 200 条未列明细")).toBeInTheDocument();
  });

  it("groups failures without error under 原因未说明 with the sample ref", async () => {
    closeMutateAsync.mockResolvedValue({
      total: 1,
      succeeded: 0,
      failed: 1,
      failures: [{ id: "i-1", reference: "INT-1" }],
    });
    const description = await closeAndCaptureDescription();
    render(description);
    expect(screen.getByText("原因未说明 ×1（如 INT-1）")).toBeInTheDocument();
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
    const html = new Response("<html>oops</html>", { status: 400 });
    await expect(formatBatchCloseError(new HTTPError(html, request, options))).resolves.toBe(
      "批量关闭失败",
    );
  });

  // INT-185（复审 R3）：网关截断（502/503/504，通常无 JSON 正文）与网络中断
  // 都是「服务端可能已关闭一部分」的场景，按刷新确认处理；带 JSON message 的
  // 服务端错误仍优先。
  it("maps gateway timeouts to the refresh hint", async () => {
    const request = new Request("https://fast-ship.test/api/projects/p1/issues/batch-close", {
      method: "POST",
    });
    const options = { method: "POST" } as NormalizedOptions;
    for (const status of [502, 503, 504]) {
      const html = new Response("<html>gateway oops</html>", { status });
      await expect(formatBatchCloseError(new HTTPError(html, request, options))).resolves.toBe(
        "请求超时：部分问题可能已关闭，请刷新看板确认结果",
      );
    }
  });

  it("maps network failures to the refresh hint", async () => {
    const error = new TypeError("fetch failed");
    await expect(formatBatchCloseError(error)).resolves.toBe(
      "请求超时：部分问题可能已关闭，请刷新看板确认结果",
    );
  });

  // INT-185（复审 R4）：2xx 响应体截断/非 JSON 时 ky 的 .json() 抛
  // SyntaxError——请求已到达服务端、关闭可能已生效，按刷新确认处理。
  it("maps 2xx response parse failures to the refresh hint", async () => {
    await expect(formatBatchCloseError(new SyntaxError("Unexpected token < in JSON"))).resolves.toBe(
      "请求超时：部分问题可能已关闭，请刷新看板确认结果",
    );
  });
});
