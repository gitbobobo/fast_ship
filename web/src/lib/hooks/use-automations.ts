import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  automationApi,
  type AutomationInput,
} from "@/lib/api/automations";
import { useAuthStore } from "@/lib/store/auth-store";

// 自动化查询（INT-90）。缓存按用户隔离（与 use-threads 同口径）。
const ROOT = "automations";

const userId = () => useAuthStore.getState().user?.id ?? null;

const keys = {
  list: () => [ROOT, userId(), "list"],
  runs: (id: string) => [ROOT, userId(), "runs", id],
};

export function useAutomations() {
  return useQuery({
    queryKey: keys.list(),
    queryFn: async () => (await automationApi.list()).data.items,
    enabled: Boolean(userId()),
  });
}

export function useAutomationRuns(id: string | null) {
  return useQuery({
    queryKey: keys.runs(id ?? ""),
    queryFn: async () => (await automationApi.listRuns(id as string)).data.items,
    enabled: Boolean(id && userId()),
  });
}

function useInvalidateAutomations() {
  const queryClient = useQueryClient();
  return () => {
    queryClient.invalidateQueries({ queryKey: [ROOT, userId()] });
  };
}

export function useCreateAutomation() {
  const invalidate = useInvalidateAutomations();
  return useMutation({
    mutationFn: (data: AutomationInput) => automationApi.create(data),
    onSuccess: invalidate,
  });
}

export function useUpdateAutomation() {
  const invalidate = useInvalidateAutomations();
  return useMutation({
    mutationFn: ({ id, data }: { id: string; data: AutomationInput }) =>
      automationApi.update(id, data),
    onSuccess: invalidate,
  });
}

// useToggleAutomation 只改启停：PUT 全量语义要求带完整配置，调用方从现有行构造。
export function useToggleAutomation() {
  const invalidate = useInvalidateAutomations();
  return useMutation({
    mutationFn: ({ id, data }: { id: string; data: AutomationInput }) =>
      automationApi.update(id, data),
    onSuccess: invalidate,
  });
}

export function useDeleteAutomation() {
  const invalidate = useInvalidateAutomations();
  return useMutation({
    mutationFn: (id: string) => automationApi.remove(id),
    onSuccess: invalidate,
  });
}
