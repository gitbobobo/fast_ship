import { useAuthStore } from "@/lib/store/auth-store";
import { api } from "./client";

export interface ScreenshotAnnotationFilters {
  status?: ScreenshotAnnotationStatus;
  issue_id?: string;
  screen_id?: string;
}

export const screenshotAnnotationApi = {
  list: (projectId: string, filters: ScreenshotAnnotationFilters = {}) =>
    api
      .get(`projects/${projectId}/screenshot-annotations`, {
        searchParams: {
          ...(filters.status ? { status: filters.status } : {}),
          ...(filters.issue_id ? { issue_id: filters.issue_id } : {}),
          ...(filters.screen_id ? { screen_id: filters.screen_id } : {}),
        },
      })
      .json<ApiResponse<{ items: ScreenshotAnnotation[] }>>(),

  create: (versionId: string, payload: CreateScreenshotAnnotationPayload) =>
    api
      .post(`screenshot-versions/${versionId}/annotations`, { json: payload })
      .json<ApiResponse<ScreenshotAnnotation>>(),

  update: (annotationId: string, payload: UpdateScreenshotAnnotationPayload) =>
    api
      .put(`screenshot-annotations/${annotationId}`, { json: payload })
      .json<ApiResponse<ScreenshotAnnotation>>(),

  delete: (annotationId: string) =>
    api
      .delete(`screenshot-annotations/${annotationId}`)
      .json<ApiResponse<null>>(),

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
