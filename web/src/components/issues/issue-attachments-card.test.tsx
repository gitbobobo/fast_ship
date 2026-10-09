import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { IssueAttachmentsCard } from "./issue-attachments-card";

const uploadState = vi.hoisted(() => ({
  mutateAsync: vi.fn(),
  isPending: false,
}));

const deleteState = vi.hoisted(() => ({
  mutateAsync: vi.fn(),
  isPending: false,
}));

vi.mock("@/lib/hooks/use-issues", () => ({
  useUploadIssueAttachment: () => uploadState,
  useDeleteIssueAttachment: () => deleteState,
}));

vi.mock("@/lib/api/attachments", () => ({
  attachmentApi: {
    downloadUrl: (attachment: Pick<IssueAttachment, "id">) =>
      `/api/attachments/${attachment.id}/download?token=jwt-token`,
  },
}));

const toastMock = vi.hoisted(() => ({
  success: vi.fn(),
  error: vi.fn(),
}));

vi.mock("sonner", () => ({
  toast: toastMock,
}));

function createDeferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((promiseResolve, promiseReject) => {
    resolve = promiseResolve;
    reject = promiseReject;
  });
  return { promise, resolve, reject };
}

function buildAttachment(
  overrides: Partial<IssueAttachment> = {},
): IssueAttachment {
  return {
    id: "att-1",
    file_name: "复现视频.mp4",
    file_size: 2048,
    mime_type: "video/mp4",
    created_at: "2026-04-12T10:00:00Z",
    uploader: "alice",
    download_url: "/api/attachments/att-1/download",
    ...overrides,
  };
}

function renderCard(attachments: IssueAttachment[] = []) {
  return render(
    <IssueAttachmentsCard issueId="issue-1" attachments={attachments} />,
  );
}

