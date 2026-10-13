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

  it("does not delete inflight uploads on unmount while the request is pending", async () => {
    uploadAttachment.mockResolvedValue(att("att-sent"));
    const pending = deferred<void>();
    const { result, unmount } = renderHook(() => useImageUploads("e1"));
    act(() => result.current.add([png()]));
    await waitFor(() => expect(result.current.images[0]?.status).toBe("done"));
    act(() => {
      void result.current.submit(() => pending.promise);
    });
    unmount();
    expect(deleteAttachment).not.toHaveBeenCalled();
    await act(async () => pending.resolve());
    expect(deleteAttachment).not.toHaveBeenCalled();
  });

  it("deletes a later draft after a successful submit", async () => {
    uploadAttachment
      .mockResolvedValueOnce(att("att-sent"))
      .mockResolvedValueOnce(att("att-next"));
    const { result } = renderHook(() => useImageUploads("e1"));
    act(() => result.current.add([png("sent.png")]));
    await waitFor(() => expect(result.current.images[0]?.status).toBe("done"));
    await act(async () => {
      await result.current.submit(() => Promise.resolve());
    });
    expect(result.current.images).toEqual([]);
    act(() => result.current.add([png("next.png")]));
    await waitFor(() => expect(result.current.images[0]?.status).toBe("done"));
    const key = result.current.images[0]?.key;
    act(() => result.current.remove(key!));
    expect(deleteAttachment).toHaveBeenCalledWith("e1", "att-next");
    expect(deleteAttachment).not.toHaveBeenCalledWith("e1", "att-sent");
  });

  it("does not delete submitted ids after unmount if the request succeeds", async () => {
    uploadAttachment.mockResolvedValue(att("att-sent"));
    const pending = deferred<void>();
    const { result, unmount } = renderHook(() => useImageUploads("e1"));
    act(() => result.current.add([png()]));
    await waitFor(() => expect(result.current.images[0]?.status).toBe("done"));
    let done!: Promise<void>;
    act(() => {
      done = result.current.submit(() => pending.promise);
    });
    unmount();
    await act(async () => pending.resolve());
    await done;
    expect(deleteAttachment).not.toHaveBeenCalled();
  });

  it("deletes submitted ids after unmount if the request fails", async () => {
    uploadAttachment.mockResolvedValue(att("att-sent"));
    const pending = deferred<void>();
    const { result, unmount } = renderHook(() => useImageUploads("e1"));
    act(() => result.current.add([png()]));
    await waitFor(() => expect(result.current.images[0]?.status).toBe("done"));
    const done = result.current.submit(() => pending.promise).catch((err: unknown) => err);
    unmount();
    await act(async () => pending.reject(new Error("no")));
    await done;
    await waitFor(() => expect(deleteAttachment).toHaveBeenCalledWith("e1", "att-sent"));
  });

  it("keeps images after a failure while still mounted", async () => {
    uploadAttachment.mockResolvedValue(att("att-sent"));
    const { result } = renderHook(() => useImageUploads("e1"));
    act(() => result.current.add([png()]));
    await waitFor(() => expect(result.current.images[0]?.status).toBe("done"));
    await act(async () => {
      await result.current.submit(() => Promise.reject(new Error("no"))).catch(() => undefined);
    });
    expect(result.current.images).toHaveLength(1);
    expect(deleteAttachment).not.toHaveBeenCalled();
    const key = result.current.images[0]?.key;
    act(() => result.current.remove(key!));
    expect(deleteAttachment).toHaveBeenCalledWith("e1", "att-sent");
  });

  it("does not delete existing attachments when an inflight edit fails after unmount", async () => {
    uploadAttachment.mockResolvedValue(att("att-new", "new.png"));
    const pending = deferred<void>();
    const { result, unmount } = renderHook(() =>
      useImageUploads("e1", [att("queued", "queued.png")]),
    );
    act(() => result.current.add([png("new.png")]));
    await waitFor(() => expect(result.current.images.some((i) => i.name === "new.png" && i.status === "done")).toBe(true));
    const done = result.current.submit(() => pending.promise).catch((err: unknown) => err);
    unmount();
    await act(async () => pending.reject(new Error("no")));
    await done;
    await waitFor(() => expect(deleteAttachment).toHaveBeenCalledWith("e1", "att-new"));
    expect(deleteAttachment).not.toHaveBeenCalledWith("e1", "queued");
  });

  it("does not delete an inflight image if remove is called while the request is pending", async () => {
    uploadAttachment.mockResolvedValue(att("att-sent"));
    const pending = deferred<void>();
    const { result } = renderHook(() => useImageUploads("e1"));
    act(() => result.current.add([png()]));
    await waitFor(() => expect(result.current.images[0]?.status).toBe("done"));
    const key = result.current.images[0]?.key;
    act(() => {
      void result.current.submit(() => pending.promise);
    });
    act(() => result.current.remove(key!));
    expect(deleteAttachment).not.toHaveBeenCalled();
    await act(async () => pending.resolve());
    expect(deleteAttachment).not.toHaveBeenCalled();
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
