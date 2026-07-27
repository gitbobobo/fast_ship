import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { issuePromptApi } from "@/lib/api/issue-prompt";
import { normalizeIssuePrompts } from "@/lib/issue-prompt";

export function useIssuePrompts() {
  return useQuery({
    queryKey: ["issue-prompts"],
    queryFn: async () => {
      const res = await issuePromptApi.get();
      return res.data;
    },
  });
}

export function useIssuePromptList() {
  const { data } = useIssuePrompts();
  return normalizeIssuePrompts(data?.prompts);
}

export function useUpdateIssuePrompts() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (prompts: IssuePrompt[]) => {
      const res = await issuePromptApi.update(prompts);
      return res.data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["issue-prompts"] });
    },
  });
}
