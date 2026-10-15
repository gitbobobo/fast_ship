import { useAuthStore } from "@/lib/store/auth-store";
import { api } from "./client";

export interface ScreenshotAnnotationFilters {
  status?: ScreenshotAnnotationStatus;
  issue_id?: string;
  screen_id?: string;
  outdated?: boolean;
  /** 逗号多值，含「未标注」桶 __none__ */
  platform?: string[];
  locale?: string[];
  theme?: string[];
}

const joinValues = (values?: string[]) =>
  values && values.length > 0 ? values.join(",") : undefined;

export const screenshotAnnotationApi = {
  list: (projectId: string, filters: ScreenshotAnnotationFilters = {}) =>
    api
      .get(`projects/${projectId}/screenshot-annotations`, {
        searchParams: {
          ...(filters.status ? { status: filters.status } : {}),
          ...(filters.issue_id ? { issue_id: filters.issue_id } : {}),
          ...(filters.screen_id ? { screen_id: filters.screen_id } : {}),
          ...(filters.outdated !== undefined
            ? { outdated: String(filters.outdated) }
            : {}),
          ...(joinValues(filters.platform)
            ? { platform: joinValues(filters.platform) }
            : {}),
          ...(joinValues(filters.locale)
            ? { locale: joinValues(filters.locale) }
            : {}),
          ...(joinValues(filters.theme)
            ? { theme: joinValues(filters.theme) }
            : {}),
        },
      })
      .json<ApiResponse<{ items: ScreenshotAnnotation[] }>>(),

  create: (screenId: string, payload: CreateScreenshotAnnotationPayload) =>
    api
      .post(`screenshot-screens/${screenId}/annotations`, { json: payload })
      .json<ApiResponse<ScreenshotAnnotation>>(),

  update: (annotationId: string, payload: UpdateScreenshotAnnotationPayload) =>
    api
      .put(`screenshot-annotations/${annotationId}`, { json: payload })
      .json<ApiResponse<ScreenshotAnnotation>>(),

  delete: (annotationId: string) =>
    api
      .delete(`screenshot-annotations/${annotationId}`)
      .json<ApiResponse<null>>(),

  batch: (projectId: string, payload: ScreenshotAnnotationBatchPayload) =>
    api
      .post(`projects/${projectId}/screenshot-annotations/batch`, {
        json: payload,
      })
      .json<ApiResponse<ScreenshotAnnotationBatchResult>>(),

  // 裁剪图是 <img> 直链，需带 ?token=；拼法同 screenshotApi.contentUrl
  cropUrl: (annotation: Pick<ScreenshotAnnotation, "id" | "crop_url">) => {
    const token = useAuthStore.getState().token;
    const base =
      annotation.crop_url ||
      `/api/screenshot-annotations/${annotation.id}/crop`;
    const url = new URL(base, window.location.origin);

    if (token) {
      url.searchParams.set("token", token);
    }

    return `${url.pathname}${url.search}`;
  },
};
