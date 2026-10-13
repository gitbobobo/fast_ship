import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { githubTokenApi } from "@/lib/api/github-tokens";

export function useGithubTokens() {
  return useQuery({
    queryKey: ["github-tokens"],
    queryFn: async () => {
      const res = await githubTokenApi.list();
      return res.data;
    },
  });
}

export function useCreateGithubToken() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: githubTokenApi.create,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["github-tokens"] });
    },
  });
}

export function useUpdateGithubToken() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, ...data }: { id: string; title?: string; token?: string }) =>
      githubTokenApi.update(id, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["github-tokens"] });
    },
  });
}

export function useDeleteGithubToken() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: githubTokenApi.delete,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["github-tokens"] });
    },
  });
}

export function useDetectGithubToken() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: githubTokenApi.detect,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["github-tokens"] });
    },
  });
}
