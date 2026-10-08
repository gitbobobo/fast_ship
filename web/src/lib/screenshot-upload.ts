import {
  screenshotApi,
  type UploadScreenshotFields,
} from "@/lib/api/screenshots";

// 文件名 → 默认 screen_key：去扩展名、转小写、非字母数字字符折叠为 -，
// 截到服务端上限 100 字符（截完再清一次尾部连字符）
export function normalizeScreenKey(fileName: string): string {
  const stem = fileName.replace(/\.[^.]+$/, "");
  const key = [...stem
    .trim()
    .toLowerCase()
    .replace(/[^\p{L}\p{N}_-]+/gu, "-")
    .replace(/-{2,}/g, "-")
    .replace(/^[-_]+|[-_]+$/g, "")]
    .slice(0, 100)
    .join("")
    .replace(/[-_]+$/g, "");
  return key || "screenshot";
}

export interface ScreenshotUploadMeta {
  /** 本批共用的分组；不传保持界面原分组（置为未分组走编辑） */
  group?: string;
  /** 以下三项仅单文件上传时生效 */
  screenKey?: string;
  title?: string;
  note?: string;
}

export interface ScreenshotUploadFailure {
  file: File;
  message: string;
}

export interface ScreenshotUploadBatchResult {
  succeeded: File[];
  failed: ScreenshotUploadFailure[];
}

type UploadOneFn = (
  projectId: string,
  file: File,
  fields: UploadScreenshotFields,
) => Promise<unknown>;

// 一图一请求逐张上传；单文件失败不中断其余，全部跑完后聚合返回
export async function uploadScreenshotBatch(
  projectId: string,
  files: File[],
  meta: ScreenshotUploadMeta = {},
  uploadOne: UploadOneFn = screenshotApi.upload,
  onProgress?: (completed: number, total: number) => void,
): Promise<ScreenshotUploadBatchResult> {
  const succeeded: File[] = [];
  const failed: ScreenshotUploadFailure[] = [];
  const single = files.length === 1;

  for (const file of files) {
    const fields: UploadScreenshotFields = {
      screen_key:
        single && meta.screenKey
          ? meta.screenKey
          : normalizeScreenKey(file.name),
      group: meta.group,
      ...(single
        ? { title: meta.title, note: meta.note }
        : {}),
    };
    try {
      await uploadOne(projectId, file, fields);
      succeeded.push(file);
    } catch (error) {
      failed.push({
        file,
        message: error instanceof Error ? error.message : "上传失败",
      });
    }
    onProgress?.(succeeded.length + failed.length, files.length);
  }

  return { succeeded, failed };
}
