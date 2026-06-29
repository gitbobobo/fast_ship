import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { downloadAllArtifacts } from "@/lib/utils/download-artifacts";

describe("downloadAllArtifacts", () => {
  let clickMock: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    clickMock = vi
      .spyOn(HTMLAnchorElement.prototype, "click")
      .mockImplementation(() => {});
  });

  afterEach(() => {
    clickMock.mockRestore();
    document.body.innerHTML = "";
    vi.restoreAllMocks();
  });

  it("does nothing for empty urls", async () => {
    await downloadAllArtifacts([]);

    expect(clickMock).not.toHaveBeenCalled();
  });

  it("downloads a single url and cleans up the anchor", async () => {
    await downloadAllArtifacts(["/api/artifacts/a1/download?token=t"]);

    expect(clickMock).toHaveBeenCalledTimes(1);
    expect(document.body.querySelectorAll("a").length).toBe(0);
  });

  it("sets href and empty download attribute on the temporary anchor", async () => {
    const appendSpy = vi.spyOn(document.body, "appendChild");

    await downloadAllArtifacts(["/api/artifacts/a1/download?token=t"]);

    const anchor = appendSpy.mock.calls[0]?.[0] as HTMLAnchorElement;
    expect(anchor.href).toContain("/api/artifacts/a1/download?token=t");
    expect(anchor.hasAttribute("download")).toBe(true);
    expect(anchor.getAttribute("download")).toBe("");
  });

  it("downloads multiple urls with configured delay between clicks", async () => {
    const setTimeoutSpy = vi
      .spyOn(globalThis, "setTimeout")
      .mockImplementation(((callback: TimerHandler, delay?: number) => {
        expect(delay).toBe(400);
        if (typeof callback === "function") {
          callback();
        }
        return 0 as unknown as ReturnType<typeof setTimeout>;
      }) as typeof setTimeout);

    await downloadAllArtifacts(["/u1", "/u2", "/u3"], { intervalMs: 400 });

    expect(clickMock).toHaveBeenCalledTimes(3);
    expect(setTimeoutSpy).toHaveBeenCalledTimes(2);
    expect(document.body.querySelectorAll("a").length).toBe(0);
  });

  it("does not click when signal is already aborted", async () => {
    const controller = new AbortController();
    controller.abort();

    await downloadAllArtifacts(["/u1", "/u2"], { signal: controller.signal });

    expect(clickMock).not.toHaveBeenCalled();
  });

  it("does not schedule a delay for a single url", async () => {
    const setTimeoutSpy = vi.spyOn(globalThis, "setTimeout");

    await downloadAllArtifacts(["/u1"]);

    expect(setTimeoutSpy).not.toHaveBeenCalled();
  });

  it("cleans up the anchor even when click throws", async () => {
    clickMock.mockImplementationOnce(() => {
      throw new Error("click blocked");
    });

    await expect(downloadAllArtifacts(["/u1"])).rejects.toThrow("click blocked");
    expect(document.body.querySelectorAll("a").length).toBe(0);
  });

  it("stops after abort before subsequent downloads", async () => {
    const controller = new AbortController();

    vi.spyOn(globalThis, "setTimeout").mockImplementation(((callback: TimerHandler) => {
      controller.abort();
      if (typeof callback === "function") {
        callback();
      }
      return 0 as unknown as ReturnType<typeof setTimeout>;
    }) as typeof setTimeout);

    await downloadAllArtifacts(["/u1", "/u2", "/u3"], {
      signal: controller.signal,
    });

    expect(clickMock).toHaveBeenCalledTimes(1);
  });
});
