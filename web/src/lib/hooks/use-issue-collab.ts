import { useQuery } from "@tanstack/react-query";
import { issueApi } from "@/lib/api/issues";

const collabKey = (issueId: string) => ["issues", issueId, "collab"] as const;

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
