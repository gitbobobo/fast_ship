import { useAuthStore } from "@/lib/store/auth-store";
import { api } from "./client";

export interface UpdateScreenshotScreenPayload {
  /** 出现才更新；空串表示清除为未标注 */
  title?: string;
  platform?: string;
  locale?: string;
  theme?: string;
}

export interface UploadScreenshotFields {
  screen_key: string;
  title?: string;
  /** 三个维度沿用表单存在性语义：出现才传（空串清除为未标注） */
  platform?: string;
  locale?: string;
  theme?: string;
}

/** 截图列表的维度过滤：逗号多值，含「未标注」桶 __none__ */
export interface ScreenshotScreenQueryFilters {
  platform?: string[];
  locale?: string[];
  theme?: string[];
}

export const screenshotApi = {
  list: (projectId: string, filters: ScreenshotScreenQueryFilters = {}) =>
    api
      .get(`projects/${projectId}/screenshots`, {
        searchParams: {
          ...(filters.platform?.length ? { platform: filters.platform.join(",") } : {}),
          ...(filters.locale?.length ? { locale: filters.locale.join(",") } : {}),
          ...(filters.theme?.length ? { theme: filters.theme.join(",") } : {}),
        },
      })
      .json<ApiResponse<ScreenshotScreenListData>>(),

  update: (screenId: string, payload: UpdateScreenshotScreenPayload) =>
    api
      .patch(`screenshot-screens/${screenId}`, { json: payload })
      .json<ApiResponse<ScreenshotScreen>>(),

  deleteScreen: (screenId: string) =>
    api
      .delete(`screenshot-screens/${screenId}`)
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
    // 可选字段「出现才传」：空串是合法值（如 platform="" 清除为未标注），不能真值判断
    if (fields.title !== undefined) formData.append("title", fields.title);
    if (fields.platform !== undefined) formData.append("platform", fields.platform);
    if (fields.locale !== undefined) formData.append("locale", fields.locale);
    if (fields.theme !== undefined) formData.append("theme", fields.theme);
    return api
      .post(`projects/${projectId}/screenshots`, {
        body: formData,
        timeout: 60_000,
      })
      .json<ApiResponse<ScreenshotUploadResult>>();
  },

  // 图片字节接口需要带 jwt；参照 artifactApi.downloadUrl 的拼法
  contentUrl: (screen: Pick<ScreenshotScreen, "id" | "content_url">) => {
    const token = useAuthStore.getState().token;
    const base =
      screen.content_url ||
      `/api/screenshot-screens/${screen.id}/content`;
    const url = new URL(base, window.location.origin);

    if (token) {
      url.searchParams.set("token", token);
    }

    return `${url.pathname}${url.search}`;
  },
};
