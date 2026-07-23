import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  documentApi,
  type CreateDocumentPayload,
  type UpdateDocumentPayload,
} from "@/lib/api/documents";

export function useDocuments(projectId: string) {
  return useQuery({
    queryKey: ["documents", "list", projectId],
    queryFn: async () => {
      const res = await documentApi.list(projectId);
      return res.data;
    },
    enabled: !!projectId,
  });
}

export function useDocument(docId: string) {
  return useQuery({
    queryKey: ["documents", "detail", docId],
    queryFn: async () => {
      const res = await documentApi.get(docId);
      return res.data;
    },
    enabled: !!docId,
    retry: false,
  });
}

export function useCreateDocument(projectId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (payload: CreateDocumentPayload) =>
      documentApi.create(projectId, payload),
    onSuccess: () => {
      void queryClient.invalidateQueries({
        queryKey: ["documents", "list", projectId],
      });
    },
  });
}

export function useUpdateDocument(projectId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      docId,
      payload,
    }: {
      docId: string;
      payload: UpdateDocumentPayload;
    }) => documentApi.update(docId, payload),
    onSuccess: (_data, variables) => {
      void queryClient.invalidateQueries({
        queryKey: ["documents", "list", projectId],
      });
      void queryClient.invalidateQueries({
        queryKey: ["documents", "detail", variables.docId],
      });
    },
  });
}

export function useDeleteDocument(projectId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (docId: string) => documentApi.delete(docId),
    onSuccess: () => {
      void queryClient.invalidateQueries({
        queryKey: ["documents", "list", projectId],
      });
      void queryClient.removeQueries({
        queryKey: ["documents", "detail"],
      });
    },
  });
}
