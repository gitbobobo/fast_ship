import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  screenshotApi,
  type ScreenshotScreenQueryFilters,
  type UpdateScreenshotScreenPayload,
} from "@/lib/api/screenshots";
import {
  uploadScreenshotBatch,
  type ScreenshotUploadMeta,
} from "@/lib/screenshot-upload";

function invalidateScreenshotList(
  queryClient: ReturnType<typeof useQueryClient>,
  projectId: string,
) {
  void queryClient.invalidateQueries({
    queryKey: ["screenshots", "list", projectId],
  });
  // 上传覆盖会改变标注的 outdated，删除截图会级联删标注
  void queryClient.invalidateQueries({
    queryKey: ["screenshot-annotations", "list", projectId],
  });
}

export function useScreenshotScreens(
  projectId: string,
  filters: ScreenshotScreenQueryFilters = {},
) {
  const key = JSON.stringify(filters);
  return useQuery({
    queryKey: ["screenshots", "list", projectId, key],
    queryFn: async () => {
      const res = await screenshotApi.list(projectId, filters);
      return res.data;
    },
    enabled: !!projectId,
  });
}

export function useUploadScreenshots(projectId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      files,
      meta,
      onProgress,
    }: {
      files: File[];
      meta?: ScreenshotUploadMeta;
      onProgress?: (completed: number, total: number) => void;
    }) => uploadScreenshotBatch(projectId, files, meta, undefined, onProgress),
    onSettled: () => {
      invalidateScreenshotList(queryClient, projectId);
    },
  });
}

export function useUpdateScreenshotScreen(projectId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      screenId,
      payload,
    }: {
      screenId: string;
      payload: UpdateScreenshotScreenPayload;
    }) => screenshotApi.update(screenId, payload),
    onSuccess: (_data) => {
      invalidateScreenshotList(queryClient, projectId);
    },
  });
}

export function useDeleteScreenshotScreen(projectId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (screenId: string) => screenshotApi.deleteScreen(screenId),
    onSuccess: (_data) => {
      invalidateScreenshotList(queryClient, projectId);
    },
  });
}
