import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  automationApi,
  type AutomationInput,
} from "@/lib/api/automations";
import { automationRecordToInput } from "@/lib/automations/form-model";
import { useAuthStore } from "@/lib/store/auth-store";

// 自动化查询（INT-90）。缓存按用户隔离（与 use-threads 同口径）。
const ROOT = "automations";

const userId = () => useAuthStore.getState().user?.id ?? null;

const keys = {
  list: () => [ROOT, userId(), "list"],
  detail: (id: string) => [ROOT, userId(), "detail", id],
  runs: (id: string) => [ROOT, userId(), "runs", id],
};

export function useAutomations() {
  return useQuery({
    queryKey: keys.list(),
    queryFn: async () => (await automationApi.list()).data.items,
    enabled: Boolean(userId()),
  });
}

// 按 id 的详情查询：编辑页加载已有自动化（含触发器）用。
// gcTime: 0 —— 离开编辑页即丢弃详情缓存。编辑页只用首次取到的详情初始化
// 表单（保存成功后不重置输入），若复用旧缓存，列表页的启停等全量 PUT 就会被
// 表单里的过期字段（如 enabled）覆盖写回。丢弃缓存后再次进入必然先 fetch，
// isLoading 为 true，AutomationEditor 只会挂在新数据上。
export function useAutomation(id: string | null) {
  return useQuery({
    queryKey: keys.detail(id ?? ""),
    queryFn: async () => (await automationApi.get(id as string)).data,
    enabled: Boolean(id && userId()),
    gcTime: 0,
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

// useUpdateAutomation：编辑页已把表单整理成完整请求体，原样 PUT——表单就是唯一
// 数据来源，调用方对「要写回什么」负全责。
export function useUpdateAutomation() {
  const invalidate = useInvalidateAutomations();
  return useMutation({
    mutationFn: ({ id, data }: { id: string; data: AutomationInput }) =>
      automationApi.update(id, data),
    onSuccess: invalidate,
  });
}

// useToggleAutomation：只改启停，因此只接收 id。服务端 PUT 是触发器全量替换语义，
// 不能拿列表行快照组请求体——自列表加载后别处（编辑页）可能已新增/删除触发器，
// 用旧快照会把新增的静默删掉、把已删的 id 传上去换 400。这里先 GET 最新详情，
// 再用最新 triggers 组全量 PUT 并翻转 enabled。
export function useToggleAutomation() {
  const invalidate = useInvalidateAutomations();
  return useMutation({
    mutationFn: async (id: string) => {
      const fresh = (await automationApi.get(id)).data;
      return automationApi.update(id, {
        ...automationRecordToInput(fresh),
        enabled: !fresh.enabled,
      });
    },
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
