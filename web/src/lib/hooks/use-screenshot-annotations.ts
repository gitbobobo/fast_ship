import {
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { screenshotAnnotationApi } from "@/lib/api/screenshot-annotations";

/** 项目下全部标注（含旧版本上的）；画布面板与 lightbox 叠加层共用这一份缓存 */
export function useScreenshotAnnotations(projectId: string, enabled = true) {
  return useQuery({
    queryKey: ["screenshot-annotations", "list", projectId],
    queryFn: async () => {
      const res = await screenshotAnnotationApi.list(projectId);
      return res.data.items;
    },
    enabled: !!projectId && enabled,
  });
}

// 标注写操作后：标注列表、界面列表（版本/标注联动展示）、Issue 详情（内嵌标注）都要刷新
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
      versionId,
      payload,
    }: {
      versionId: string;
      payload: CreateScreenshotAnnotationPayload;
    }) => screenshotAnnotationApi.create(versionId, payload),
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
