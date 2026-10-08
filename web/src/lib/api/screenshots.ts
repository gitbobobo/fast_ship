import { useAuthStore } from "@/lib/store/auth-store";
import { api } from "./client";

export interface UpdateScreenshotScreenPayload {
  /** 出现才更新；空串表示未分组 */
  group?: string;
  /** 出现才更新；空串表示回退展示 screen_key */
  title?: string;
}

export interface UploadScreenshotFields {
  screen_key: string;
  group?: string;
  title?: string;
  note?: string;
}

export const screenshotApi = {
  list: (projectId: string) =>
    api
      .get(`projects/${projectId}/screenshots`)
      .json<ApiResponse<ScreenshotScreenListData>>(),

  get: (screenId: string) =>
    api
      .get(`screenshot-screens/${screenId}`)
      .json<ApiResponse<ScreenshotScreenDetail>>(),

  update: (screenId: string, payload: UpdateScreenshotScreenPayload) =>
    api
      .patch(`screenshot-screens/${screenId}`, { json: payload })
      .json<ApiResponse<ScreenshotScreenDetail>>(),

  deleteScreen: (screenId: string) =>
    api
      .delete(`screenshot-screens/${screenId}`)
      .json<ApiResponse<null>>(),

  deleteVersion: (versionId: string) =>
    api
      .delete(`screenshot-versions/${versionId}`)
      .json<ApiResponse<null>>(),

  // 一图一请求；批量上传由调用方循环
  upload: (
    projectId: string,
    file: File,
    fields: UploadScreenshotFields,
  ) => {
    const formData = new FormData();
    formData.append("file", file);
    formData.append("screen_key", fields.screen_key);
    // 可选字段「出现才传」：空串是合法值（如 group="" 表示未分组），不能真值判断
    if (fields.group !== undefined) formData.append("group", fields.group);
    if (fields.title !== undefined) formData.append("title", fields.title);
    if (fields.note !== undefined) formData.append("note", fields.note);
    return api
      .post(`projects/${projectId}/screenshots`, {
        body: formData,
        timeout: 60_000,
      })
      .json<ApiResponse<ScreenshotUploadResult>>();
  },

  // 图片字节接口需要带 jwt；参照 artifactApi.downloadUrl 的拼法
  contentUrl: (version: Pick<ScreenshotVersion, "id" | "content_url">) => {
    const token = useAuthStore.getState().token;
    const base =
      version.content_url ||
      `/api/screenshot-versions/${version.id}/content`;
    const url = new URL(base, window.location.origin);

    if (token) {
      url.searchParams.set("token", token);
    }

    return `${url.pathname}${url.search}`;
  },
};
