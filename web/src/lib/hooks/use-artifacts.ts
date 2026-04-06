import { useMutation, useQueryClient } from "@tanstack/react-query";
import { artifactApi } from "@/lib/api/artifacts";

export function useUploadArtifact(vid: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (formData: FormData) => artifactApi.upload(vid, formData),
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
