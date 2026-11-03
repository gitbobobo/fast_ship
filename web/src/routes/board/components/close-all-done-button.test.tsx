import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HTTPError, type NormalizedOptions } from "ky";
import { describe, expect, it, vi } from "vitest";
import { CloseAllDoneButton, formatBatchCloseError } from "./close-all-done-button";

vi.mock("@/lib/hooks/use-issues", () => ({
  useBatchClosePreviewCount: () => ({
    data: 2,
    isLoading: false,
  }),
  useCloseIssuesBatch: () => ({
    mutateAsync: vi.fn(),
    isPending: false,
  }),
}));

describe("CloseAllDoneButton select label", () => {
  it("shows source label in the closed select trigger", async () => {
    const user = userEvent.setup();
    render(<CloseAllDoneButton projectId="proj-1" />);

    await user.click(screen.getByRole("button", { name: /关闭全部/ }));

    const trigger = screen.getByRole("combobox");
    expect(trigger).toHaveTextContent("内部问题");
    expect(trigger).not.toHaveTextContent(/^internal$/);
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
