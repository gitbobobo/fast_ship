import { useMutation, useQueryClient } from "@tanstack/react-query";
import { artifactApi } from "@/lib/api/artifacts";

interface UploadArtifactInput {
  formData: FormData;
  onProgress?: (percent: number) => void;
}

export function useUploadArtifact(vid: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ formData, onProgress }: UploadArtifactInput) =>
      artifactApi.upload(vid, formData, onProgress),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["versions", vid] });
    },
  });
}

export function useDeleteArtifact(vid: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: artifactApi.delete,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["versions", vid] });
    },
  });
}
