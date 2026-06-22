import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { issueApi } from "@/lib/api/issues";

export type CollabDeleteSection = "all" | "suggestions" | "plan" | "review" | "summary";

export const collabKey = (issueId: string) => ["issues", issueId, "collab"] as const;

export function useIssueCollab(issueId: string) {
  return useQuery({
    queryKey: collabKey(issueId),
    queryFn: async () => {
      const res = await issueApi.getCollab(issueId);
      return res.data;
    },
    enabled: !!issueId,
  });
}

export function useDeleteCollabSection(issueId: string, section: CollabDeleteSection) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => issueApi.deleteCollabSection(issueId, section),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: collabKey(issueId) });
    },
  });
}
