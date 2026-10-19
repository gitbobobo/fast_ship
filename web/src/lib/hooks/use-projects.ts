import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { projectApi } from "@/lib/api/projects";

// 项目列表的统一顺序：按 Issue 总数（含已关闭）降序，计数缺失按 0；
// 同计数（含并列 0）保持服务端顺序（created_at DESC）。
// 所有出现 Fast Ship 项目列表的界面都从这里取顺序，组件不再各排一套。
const compareByIssueCount = (a: Project, b: Project) =>
  (b.issue_count ?? 0) - (a.issue_count ?? 0);

export function useProjects() {
  return useQuery({
    queryKey: ["projects"],
    queryFn: async () => {
      // 语义是全量列表（调用方在内存里过滤/排序），按 total 拉完剩余分页，
      // 防止超过 pageSize 的老项目被静默截断
      const pageSize = 100;
      const first = await projectApi.list(1, pageSize);
      const items = [...first.data.items];
      for (let page = 2; items.length < first.data.total; page++) {
        const res = await projectApi.list(page, pageSize);
        items.push(...res.data.items);
        if (res.data.items.length < pageSize) break;
      }
      return { ...first.data, items: items.sort(compareByIssueCount) };
    },
  });
}

export function useProject(id: string) {
  return useQuery({
    queryKey: ["projects", id],
    queryFn: async () => {
      const res = await projectApi.get(id);
      return res.data;
    },
    enabled: !!id,
  });
}

export function useCreateProject() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: projectApi.create,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["projects"] });
    },
  });
}

export function useUpdateProject(id: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (data: Parameters<typeof projectApi.update>[1]) =>
      projectApi.update(id, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["projects"] });
      queryClient.invalidateQueries({ queryKey: ["projects", id] });
    },
  });
}

export function useDeleteProject() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: projectApi.delete,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["projects"] });
    },
  });
}

export function useProjectBranches(projectId: string, enabled = true) {
  return useQuery({
    queryKey: ["projects", projectId, "branches"],
    queryFn: async () => {
      const res = await projectApi.getBranches(projectId);
      return res.data;
    },
    enabled: !!projectId && enabled,
    staleTime: 5 * 60 * 1000, // Cache for 5 minutes
    gcTime: 10 * 60 * 1000,   // Keep in cache for 10 minutes
  });
}
