import { describe, expect, it, vi } from "vitest";
import {
  normalizeScreenKey,
  uploadScreenshotBatch,
} from "./screenshot-upload";
import type { UploadScreenshotFields } from "@/lib/api/screenshots";

function makeFile(name: string): File {
  return new File(["x"], name, { type: "image/png" });
}

type UploadOne = (
  projectId: string,
  file: File,
  fields: UploadScreenshotFields,
) => Promise<unknown>;

describe("normalizeScreenKey", () => {
  it("strips the extension and lowercases", () => {
    expect(normalizeScreenKey("Home.PNG")).toBe("home");
    expect(normalizeScreenKey("Settings.png")).toBe("settings");
  });

  it("collapses non-alphanumeric runs into a single dash", () => {
    expect(normalizeScreenKey("settings page.png")).toBe("settings-page");
    expect(normalizeScreenKey("a__b--c.png")).toBe("a__b-c");
    expect(normalizeScreenKey("weird!!!.jpeg")).toBe("weird");
  });

  it("keeps CJK characters", () => {
    expect(normalizeScreenKey("首页 截图.png")).toBe("首页-截图");
  });

  it("handles multi-extension and edge cases", () => {
    expect(normalizeScreenKey("archive.tar.gz")).toBe("archive-tar");
    expect(normalizeScreenKey(".png")).toBe("screenshot");
    expect(normalizeScreenKey("  spaced  .webp")).toBe("spaced");
  });
});

describe("uploadScreenshotBatch", () => {
  it("uploads each file once and aggregates successes", async () => {
    const uploadOne = vi.fn<UploadOne>().mockResolvedValue({});
    const files = [makeFile("a.png"), makeFile("b.png")];

    const result = await uploadScreenshotBatch("p1", files, {}, uploadOne);

    expect(uploadOne).toHaveBeenCalledTimes(2);
    expect(result.succeeded).toEqual(files);
    expect(result.failed).toEqual([]);
  });

  it("keeps going after a failure and reports it", async () => {
    const bad = makeFile("bad.png");
    const uploadOne = vi.fn<UploadOne>((_pid, file) =>
      file === bad ? Promise.reject(new Error("图片过大")) : Promise.resolve({}),
    );
    const files = [makeFile("ok1.png"), bad, makeFile("ok2.png")];

    const result = await uploadScreenshotBatch("p1", files, {}, uploadOne);

    expect(uploadOne).toHaveBeenCalledTimes(3);
    expect(result.succeeded).toHaveLength(2);
    expect(result.failed).toEqual([{ file: bad, message: "图片过大" }]);
  });

  it("wraps non-Error rejections with a default message", async () => {
    const uploadOne = vi.fn<UploadOne>().mockRejectedValue("boom");
    const result = await uploadScreenshotBatch(
      "p1",
      [makeFile("a.png")],
      {},
      uploadOne,
    );
    expect(result.failed[0].message).toBe("上传失败");
  });

  it("multi-file batch uses normalized file name as screen_key and shared group", async () => {
    const calls: UploadScreenshotFields[] = [];
    const uploadOne = vi.fn<UploadOne>((_pid, _file, fields) => {
      calls.push(fields);
      return Promise.resolve({});
    });
    const files = [makeFile("Home Page.png"), makeFile("设置.png")];

    await uploadScreenshotBatch("p1", files, { group: "核心" }, uploadOne);

    expect(calls.map((c) => c.screen_key)).toEqual(["home-page", "设置"]);
    expect(calls.every((c) => c.group === "核心")).toBe(true);
    // 多文件不带 title/note
    expect(calls.every((c) => c.title === undefined && c.note === undefined)).toBe(
      true,
    );
  });

  it("single file uses the provided screen_key, title and note", async () => {
    const calls: UploadScreenshotFields[] = [];
    const uploadOne = vi.fn<UploadOne>((_pid, _file, fields) => {
      calls.push(fields);
      return Promise.resolve({});
    });

    await uploadScreenshotBatch(
      "p1",
      [makeFile("whatever.png")],
      { group: "核心", screenKey: "main-screen", title: "主界面", note: "改版后" },
      uploadOne,
    );

    expect(calls).toEqual([
      {
        screen_key: "main-screen",
        group: "核心",
        title: "主界面",
        note: "改版后",
      },
    ]);
  });

  it("reports progress after each file", async () => {
    const progress: Array<[number, number]> = [];
    const uploadOne = vi.fn<UploadOne>().mockResolvedValue({});
    await uploadScreenshotBatch(
      "p1",
      [makeFile("a.png"), makeFile("b.png")],
      {},
      uploadOne,
      (done, total) => progress.push([done, total]),
    );
    expect(progress).toEqual([
      [1, 2],
      [2, 2],
    ]);
  });
});
