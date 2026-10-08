import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  screenshotApi,
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
}

export function useScreenshotScreens(projectId: string) {
  return useQuery({
    queryKey: ["screenshots", "list", projectId],
    queryFn: async () => {
      const res = await screenshotApi.list(projectId);
      return res.data;
    },
    enabled: !!projectId,
  });
}

export function useScreenshotScreen(screenId: string) {
  return useQuery({
    queryKey: ["screenshots", "detail", screenId],
    queryFn: async () => {
      const res = await screenshotApi.get(screenId);
      return res.data;
    },
    enabled: !!screenId,
    retry: false,
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
    onSuccess: (_data, variables) => {
      invalidateScreenshotList(queryClient, projectId);
      void queryClient.invalidateQueries({
        queryKey: ["screenshots", "detail", variables.screenId],
      });
    },
  });
}

export function useDeleteScreenshotScreen(projectId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (screenId: string) => screenshotApi.deleteScreen(screenId),
    onSuccess: (_data, screenId) => {
      invalidateScreenshotList(queryClient, projectId);
      void queryClient.removeQueries({
        queryKey: ["screenshots", "detail", screenId],
      });
    },
  });
}

export function useDeleteScreenshotVersion(projectId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      versionId,
    }: {
      versionId: string;
      screenId: string;
    }) => screenshotApi.deleteVersion(versionId),
    onSuccess: (_data, variables) => {
      invalidateScreenshotList(queryClient, projectId);
      void queryClient.invalidateQueries({
        queryKey: ["screenshots", "detail", variables.screenId],
      });
    },
  });
}
