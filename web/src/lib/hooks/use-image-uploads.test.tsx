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

describe("useImageUploads park", () => {
  beforeEach(() => {
    uploadAttachment.mockReset();
    deleteAttachment.mockReset();
    deleteAttachment.mockResolvedValue(null);
  });

  it("keeps an in-flight parked draft when the upload finishes", async () => {
    const pending = deferred<ChatAttachment>();
    uploadAttachment.mockReturnValue(pending.promise);
    const { result } = renderHook(() => useImageUploads("e1"));

    act(() => result.current.add([png()]));
    expect(result.current.images[0]?.status).toBe("uploading");

    act(() => result.current.parkAndShowExisting([att("queued", "queued.png")]));
    expect(result.current.images.map((i) => i.name)).toEqual(["queued.png"]);

    await act(async () => pending.resolve(att("att-draft")));
    expect(deleteAttachment).not.toHaveBeenCalled();

    act(() => result.current.restoreParked());
    await waitFor(() => expect(result.current.images[0]?.status).toBe("done"));
    expect(result.current.images[0]?.attachment?.id).toBe("att-draft");
    expect(deleteAttachment).not.toHaveBeenCalled();
  });

  it("records a parked upload failure instead of leaving it spinning", async () => {
    const pending = deferred<ChatAttachment>();
    uploadAttachment.mockReturnValue(pending.promise);
    const { result } = renderHook(() => useImageUploads("e1"));

    act(() => result.current.add([png()]));
    act(() => result.current.parkAndShowExisting([]));
    await act(async () => pending.reject(new Error("上游拒绝")));

    act(() => result.current.restoreParked());
    await waitFor(() => expect(result.current.images[0]?.status).toBe("error"));
    expect(result.current.images[0]?.error).toBe("上游拒绝");
    expect(deleteAttachment).not.toHaveBeenCalled();
  });

  it("discards images added during edit when restoring unsaved", async () => {
    uploadAttachment
      .mockResolvedValueOnce(att("att-draft"))
      .mockResolvedValueOnce(att("att-edit", "edit.png"));
    const { result } = renderHook(() => useImageUploads("e1"));

    act(() => result.current.add([png()]));
    await waitFor(() => expect(result.current.images[0]?.status).toBe("done"));

    act(() => result.current.parkAndShowExisting([att("queued", "queued.png")]));
    act(() => result.current.add([png("edit.png")]));
    await waitFor(() =>
      expect(result.current.images.some((i) => i.name === "edit.png" && i.status === "done")).toBe(
        true,
      ),
    );

    act(() => result.current.restoreParked({ discardUnsaved: true }));
    await waitFor(() => expect(deleteAttachment).toHaveBeenCalledWith("e1", "att-edit"));
    expect(result.current.images.map((i) => i.attachment?.id)).toEqual(["att-draft"]);
    expect(deleteAttachment).not.toHaveBeenCalledWith("e1", "att-draft");
  });

  it("does not delete images added during edit when saving", async () => {
    uploadAttachment
      .mockResolvedValueOnce(att("att-draft"))
      .mockResolvedValueOnce(att("att-edit", "edit.png"));
    const { result } = renderHook(() => useImageUploads("e1"));

    act(() => result.current.add([png()]));
    await waitFor(() => expect(result.current.images[0]?.status).toBe("done"));
    act(() => result.current.parkAndShowExisting([att("queued", "queued.png")]));
    act(() => result.current.add([png("edit.png")]));
    await waitFor(() =>
      expect(result.current.images.some((i) => i.name === "edit.png" && i.status === "done")).toBe(
        true,
      ),
    );

    act(() => result.current.restoreParked());
    expect(deleteAttachment).not.toHaveBeenCalled();
    expect(result.current.images.map((i) => i.attachment?.id)).toEqual(["att-draft"]);
  });
});