describe("IssueAttachmentsCard", () => {
  beforeEach(() => {
    uploadState.mutateAsync.mockClear().mockResolvedValue({});
    deleteState.mutateAsync.mockClear().mockResolvedValue({});
    toastMock.success.mockClear();
    toastMock.error.mockClear();
  });

  it("空态展示提示与上传按钮", () => {
    renderCard();

    expect(screen.getByText("附件")).toBeInTheDocument();
    expect(screen.getByText("暂无附件")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "上传附件" }),
    ).toBeInTheDocument();
  });

  it("列表行展示文件名、大小、上传者、时间与下载链接", () => {
    renderCard([
      buildAttachment(),
      buildAttachment({
        id: "att-2",
        file_name: "日志.txt",
        file_size: 512,
        mime_type: "text/plain",
        uploader: "API Key: CI",
      }),
    ]);

    expect(screen.getByText("复现视频.mp4")).toBeInTheDocument();
    expect(screen.getByText("2.0 KB")).toBeInTheDocument();
    expect(screen.getByText("alice")).toBeInTheDocument();
    expect(screen.getByText("日志.txt")).toBeInTheDocument();
    expect(screen.getByText("API Key: CI")).toBeInTheDocument();

    const downloadLink = screen.getByRole("link", {
      name: "下载 复现视频.mp4",
    });
    expect(downloadLink).toHaveAttribute(
      "href",
      "/api/attachments/att-1/download?token=jwt-token",
    );
    expect(downloadLink).toHaveAttribute("download");
  });

  it("点击上传按钮并选择文件后调用上传 mutation", async () => {
    const user = userEvent.setup();
    const { container } = renderCard();

    await user.click(screen.getByRole("button", { name: "上传附件" }));

    const input = container.querySelector<HTMLInputElement>(
      'input[type="file"]',
    );
    expect(input).not.toBeNull();

    fireEvent.change(input!, {
      target: {
        files: [new File(["payload"], "崩溃日志.log", { type: "text/plain" })],
      },
    });

    await waitFor(() => {
      expect(uploadState.mutateAsync).toHaveBeenCalledTimes(1);
    });
    const call = uploadState.mutateAsync.mock.calls[0][0] as {
      formData: FormData;
      onProgress: (percent: number) => void;
    };
    expect(call.formData.get("file")).toBeInstanceOf(File);
    expect((call.formData.get("file") as File).name).toBe("崩溃日志.log");
    await waitFor(() => {
      expect(toastMock.success).toHaveBeenCalledWith("崩溃日志.log 上传成功");
    });
  });

  it("上传失败时展示错误 toast", async () => {
    uploadState.mutateAsync.mockRejectedValueOnce(new Error("network"));
    const { container } = renderCard();

    const input = container.querySelector<HTMLInputElement>(
      'input[type="file"]',
    );
    fireEvent.change(input!, { target: { files: [new File(["x"], "a.zip")] } });

    await waitFor(() => {
      expect(toastMock.error).toHaveBeenCalledWith("a.zip 上传失败");
    });
  });

  it("批次进行中再次选择文件不会另起一批", async () => {
    uploadState.mutateAsync.mockImplementationOnce(
      () => new Promise(() => {}),
    );
    const { container } = renderCard();

    const input = container.querySelector<HTMLInputElement>(
      'input[type="file"]',
    )!;
    fireEvent.change(input, {
      target: { files: [new File(["a"], "a.txt")] },
    });

    await waitFor(() => {
      expect(screen.getByText("正在上传第 1/1 个文件")).toBeInTheDocument();
    });
    expect(
      screen.getByRole("button", { name: "上传中..." }),
    ).toBeDisabled();
    expect(input).toBeDisabled();

    fireEvent.change(input, {
      target: { files: [new File(["b"], "b.txt")] },
    });
    await Promise.resolve();

    expect(uploadState.mutateAsync).toHaveBeenCalledTimes(1);
    expect(screen.getByText("a.txt")).toBeInTheDocument();
    expect(screen.queryByText("b.txt")).not.toBeInTheDocument();
  });

  it("单文件失败后批次继续，结束后才汇总为失败状态", async () => {
    const secondUpload = createDeferred<unknown>();
    uploadState.mutateAsync
      .mockRejectedValueOnce(new Error("network"))
      .mockImplementationOnce(() => secondUpload.promise);
    const { container } = renderCard();

    const input = container.querySelector<HTMLInputElement>(
      'input[type="file"]',
    )!;
    fireEvent.change(input, {
      target: {
        files: [new File(["a"], "a.zip"), new File(["b"], "b.txt")],
      },
    });

    await waitFor(() => {
      expect(toastMock.error).toHaveBeenCalledWith("a.zip 上传失败");
      expect(screen.getByText("正在上传第 2/2 个文件")).toBeInTheDocument();
    });
    // 批次仍在进行：上传按钮保持禁用
    expect(
      screen.getByRole("button", { name: "上传中..." }),
    ).toBeDisabled();

    secondUpload.resolve({});
    await waitFor(() => {
      expect(screen.getByText("上传结束，失败 1 个")).toBeInTheDocument();
    });
    expect(toastMock.success).toHaveBeenCalledWith("b.txt 上传成功");
  });

  it("新一批上传开始时作废上一批遗留的进度清理定时器", async () => {
    vi.useFakeTimers();
    try {
      const { container } = renderCard();
      const input = container.querySelector<HTMLInputElement>(
        'input[type="file"]',
      )!;

      fireEvent.change(input, {
        target: { files: [new File(["a"], "a.txt")] },
      });
      // 第一批结束，进度块进入 2 秒自动清理窗口
      await act(async () => {});
      expect(screen.getByText("上传完成")).toBeInTheDocument();

      // 旧定时器触发前开始第二批，并让它保持 pending
      await act(async () => {
        vi.advanceTimersByTime(1900);
      });
      uploadState.mutateAsync.mockImplementationOnce(
        () => new Promise(() => {}),
      );
      fireEvent.change(input, {
        target: { files: [new File(["b"], "b.txt")] },
      });
      await act(async () => {});
      expect(screen.getByText("正在上传第 1/1 个文件")).toBeInTheDocument();

      // 越过旧定时器原定触发点（2s），第二批的进度不应被清掉
      await act(async () => {
        vi.advanceTimersByTime(300);
      });
      expect(screen.getByText("正在上传第 1/1 个文件")).toBeInTheDocument();
      expect(screen.getByText("b.txt")).toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });

  it("删除需二次确认，确认后调用删除 mutation 并提示", async () => {
    const user = userEvent.setup();
    renderCard([buildAttachment()]);

    await user.click(
      screen.getByRole("button", { name: "删除 复现视频.mp4" }),
    );
    expect(screen.getByText("确认删除附件?")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "确认删除" }));

    await waitFor(() => {
      expect(deleteState.mutateAsync).toHaveBeenCalledWith("att-1");
      expect(toastMock.success).toHaveBeenCalledWith("复现视频.mp4 已删除");
    });
  });

  it("删除失败时展示错误 toast", async () => {
    const user = userEvent.setup();
    deleteState.mutateAsync.mockRejectedValueOnce(new Error("network"));
    renderCard([buildAttachment()]);

    await user.click(
      screen.getByRole("button", { name: "删除 复现视频.mp4" }),
    );
    await user.click(screen.getByRole("button", { name: "确认删除" }));

    await waitFor(() => {
      expect(toastMock.error).toHaveBeenCalledWith("删除失败");
    });
  });
});
