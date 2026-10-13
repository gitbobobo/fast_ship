import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ChatAttachment } from "@/lib/api/threads";
import { useImageUploads } from "@/lib/hooks/use-image-uploads";

const { uploadAttachment, deleteAttachment } = vi.hoisted(() => ({
  uploadAttachment: vi.fn(),
  deleteAttachment: vi.fn(),
}));

vi.mock("@/lib/api/threads", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/threads")>()),
  threadApi: {
    uploadAttachment,
    deleteAttachment,
    list: vi.fn(),
    detail: vi.fn(),
  },
}));

function png(name = "draft.png") {
  return new File([new Uint8Array([1, 2, 3, 4])], name, { type: "image/png" });
}

function att(id: string, name = "draft.png"): ChatAttachment {
  return { type: "image", id, name, mime_type: "image/png", size_bytes: 4 };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

describe("useImageUploads", () => {
  beforeEach(() => {
    uploadAttachment.mockReset();
    deleteAttachment.mockReset();
    deleteAttachment.mockResolvedValue(null);
  });

  it("seeds existing attachments without uploading", () => {
    const { result } = renderHook(() =>
      useImageUploads("e1", [att("queued", "queued.png")]),
    );
    expect(result.current.images).toEqual([
      expect.objectContaining({
        name: "queued.png",
        status: "done",
        existing: true,
        attachment: att("queued", "queued.png"),
      }),
    ]);
    expect(uploadAttachment).not.toHaveBeenCalled();
  });

  it("deletes a finished upload that the user removed before it landed", async () => {
    const pending = deferred<ChatAttachment>();
    uploadAttachment.mockReturnValue(pending.promise);
    const { result } = renderHook(() => useImageUploads("e1"));

    act(() => result.current.add([png()]));
    const key = result.current.images[0]?.key;
    expect(key).toBeTruthy();
    act(() => result.current.remove(key!));
    await act(async () => pending.resolve(att("att-draft")));
    expect(deleteAttachment).toHaveBeenCalledWith("e1", "att-draft");
  });

  it("does not delete existing attachments on unmount", () => {
    const { unmount } = renderHook(() =>
      useImageUploads("e1", [att("queued", "queued.png")]),
    );
    unmount();
    expect(deleteAttachment).not.toHaveBeenCalled();
  });

  it("deletes unsent uploads on unmount", async () => {
    uploadAttachment.mockResolvedValue(att("att-draft"));
    const { result, unmount } = renderHook(() => useImageUploads("e1"));
    act(() => result.current.add([png()]));
    await waitFor(() => expect(result.current.images[0]?.status).toBe("done"));
    unmount();
    await waitFor(() => expect(deleteAttachment).toHaveBeenCalledWith("e1", "att-draft"));
  });

  it("clear drops local previews without deleting referenced files", async () => {
    uploadAttachment.mockResolvedValue(att("att-draft"));
    const { result } = renderHook(() => useImageUploads("e1"));
    act(() => result.current.add([png()]));
    await waitFor(() => expect(result.current.images[0]?.status).toBe("done"));
    act(() => result.current.clear());
    expect(result.current.images).toEqual([]);
    expect(deleteAttachment).not.toHaveBeenCalled();
  });
});
