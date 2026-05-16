import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { aiApi } from "@/lib/api/ai";

export function useAISettings() {
  return useQuery({
    queryKey: ["ai", "settings"],
    queryFn: async () => {
      const res = await aiApi.getSettings();
      return res.data;
    },
  });
}

export function useUpdateAISettings() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: Parameters<typeof aiApi.updateSettings>[0]) =>
      aiApi.updateSettings(data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["ai", "settings"] });
    },
  });
}

export function useIssueChecklistSuggestions(issueId: string) {
  return useMutation({
    mutationFn: async () => {
      const res = await aiApi.suggestIssueChecklist(issueId);
      return res.data;
    },
  });
}

export function useGenerateTitle() {
  return useMutation({
    mutationFn: async (body: string) => {
      const res = await aiApi.generateTitle(body);
      return res.data;
    },
  });
}
