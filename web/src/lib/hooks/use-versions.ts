import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { versionApi } from "@/lib/api/versions";

export function useVersions(projectId: string, status?: string) {
  return useQuery({
    queryKey: ["projects", projectId, "versions", status ?? "all"],
    queryFn: async () => {
      const res = await versionApi.list(projectId, status);
      return res.data;
    },
    enabled: !!projectId,
  });
}

export function useVersion(vid: string) {
  return useQuery({
    queryKey: ["versions", vid],
    queryFn: async () => {
      const res = await versionApi.get(vid);
      return res.data;
    },
    enabled: !!vid,
  });
}

export function useShipCheck(vid: string, enabled = true) {
  return useQuery({
    queryKey: ["versions", vid, "ship-check"],
    queryFn: async () => {
      const res = await versionApi.shipCheck(vid);
      return res.data;
    },
    enabled: !!vid && enabled,
    retry: false,
  });
}

export function useCreateVersion(projectId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: Parameters<typeof versionApi.create>[1]) =>
      versionApi.create(projectId, data),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["projects", projectId, "versions"],
      });
    },
  });
}

export function useUpdateVersion(vid: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: Parameters<typeof versionApi.update>[1]) =>
      versionApi.update(vid, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["versions", vid] });
    },
  });
}

export function useDeleteVersion(projectId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: versionApi.delete,
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["projects", projectId, "versions"],
      });
    },
  });
}

export function useShipVersion(vid: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => versionApi.ship(vid),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["versions", vid] });
      queryClient.invalidateQueries({ queryKey: ["versions", vid, "ship-check"] });
    },
  });
}
