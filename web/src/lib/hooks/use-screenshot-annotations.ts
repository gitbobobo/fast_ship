import {
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import {
  screenshotAnnotationApi,
  type ScreenshotAnnotationFilters,
} from "@/lib/api/screenshot-annotations";

/** 项目下全部标注；画布面板与 lightbox 叠加层共用这一份缓存 */
export function useScreenshotAnnotations(
  projectId: string,
  filters: ScreenshotAnnotationFilters = {},
  enabled = true,
) {
  const key = JSON.stringify(filters);
  return useQuery({
    queryKey: ["screenshot-annotations", "list", projectId, key],
    queryFn: async () => {
      const res = await screenshotAnnotationApi.list(projectId, filters);
      return res.data.items;
    },
    enabled: !!projectId && enabled,
  });
}

// 标注写操作后：标注列表、截图列表（覆盖联动 outdated）、Issue 详情（内嵌标注）都要刷新
function useInvalidateAnnotationQueries(projectId: string) {
  const queryClient = useQueryClient();
  return () => {
    void queryClient.invalidateQueries({
      queryKey: ["screenshot-annotations", "list", projectId],
    });
    void queryClient.invalidateQueries({
      queryKey: ["screenshots", "list", projectId],
    });
    void queryClient.invalidateQueries({ queryKey: ["issues"] });
  };
}

export function useCreateScreenshotAnnotation(projectId: string) {
  const invalidate = useInvalidateAnnotationQueries(projectId);
  return useMutation({
    mutationFn: ({
      screenId,
      payload,
    }: {
      screenId: string;
      payload: CreateScreenshotAnnotationPayload;
    }) => screenshotAnnotationApi.create(screenId, payload),
    onSuccess: invalidate,
  });
}

export function useUpdateScreenshotAnnotation(projectId: string) {
  const invalidate = useInvalidateAnnotationQueries(projectId);
  return useMutation({
    mutationFn: ({
      annotationId,
      payload,
    }: {
      annotationId: string;
      payload: UpdateScreenshotAnnotationPayload;
    }) => screenshotAnnotationApi.update(annotationId, payload),
    onSuccess: invalidate,
  });
}

export function useDeleteScreenshotAnnotation(projectId: string) {
  const invalidate = useInvalidateAnnotationQueries(projectId);
  return useMutation({
    mutationFn: (annotationId: string) =>
      screenshotAnnotationApi.delete(annotationId),
    onSuccess: invalidate,
  });
}

export function useBatchScreenshotAnnotations(projectId: string) {
  const invalidate = useInvalidateAnnotationQueries(projectId);
  return useMutation({
    mutationFn: (payload: ScreenshotAnnotationBatchPayload) =>
      screenshotAnnotationApi.batch(projectId, payload),
    onSuccess: invalidate,
  });
}
