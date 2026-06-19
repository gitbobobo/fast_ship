import {
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { issueApi } from "@/lib/api/issues";

const collabKey = (issueId: string) => ["issues", issueId, "collab"] as const;
const issueKey = (issueId: string) => ["issues", issueId] as const;

function useInvalidateCollab(issueId: string) {
  const queryClient = useQueryClient();
  return () => {
    queryClient.invalidateQueries({ queryKey: collabKey(issueId) });
    queryClient.invalidateQueries({ queryKey: issueKey(issueId) });
  };
}

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

export function useCreateCollabNote(issueId: string) {
  const invalidate = useInvalidateCollab(issueId);
  return useMutation({
    mutationFn: (body: string) => issueApi.createCollabNote(issueId, { body }),
    onSuccess: invalidate,
  });
}

export function useUpdateCollabNote(issueId: string) {
  const invalidate = useInvalidateCollab(issueId);
  return useMutation({
    mutationFn: ({ noteId, body }: { noteId: string; body: string }) =>
      issueApi.updateCollabNote(issueId, noteId, body),
    onSuccess: invalidate,
  });
}

export function useDeleteCollabNote(issueId: string) {
  const invalidate = useInvalidateCollab(issueId);
  return useMutation({
    mutationFn: (noteId: string) => issueApi.deleteCollabNote(issueId, noteId),
    onSuccess: invalidate,
  });
}

export function useAnswerCollabQuestion(issueId: string) {
  const invalidate = useInvalidateCollab(issueId);
  return useMutation({
    mutationFn: ({ questionId, answer }: { questionId: string; answer: string }) =>
      issueApi.answerCollabQuestion(issueId, questionId, { answer }),
    onSuccess: invalidate,
  });
}

export function useUpsertCollabSummary(issueId: string) {
  const invalidate = useInvalidateCollab(issueId);
  return useMutation({
    mutationFn: (data: { body: string; commit_ids: string[] }) =>
      issueApi.upsertCollabSummary(issueId, data),
    onSuccess: invalidate,
  });
}
