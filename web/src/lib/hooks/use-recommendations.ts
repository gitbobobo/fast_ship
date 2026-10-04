import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { recommendationApi } from "@/lib/api/recommendations";

export function useRecommendations(projectId: string | undefined) {
  return useQuery({
    queryKey: ["recommendations", projectId ?? "all"],
    queryFn: async () => {
      const res = await recommendationApi.list(projectId);
      return res.data;
    },
    refetchInterval: 60000,
    refetchOnWindowFocus: true,
  });
}

export function useRemoveRecommendation() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (issueId: string) => {
      await recommendationApi.remove(issueId);
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["recommendations"] });
    },
  });
}
